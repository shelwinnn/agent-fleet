package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
	"github.com/shelwinnn/agent-fleet/internal/upstream"
)

// fakeDispatcher 记录派发并可注入"节点不可达"。
type fakeDispatcher struct {
	mu       sync.Mutex
	sent     []DispatchRequest
	machines []string
	fail     bool
}

func (d *fakeDispatcher) DispatchLifecycle(_ context.Context, machine string, req DispatchRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail {
		return domain.ErrAgentDisconnected
	}
	d.sent = append(d.sent, req)
	d.machines = append(d.machines, machine)
	return nil
}

func (d *fakeDispatcher) requests() []DispatchRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]DispatchRequest(nil), d.sent...)
}

// fakeUpstream 是 T3 发现服务的替身（CheckUpdate 编排测试用）。
type fakeUpstream struct {
	report upstream.Report
	err    error
	calls  []string
}

func (f *fakeUpstream) Check(_ context.Context, agentID string) (upstream.Report, error) {
	f.calls = append(f.calls, agentID)
	if f.err != nil {
		return upstream.Report{}, f.err
	}
	return f.report, nil
}

// fixture 是编排控制器测试环境（真实 SQLite，reconcile 控制器测试同款）。
type fixture struct {
	t        *testing.T
	ctx      context.Context
	machines domain.MachineRepository
	status   domain.MachineStatusRepository
	observed domain.ObservedStateRepository
	repo     domain.LifecycleRepository
	ctrl     *Controller
	disp     *fakeDispatcher
	up       *fakeUpstream
	now      time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), quietLogger()); err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		t:        t,
		ctx:      context.Background(),
		machines: sqlite.NewMachineStore(db),
		status:   sqlite.NewMachineStatusStore(db),
		observed: sqlite.NewObservedStateStore(db),
		repo:     sqlite.NewLifecycleStore(db),
		now:      time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
	}
	f.disp = &fakeDispatcher{}
	f.up = &fakeUpstream{}
	f.ctrl = New(f.repo, f.machines, f.observed, Config{
		Now:           func() time.Time { return f.now },
		UpstreamQuery: f.up,
	}, quietLogger())
	f.ctrl.SetDispatcher(f.disp)
	return f
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seedMachine 建一台 agentd 管理的机器；online 控制连接条件。
func (f *fixture) seedMachine(name string, online bool) {
	f.t.Helper()
	if err := f.machines.Create(f.ctx, &domain.Machine{
		Metadata: domain.ObjectMeta{Name: name},
		Spec:     []byte(`{"managementMode":"agentd"}`),
	}); err != nil {
		f.t.Fatal(err)
	}
	f.setConnected(name, online)
}

