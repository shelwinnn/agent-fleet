package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), testLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestMigrateIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	for i := 0; i < 3; i++ {
		if err := db.Migrate(ctx, testLogger()); err != nil {
			t.Fatalf("migrate run %d: %v", i+1, err)
		}
	}
	// 重复执行后登记仍只有一个版本，且全部表存在。
	var n int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	// KM-24 起为四个迁移（+0004_operation_observations）；重复执行后登记数不变。
	if n != 4 {
		t.Fatalf("applied versions = %d, want 4", n)
	}
	for _, table := range []string{"machines", "profiles", "providers", "skills", "deployments", "operations",
		"enrollment_tokens", "agent_certificates", "observed_states", "desired_snapshots",
		"operation_steps", "deployment_targets", "operation_observations"} {
		var name string
		err := db.sql.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing after repeated migrate: %v", table, err)
		}
	}
}

func TestMachineNameUnique(t *testing.T) {
	ctx := context.Background()
	machines := NewMachineStore(openTestDB(t))

	mk := func() *domain.Machine {
		return &domain.Machine{
			Spec: json.RawMessage(`{"managementMode":"agentd","profileRef":"default"}`),
		}
	}
	first := mk()
	first.Metadata.Name = "node-1"
	if err := machines.Create(ctx, first); err != nil {
		t.Fatalf("create: %v", err)
	}
	dup := mk()
	dup.Metadata.Name = "node-1"
	err := machines.Create(ctx, dup)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate create err = %v, want ErrAlreadyExists", err)
	}

	// 创建后查询列已被提取。
	got, err := machines.Get(ctx, "node-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Metadata.UID == "" || got.Metadata.ResourceVersion != 1 {
		t.Fatalf("server metadata not assigned: %+v", got.Metadata)
	}
	var core domain.MachineCoreSpec
	if err := json.Unmarshal(got.Spec, &core); err != nil {
		t.Fatalf("spec: %v", err)
	}
	if core.ProfileRef != "default" {
		t.Fatalf("profileRef = %q, want default", core.ProfileRef)
	}
}

func TestMachineCRUD(t *testing.T) {
	ctx := context.Background()
	machines := NewMachineStore(openTestDB(t))

	m := &domain.Machine{
		Metadata: domain.ObjectMeta{Name: "node-a"},
		Spec:     json.RawMessage(`{"managementMode":"ssh","ssh":{"hostAlias":"node-a"}}`),
	}
	if err := machines.Create(ctx, m); err != nil {
		t.Fatalf("create: %v", err)
	}

	list, err := machines.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v (len=%d)", err, len(list))
	}

	m.Spec = json.RawMessage(`{"managementMode":"ssh","profileRef":"p2"}`)
	if err := machines.Update(ctx, m); err != nil {
		t.Fatalf("update: %v", err)
	}
	if m.Metadata.ResourceVersion != 2 {
		t.Fatalf("resource_version = %d, want 2", m.Metadata.ResourceVersion)
	}
	got, _ := machines.Get(ctx, "node-a")
	if got.Metadata.ResourceVersion != 2 {
		t.Fatalf("stored resource_version = %d, want 2", got.Metadata.ResourceVersion)
	}

	if err := machines.Delete(ctx, "node-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := machines.Get(ctx, "node-a"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
}

// TestOperationsMachineMutex 验证 §4.3/§4.9 的机器级互斥：
// 每台机器至多一个未决操作；进入终态（或显式迁移出未决集合）后释放。
func TestOperationsMachineMutex(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	ops := NewOperationStore(db)

	newOp := func(machine string) *domain.Operation {
		return &domain.Operation{Spec: domain.OperationSpec{
			Machine: machine, Type: domain.OperationTypeReconcile, Transport: domain.TransportSSH,
		}}
	}

	first := newOp("node-a")
	if err := ops.Create(ctx, first); err != nil {
		t.Fatalf("create first: %v", err)
	}
	if first.Status.Phase != domain.OperationPhasePending || first.Metadata.UID == "" {
		t.Fatalf("defaults not applied: %+v", first)
	}

	// 同机第二个未决操作 → 机器忙。
	if err := ops.Create(ctx, newOp("node-a")); !errors.Is(err, domain.ErrMachineBusy) {
		t.Fatalf("concurrent create err = %v, want ErrMachineBusy", err)
	}
	// 异机不受影响。
	if err := ops.Create(ctx, newOp("node-b")); err != nil {
		t.Fatalf("create on other machine: %v", err)
	}

	// 终态释放互斥。
	if err := ops.Transition(ctx, first.Metadata.UID, "", domain.OperationPhaseSucceeded, nil); err != nil {
		t.Fatalf("finish first: %v", err)
	}
	second := newOp("node-a")
	if err := ops.Create(ctx, second); err != nil {
		t.Fatalf("create after terminal: %v", err)
	}

	// Unknown 仍占用互斥（不是终态，FR-15.4）。
	if err := ops.Transition(ctx, second.Metadata.UID, "", domain.OperationPhaseUnknown, nil); err != nil {
		t.Fatalf("mark unknown: %v", err)
	}
	if err := ops.Create(ctx, newOp("node-a")); !errors.Is(err, domain.ErrMachineBusy) {
		t.Fatalf("create during Unknown err = %v, want ErrMachineBusy", err)
	}

	got, err := ops.ListByMachine(ctx, "node-a")
	if err != nil || len(got) != 2 {
		t.Fatalf("list by machine: %v (len=%d)", err, len(got))
	}
}
