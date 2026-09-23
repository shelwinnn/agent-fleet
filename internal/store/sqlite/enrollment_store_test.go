package sqlite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTokenRowFactory 直接构造具体类型（测试同包可见）。
func newTokenRowFactory(db *DB) *enrollmentStore { return &enrollmentStore{db: db.sql} }

func newTokenRow(t *testing.T, ctx context.Context, s *enrollmentStore, machine, fp string, ttl time.Duration) *domain.EnrollmentToken {
	t.Helper()
	tok := &domain.EnrollmentToken{
		TokenHash:      fmt.Sprintf("hash-%s-%s", machine, fp),
		Machine:        machine,
		CSRFingerprint: fp,
		CreatedAt:      time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(ttl),
	}
	if err := s.Create(ctx, tok); err != nil {
		t.Fatalf("create token: %v", err)
	}
	return tok
}

func TestEnrollTokenConsumeOnce(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := newTokenRowFactory(db)

	const fp = "sha256:aaaa"
	tok := newTokenRow(t, ctx, s, "m1", fp, time.Minute)

	if err := s.Consume(ctx, tok.TokenHash, "m1", fp, time.Now().UTC()); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	// 重复消费：一次性（FR-11.5）。
	err := s.Consume(ctx, tok.TokenHash, "m1", fp, time.Now().UTC())
	if !errors.Is(err, domain.ErrEnrollmentRejected) {
		t.Fatalf("second consume: want ErrEnrollmentRejected, got %v", err)
	}
	// Get 仍返回登记（供审计分类），且 used_at 非空。
	got, err := s.Get(ctx, tok.TokenHash)
	if err != nil {
		t.Fatalf("get after consume: %v", err)
	}
	if got.UsedAt == nil {
		t.Fatal("used_at should be set after consume")
	}
}

func TestEnrollTokenBindingAndExpiry(t *testing.T) {
	ctx := context.Background()
	s := newTokenRowFactory(testDB(t))

	tok := newTokenRow(t, ctx, s, "m1", "sha256:bbbb", time.Minute)
	// 机器绑定不符。
	if err := s.Consume(ctx, tok.TokenHash, "OTHER", "sha256:bbbb", time.Now().UTC()); !errors.Is(err, domain.ErrEnrollmentRejected) {
		t.Fatalf("machine mismatch: want reject, got %v", err)
	}
	// CSR 指纹绑定不符。
	if err := s.Consume(ctx, tok.TokenHash, "m1", "sha256:cccc", time.Now().UTC()); !errors.Is(err, domain.ErrEnrollmentRejected) {
		t.Fatalf("csr mismatch: want reject, got %v", err)
	}
	// 过期。
	expired := newTokenRow(t, ctx, s, "m2", "sha256:dddd", -time.Minute)
	if err := s.Consume(ctx, expired.TokenHash, "m2", "sha256:dddd", time.Now().UTC()); !errors.Is(err, domain.ErrEnrollmentRejected) {
		t.Fatalf("expired: want reject, got %v", err)
	}
	// 未登记的 token 哈希。
	if err := s.Consume(ctx, "no-such-hash", "m1", "sha256:bbbb", time.Now().UTC()); !errors.Is(err, domain.ErrEnrollmentRejected) {
		t.Fatalf("unknown token: want reject, got %v", err)
	}
}

func TestEnrollTokenConcurrentConsumeExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	s := newTokenRowFactory(testDB(t))
	const fp = "sha256:eeee"
	tok := newTokenRow(t, ctx, s, "m1", fp, time.Minute)

	const n = 16
	var wg sync.WaitGroup
	wins := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Consume(ctx, tok.TokenHash, "m1", fp, time.Now().UTC()); err == nil {
				wins <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(wins)
	if got := len(wins); got != 1 {
		t.Fatalf("concurrent consume: want exactly 1 winner, got %d", got)
	}
}

func TestObservedStateHighWaterMark(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	machines := NewMachineStore(db)
	obs := NewObservedStateStore(db)

	if err := machines.Create(ctx, &domain.Machine{Metadata: domain.ObjectMeta{Name: "m1"}}); err != nil {
		t.Fatal(err)
	}
	store := func(seq int64) bool {
		t.Helper()
		accepted, err := obs.Store(ctx, &domain.ObservedStateRecord{
			Machine:      "m1",
			InventorySeq: seq,
			Payload:      []byte(fmt.Sprintf(`{"inventorySeq":%d}`, seq)),
			RecordedAt:   time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("store seq %d: %v", seq, err)
		}
		return accepted
	}
	if !store(5) {
		t.Fatal("seq 5 should be accepted")
	}
	if store(3) {
		t.Fatal("stale seq 3 must not rewrite (FR-8.7)")
	}
	if !store(5) {
		t.Fatal("equal seq 5 should be accepted (idempotent redelivery)")
	}
	if !store(6) {
		t.Fatal("seq 6 should be accepted")
	}
	got, err := obs.Latest(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.InventorySeq != 6 {
		t.Fatalf("latest seq = %d, want 6", got.InventorySeq)
	}
	if _, err := obs.Latest(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("latest for missing machine: want ErrNotFound, got %v", err)
	}
}

func TestMachineStatusUpdate(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	machines := NewMachineStore(db)
	status := NewMachineStatusStore(db)

	if err := machines.Create(ctx, &domain.Machine{Metadata: domain.ObjectMeta{Name: "m1"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	err := status.UpdateStatus(ctx, "m1", func(st *domain.MachineStatus) error {
		st.SetCondition(domain.ConditionAgentConnected, domain.ConditionTrue, "Connected", "up", now)
		return nil
	})
	if err != nil {
		t.Fatalf("update status: %v", err)
	}
	m, err := machines.Get(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	st, err := domain.ParseMachineStatus(m.StatusJSON())
	if err != nil {
		t.Fatal(err)
	}
	cond, ok := st.GetCondition(domain.ConditionAgentConnected)
	if !ok || cond.Status != domain.ConditionTrue || cond.Reason != "Connected" {
		t.Fatalf("condition not written: %+v ok=%v", cond, ok)
	}
	if m.Meta().ResourceVersion != 2 {
		t.Fatalf("resource version = %d, want 2 (status write bumps rv)", m.Meta().ResourceVersion)
	}
	if err := status.UpdateStatus(ctx, "missing", func(*domain.MachineStatus) error { return nil }); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update missing machine: want ErrNotFound, got %v", err)
	}
}

func TestWithBusyRetryCountsAndRetries(t *testing.T) {
	before := BusyRetriesTotal()
	calls := 0
	err := withBusyRetry(context.Background(), "test", func() error {
		calls++
		if calls < 3 {
			return errors.New("sqlite: update machine: database is locked (5) (SQLITE_BUSY)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withBusyRetry: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if after := BusyRetriesTotal(); after-before != 2 {
		t.Fatalf("busy counter delta = %d, want 2", after-before)
	}
}

func TestCertificateRetireByMachine(t *testing.T) {
	ctx := context.Background()
	s := newTokenRowFactory(testDB(t))
	now := time.Now().UTC()
	if err := s.Record(ctx, &domain.AgentCertificate{
		Serial: "ab01", Machine: "m1", NotBefore: now, NotAfter: now.Add(time.Hour), IssuedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireByMachine(ctx, "m1", now); err != nil {
		t.Fatal(err)
	}
	// 幂等：重复退役不报错。
	if err := s.RetireByMachine(ctx, "m1", now); err != nil {
		t.Fatal(err)
	}
}