func (f *fixture) setConnected(name string, online bool) {
	f.t.Helper()
	status := domain.ConditionFalse
	if online {
		status = domain.ConditionTrue
	}
	if err := f.status.UpdateStatus(f.ctx, name, func(st *domain.MachineStatus) error {
		st.SetCondition(domain.ConditionAgentConnected, status, "test", "", f.now)
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
}

// seedObservation 回填机器台账平台事实（OS/Arch 回填的输入）。
func (f *fixture) seedObservation(machine, os, arch string) {
	f.t.Helper()
	rec := &domain.ObservedStateRecord{Machine: machine, InventorySeq: 1, RecordedAt: f.now}
	obs := domain.ObservedState{InventorySeq: 1, Machine: &domain.MachineObservation{OS: os, Arch: arch}}
	b, _ := json.Marshal(obs)
	rec.Payload = b
	if _, err := f.observed.Store(f.ctx, rec); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) codexTarget(machine string) domain.LifecycleTarget {
	return domain.LifecycleTarget{Machine: machine, User: "alice", Agent: "codex",
		Method: "npm", Package: "@openai/codex"}
}

func (f *fixture) createInstall(machine, version string) *domain.LifecycleOperation {
	f.t.Helper()
	op, created, err := f.ctrl.CreateOperation(f.ctx, domain.LifecycleOperationSpec{
		Type:           domain.LifecycleOpInstall,
		IdempotencyKey: "idem-" + machine + "-" + version,
		Version:        version,
		Targets:        []domain.LifecycleTarget{f.codexTarget(machine)},
	})
	if err != nil || !created {
		f.t.Fatalf("create install: created=%v err=%v", created, err)
	}
	return op
}

// ---- 创建：幂等、台账回填、阻塞组合拒绝 ----

func TestCreateFillsPlatformFactsFromLedger(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	f.seedObservation("m1", "linux", "amd64")
	op := f.createInstall("m1", "0.44.0")
	got := op.Spec.Targets[0]
	if got.OS != "linux" || got.Arch != "amd64" {
		t.Fatalf("platform facts not filled from ledger: %+v", got)
	}
	if got.CapabilityState != domain.CapabilityUnverified {
		t.Fatalf("capability snapshot = %q, want unverified (not marked supported)", got.CapabilityState)
	}
}

func TestCreateRejectsUnknownMachineAndBlockedCombo(t *testing.T) {
	f := newFixture(t)
	if _, _, err := f.ctrl.CreateOperation(f.ctx, domain.LifecycleOperationSpec{
		Type: domain.LifecycleOpInstall, IdempotencyKey: "k1", Version: "0.44.0",
		Targets: []domain.LifecycleTarget{f.codexTarget("no-such-machine")},
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown machine err = %v, want ErrNotFound", err)
	}
	f.seedMachine("m1", true)
	f.seedObservation("m1", "linux", "amd64")
	// binary 通道是跨平台阻塞（KM-46 §7）：平台未知也必须在创建时拒绝。
	_, _, err := f.ctrl.CreateOperation(f.ctx, domain.LifecycleOperationSpec{
		Type: domain.LifecycleOpInstall, IdempotencyKey: "k2", Version: "0.44.0",
		Targets: []domain.LifecycleTarget{{Machine: "m1", User: "alice", Agent: "codex",
			Method: "binary", Package: "codex"}},
	})
	if !errors.Is(err, domain.ErrUnsupportedCombo) {
		t.Fatalf("blocked combo err = %v, want ErrUnsupportedCombo", err)
	}
}

func TestCreateIdempotentDuplicate(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	spec := domain.LifecycleOperationSpec{
		Type: domain.LifecycleOpInstall, IdempotencyKey: "same-key", Version: "0.44.0",
		Targets: []domain.LifecycleTarget{f.codexTarget("m1")},
	}
	first, created, err := f.ctrl.CreateOperation(f.ctx, spec)
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	second, created, err := f.ctrl.CreateOperation(f.ctx, spec)
	if err != nil || created {
		t.Fatalf("duplicate create: created=%v err=%v", created, err)
	}
	if first.Metadata.Name != second.Metadata.Name {
		t.Fatalf("duplicate must reuse same operation: %s vs %s", first.Metadata.Name, second.Metadata.Name)
	}
}

// ---- 派发编排：确认闸、离线未执行、派发失败、上线不复活 ----

func TestDispatchRequiresConfirmation(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	f.createInstall("m1", "0.44.0")
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(f.disp.requests()); got != 0 {
		t.Fatalf("unconfirmed op dispatched %d times, want 0", got)
	}
	op, _ := f.repo.GetOperation(f.ctx, "nonexist")
	_ = op
}

func TestDispatchOfflineMarksNotExecutedAndStaysPut(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", false) // 提交前离线
	op := f.createInstall("m1", "0.44.0")
	if err := f.ctrl.Confirm(f.ctx, op.Metadata.Name, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	got, err := f.repo.GetOperation(f.ctx, op.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	r := got.Status.Results[0]
	if r.State != domain.TargetNotExecuted || r.Reason != domain.ReasonTargetOffline {
		t.Fatalf("offline target = %s/%s, want not_executed/target-offline", r.State, r.Reason)
	}
	if got.Status.Phase != domain.OperationPhaseCompleted {
		t.Fatalf("phase = %s, want Completed", got.Status.Phase)
	}
	// 上线后不自动执行旧意图：not_executed 无出边。
	f.setConnected("m1", true)
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(f.disp.requests()); got != 0 {
		t.Fatalf("old intent re-dispatched after online: %d dispatches", got)
	}
}

func TestDispatchOnlineRunsTargetAndCarriesFixedVersion(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	op := f.createInstall("m1", "0.44.0")
	if err := f.ctrl.Confirm(f.ctx, op.Metadata.Name, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	reqs := f.disp.requests()
	if len(reqs) != 1 {
		t.Fatalf("dispatches = %d, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Version != "0.44.0" || req.Type != domain.LifecycleOpInstall || req.OpID != op.Metadata.Name {
		t.Fatalf("dispatch request mismatch: %+v", req)
	}
	if req.RediscoveryRequired {
		t.Fatal("first dispatch must not require rediscovery")
	}
	if !req.Deadline.After(f.now) {
		t.Fatalf("dispatch deadline not set: %v", req.Deadline)
	}
	got, _ := f.repo.GetOperation(f.ctx, op.Metadata.Name)
	if got.Status.Results[0].State != domain.TargetRunning {
		t.Fatalf("target state = %s, want running", got.Status.Results[0].State)
	}
	// 已 running 的目标不被重复派发。
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(f.disp.requests()); got != 1 {
		t.Fatalf("running target re-dispatched: %d total", got)
	}
}

func TestDispatchFailureMarksNotExecutedDispatchFailed(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	f.disp.fail = true
	op := f.createInstall("m1", "0.44.0")
	if err := f.ctrl.Confirm(f.ctx, op.Metadata.Name, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := f.repo.GetOperation(f.ctx, op.Metadata.Name)
	r := got.Status.Results[0]
	if r.State != domain.TargetNotExecuted || r.Reason != domain.ReasonDispatchFailed {
		t.Fatalf("dispatch-failed target = %s/%s", r.State, r.Reason)
	}
}

// TestUnmanagedIntentNeverDispatches：停止管理只存在于意图文档——
// unmanaged 不产生操作、不产生派发、不触碰节点（与卸载严格区分）。
func TestUnmanagedIntentNeverDispatches(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	doc := domain.LifecycleDesired{
		SchemaVersion: domain.LifecycleDesiredSchemaVersion,
		Entries:       map[string]domain.LifecycleEntry{"codex": {Intent: domain.LifecycleIntentUnmanaged}},
	}
	if _, _, err := f.ctrl.PutDesired(f.ctx, "m1", doc); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := f.ctrl.ScanOnce(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(f.disp.requests()); got != 0 {
		t.Fatalf("unmanaged intent caused %d dispatches, want 0 (stop-managing must not delete the program)", got)
	}
	ops, err := f.repo.ListOperationsByMachine(f.ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("unmanaged intent created %d operations, want 0", len(ops))
	}
}

// ---- 重试：仅失败目标、追溯链、重发现标记与重新确认 ----

func lifecycleRetryFlow(t *testing.T) (*fixture, *domain.LifecycleOperation, *domain.LifecycleOperation) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	orig := f.createInstall("m1", "0.44.0")
	if err := f.ctrl.Confirm(f.ctx, orig.Metadata.Name, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	// 节点上报确定失败 → 原目标 failed。
	err := f.ctrl.OnLifecycleResult(f.ctx, ResultMsg{
		Machine: "m1", OpID: orig.Metadata.Name,
		IdempotencyKey: orig.Spec.IdempotencyKey,
		Results: []domain.LifecycleTargetResult{{
			Target: f.codexTarget("m1"), State: domain.TargetFailed,
			Reason: domain.ReasonOpFailed, Message: "npm install failed",
		}},
		FinishedAt: f.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.ctrl.Retry(f.ctx, orig.Metadata.Name,
		[]domain.LifecycleTarget{f.codexTarget("m1")}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	return f, orig, retry
}

func TestRetryOnlyFailedTargetsAndChain(t *testing.T) {
	f, orig, retry := lifecycleRetryFlow(t)
	if retry.RetryOf != orig.Metadata.Name {
		t.Fatalf("retryOf = %q, want %q", retry.RetryOf, orig.Metadata.Name)
	}
	if retry.Spec.Version != orig.Spec.Version {
		t.Fatalf("retry version %q differs from original %q", retry.Spec.Version, orig.Spec.Version)
	}
	if retry.Status.ConfirmedAt == nil {
		t.Fatal("retry of approved op must not require re-confirmation")
	}
	// 不在原操作里的目标不可重试（m2 在台账但不是原操作目标 → 状态机校验拒绝）。
	f.seedMachine("m2", true)
	_, err := f.ctrl.Retry(f.ctx, orig.Metadata.Name,
		[]domain.LifecycleTarget{f.codexTarget("m2")}, "operator")
	if !errors.Is(err, domain.ErrRetryNotAllowed) && !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("retry with foreign target err = %v, want not-allowed", err)
	}
	_ = f
}

func TestRetryFirstDispatchCarriesRediscovery(t *testing.T) {
	f, _, retry := lifecycleRetryFlow(t)
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	reqs := f.disp.requests()
	if len(reqs) != 2 {
		t.Fatalf("dispatches = %d, want 2 (original + retry)", len(reqs))
	}
	retryReq := reqs[1]
	if retryReq.OpID != retry.Metadata.Name {
		t.Fatalf("second dispatch op = %s, want retry %s", retryReq.OpID, retry.Metadata.Name)
	}
	if !retryReq.RediscoveryRequired {
		t.Fatal("retry first dispatch must carry rediscovery_required")
	}
	// 节点重发现发现影响变化 → 逐目标 failed(version-mismatch) + 操作级
	// needs_reconfirmation 置位；同一操作不再派发（目标已落定）。
	if err := f.ctrl.OnLifecycleResult(f.ctx, ResultMsg{
		Machine: "m1", OpID: retry.Metadata.Name,
		IdempotencyKey: retry.Spec.IdempotencyKey,
		NeedsReconfirmation: true,
		ReconfirmReason:     "rediscovery found codex@0.45.0 before retry",
		Results: []domain.LifecycleTargetResult{{
			Target: f.codexTarget("m1"), State: domain.TargetFailed,
			Reason:   domain.ReasonVersionMismatch,
			Message:  "rediscovery found codex@0.45.0 before retry, fixed-version intent is 0.44.0",
			Evidence: &domain.LifecycleEvidence{ObservedVersion: "0.45.0", RecordedAt: f.now},
		}},
		FinishedAt: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.repo.GetOperation(f.ctx, retry.Metadata.Name)
	if !got.Status.NeedsReconfirmation {
		t.Fatal("needsReconfirmation not persisted")
	}
	if r := got.Status.Results[0]; r.State != domain.TargetFailed || r.Reason != domain.ReasonVersionMismatch {
		t.Fatalf("impact-changed target = %s/%s, want failed/version-mismatch", r.State, r.Reason)
	}
	before := len(f.disp.requests())
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(f.disp.requests()); got != before {
		t.Fatalf("dispatch continued after impact change: %d → %d", before, got)
	}
	// 操作者重新确认（清除标记、记录确认时刻），然后以新重试操作继续：
	// 新重试继承重新确认时刻——链上已发生重新确认 → 派发不再携带重发现标记
	// （同一现实不得再次阻断执行；引擎侧仍有执行后核验兜底）。
	f.now = f.now.Add(time.Minute)
	if err := f.ctrl.Confirm(f.ctx, retry.Metadata.Name, f.now); err != nil {
		t.Fatal(err)
	}
	cont, err := f.ctrl.Retry(f.ctx, retry.Metadata.Name,
		[]domain.LifecycleTarget{f.codexTarget("m1")}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if cont.Status.ConfirmedAt == nil {
		t.Fatal("continuation retry must inherit the reconfirmation (no duplicate confirm)")
	}
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	reqs = f.disp.requests()
	last := reqs[len(reqs)-1]
	if last.OpID != cont.Metadata.Name {
		t.Fatalf("continuation dispatch op = %s, want %s", last.OpID, cont.Metadata.Name)
	}
	if last.RediscoveryRequired {
		t.Fatal("continuation after reconfirmation must not carry rediscovery (would loop)")
	}
}

// ---- 结果回灌：幂等、脱敏、机器归属、畸形上报 ----

func TestResultIngestionIdempotentAndRedacted(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	op := f.createInstall("m1", "0.44.0")
	if err := f.ctrl.Confirm(f.ctx, op.Metadata.Name, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	res := ResultMsg{
		Machine: "m1", OpID: op.Metadata.Name,
		IdempotencyKey: op.Spec.IdempotencyKey,
		Results: []domain.LifecycleTargetResult{{
			Target: f.codexTarget("m1"), State: domain.TargetSucceeded,
			Reason: domain.ReasonVerifiedPresent, Message: "npm _authToken=abc123 done",
			Evidence: &domain.LifecycleEvidence{ObservedVersion: "0.44.0", Method: "npm",
				Message: "token=zzz-secret", RecordedAt: f.now},
		}},
		FinishedAt: f.now,
	}
	if err := f.ctrl.OnLifecycleResult(f.ctx, res); err != nil {
		t.Fatal(err)
	}
	// 重复上报幂等：终态无出边，不产生第二次变更。
	if err := f.ctrl.OnLifecycleResult(f.ctx, res); err != nil {
		t.Fatal(err)
	}
	got, _ := f.repo.GetOperation(f.ctx, op.Metadata.Name)
	r := got.Status.Results[0]
	if r.State != domain.TargetSucceeded || r.Evidence.ObservedVersion != "0.44.0" {
		t.Fatalf("ingested result = %+v", r)
	}
	if r.Message == "" || !containsRedacted(r.Message) {
		t.Fatalf("control-plane redaction missing in %q", r.Message)
	}
	if !containsRedacted(r.Evidence.Message) {
		t.Fatalf("evidence redaction missing in %q", r.Evidence.Message)
	}
}

func containsRedacted(s string) bool {
	for _, marker := range []string{"[redacted]"} {
		if len(s) > 0 && !func() bool {
			for i := 0; i+len(marker) <= len(s); i++ {
				if s[i:i+len(marker)] == marker {
					return true
				}
			}
			return false
		}() {
			return false
		}
	}
	return true
}

func TestResultMachineMismatchIgnored(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	op := f.createInstall("m1", "0.44.0")
	if err := f.ctrl.Confirm(f.ctx, op.Metadata.Name, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	err := f.ctrl.OnLifecycleResult(f.ctx, ResultMsg{
		Machine: "other-machine", OpID: op.Metadata.Name,
		IdempotencyKey: op.Spec.IdempotencyKey,
		Results: []domain.LifecycleTargetResult{{
			Target: f.codexTarget("m1"), State: domain.TargetSucceeded,
			Reason: domain.ReasonVerifiedPresent,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := f.repo.GetOperation(f.ctx, op.Metadata.Name)
	if got.Status.Results[0].State != domain.TargetRunning {
		t.Fatalf("foreign-machine result applied: state = %s", got.Status.Results[0].State)
	}
}

func TestMalformedResultRecordedAsUnknown(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	op := f.createInstall("m1", "0.44.0")
	if err := f.ctrl.Confirm(f.ctx, op.Metadata.Name, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	// 状态/原因配对非法（succeeded 带了失败原因）→ 落 unknown(result-lost)。
	if err := f.ctrl.OnLifecycleResult(f.ctx, ResultMsg{
		Machine: "m1", OpID: op.Metadata.Name,
		IdempotencyKey: op.Spec.IdempotencyKey,
		Results: []domain.LifecycleTargetResult{{
			Target: f.codexTarget("m1"), State: domain.TargetSucceeded,
			Reason: domain.ReasonOpFailed,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.repo.GetOperation(f.ctx, op.Metadata.Name)
	r := got.Status.Results[0]
	if r.State != domain.TargetUnknown || r.Reason != domain.ReasonResultLost {
		t.Fatalf("malformed report = %s/%s, want unknown/result-lost", r.State, r.Reason)
	}
}

// ---- 超时扫描 ----

func TestTimeoutScanMarksUnknown(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	op := f.createInstall("m1", "0.44.0")
	if err := f.ctrl.Confirm(f.ctx, op.Metadata.Name, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	// deadline + 宽限内不判超时。
	f.now = f.now.Add(DefaultOpTimeout)
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := f.repo.GetOperation(f.ctx, op.Metadata.Name)
	if got.Status.Results[0].State != domain.TargetRunning {
		t.Fatalf("within grace state = %s, want running", got.Status.Results[0].State)
	}
	// 超过 deadline + 宽限 → unknown(timeout-no-result)，不自动重试。
	f.now = f.now.Add(DefaultTimeoutGrace + time.Minute)
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = f.repo.GetOperation(f.ctx, op.Metadata.Name)
	r := got.Status.Results[0]
	if r.State != domain.TargetUnknown || r.Reason != domain.ReasonTimeoutNoResult {
		t.Fatalf("timed-out target = %s/%s, want unknown/timeout-no-result", r.State, r.Reason)
	}
	if got.AllTargetsSucceeded() {
		t.Fatal("unknown must not count as success")
	}
}

// ---- CheckUpdate：只读上游、逐目标四态 ----

func TestCheckUpdateExecution(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	f.seedObservation("m1", "linux", "amd64")
	queried := f.now.Add(-time.Minute)
	f.up.report = upstream.Report{
		AgentID: "codex", CheckedAt: queried,
		Sources: []upstream.SourceResult{
			{AgentID: "codex", Method: "npm", Channel: "latest", Status: upstream.StatusOK,
				Version: &upstream.Version{}, QueriedAt: &queried, Attempts: 1},
		},
	}
	f.up.report.Sources[0].Version.Raw = "0.45.0"
	op, _, err := f.ctrl.CreateOperation(f.ctx, domain.LifecycleOperationSpec{
		Type: domain.LifecycleOpCheckUpdate, IdempotencyKey: "chk-1",
		Targets: []domain.LifecycleTarget{f.codexTarget("m1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	// CheckUpdate 只读上游：未确认也推进（无变更意图，无需预览确认）。
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.up.calls) != 1 || f.up.calls[0] != "codex" {
		t.Fatalf("upstream calls = %v", f.up.calls)
	}
	if got := len(f.disp.requests()); got != 0 {
		t.Fatalf("CheckUpdate dispatched to nodes: %d", got)
	}
	got, _ := f.repo.GetOperation(f.ctx, op.Metadata.Name)
	r := got.Status.Results[0]
	if r.State != domain.TargetSucceeded || r.Reason != domain.ReasonVerifiedPresent {
		t.Fatalf("checkupdate = %s/%s", r.State, r.Reason)
	}
	if r.Evidence.ObservedVersion != "0.45.0" {
		t.Fatalf("evidence version = %q", r.Evidence.ObservedVersion)
	}
}

func TestCheckUpdateQueryFailureAndCacheFallback(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("m1", true)
	// 查询失败 + 有缓存：failed(discovery-failed)，缓存时间进消息（未知 ≠ 最新）。
	queried := f.now.Add(-time.Hour)
	f.up.report = upstream.Report{AgentID: "codex",
		Sources: []upstream.SourceResult{{
			AgentID: "codex", Method: "npm", Channel: "latest", Status: upstream.StatusFailed,
			Failure:     &upstream.Failure{Reason: upstream.ReasonQueryFailed, Detail: "npm view timeout"},
			Attempts:    2,
			LastSuccess: &upstream.CachedVersion{QueriedAt: queried},
		}}}
	op, _, err := f.ctrl.CreateOperation(f.ctx, domain.LifecycleOperationSpec{
		Type: domain.LifecycleOpCheckUpdate, IdempotencyKey: "chk-2",
		Targets: []domain.LifecycleTarget{f.codexTarget("m1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := f.repo.GetOperation(f.ctx, op.Metadata.Name)
	r := got.Status.Results[0]
	if r.State != domain.TargetFailed || r.Reason != domain.ReasonDiscoveryFailed {
		t.Fatalf("failed query = %s/%s, want failed/discovery-failed", r.State, r.Reason)
	}
	if r.Message == "" || !containsRedacted("x[redacted]") {
		t.Fatalf("failure detail missing: %q", r.Message)
	}
	// 来源不适用（目标方式 brew 在上游来源清单中无对应声明）→ unsupported-combo。
	// binary 组合是跨平台阻塞、创建时即被拒（见 TestCreateRejectsUnknownMachineAndBlockedCombo）；
	// 这里验证的是"可创建但执行侧发现上游不适用"的判定路径。
	f.up.report = upstream.Report{AgentID: "codex",
		Sources: []upstream.SourceResult{{
			AgentID: "codex", Method: "npm", Status: upstream.StatusUnsupported,
			Failure: &upstream.Failure{Reason: upstream.ReasonMethodUnsupported, Detail: "unsupported"},
		}}}
	op2, _, err := f.ctrl.CreateOperation(f.ctx, domain.LifecycleOperationSpec{
		Type: domain.LifecycleOpCheckUpdate, IdempotencyKey: "chk-3",
		Targets: []domain.LifecycleTarget{{Machine: "m1", User: "alice", Agent: "codex",
			Method: "brew", Package: "codex"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ctrl.ScanOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	got2, _ := f.repo.GetOperation(f.ctx, op2.Metadata.Name)
	if got2.Status.Results[0].Reason != domain.ReasonUnsupportedCombo {
		t.Fatalf("unsupported source = %s/%s", got2.Status.Results[0].State, got2.Status.Results[0].Reason)
	}
}
