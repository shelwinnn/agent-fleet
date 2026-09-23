package reconcile

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
)

// fakeDispatcher 记录派发并可注入"节点不可达"。
type fakeDispatcher struct {
	mu       sync.Mutex
	executed []string // machine:opID
	cancels  []string
	fail     bool
}

func (d *fakeDispatcher) ExecuteOperation(_ context.Context, machine string, op *domain.Operation, _ *domain.DesiredStateSnapshot) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail {
		return domain.ErrAgentDisconnected
	}
	d.executed = append(d.executed, machine+":"+op.Metadata.Name)
	return nil
}

func (d *fakeDispatcher) CancelOperation(_ context.Context, machine, opID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail {
		return domain.ErrAgentDisconnected
	}
	d.cancels = append(d.cancels, machine+":"+opID)
	return nil
}

func (d *fakeDispatcher) sentExec(machine string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, e := range d.executed {
		if len(e) > len(machine)+1 && e[:len(machine)+1] == machine+":" {
			n++
		}
	}
	return n
}

// fixture 是控制器级测试环境（真实 SQLite，内存外持久化以验证恢复语义）。
type fixture struct {
	t         *testing.T
	ctx       context.Context
	db        *sqlite.DB
	machines  domain.MachineRepository
	profiles  domain.ProfileRepository
	snapshots domain.SnapshotRepository
	ops       domain.OperationRepository
	observed  domain.ObservedStateRepository
	mstatus   domain.MachineStatusRepository
	rec       *Controller
	disp      *fakeDispatcher
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
		t: t, ctx: context.Background(), db: db,
		machines:  sqlite.NewMachineStore(db),
		profiles:  sqlite.NewProfileStore(db),
		snapshots: sqlite.NewSnapshotStore(db),
		ops:       sqlite.NewOperationStore(db),
		observed:  sqlite.NewObservedStateStore(db),
		mstatus:   sqlite.NewMachineStatusStore(db),
	}
	render := NewRenderer(f.machines, f.profiles, sqlite.NewSkillStore(db),
		sqlite.NewProviderStore(db), "fixture/v1")
	f.rec = NewController(f.machines, f.mstatus, f.snapshots, f.ops, f.observed, render,
		Config{PlanTimeout: 30 * time.Minute, FreshnessWindow: time.Hour}, quietLogger())
	f.disp = &fakeDispatcher{}
	f.rec.SetDispatcher(f.disp)
	return f
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const profileSpecV1 = `{"agents":{"fixture":{"enabled":true,"version":"1.0.0"}}}`
const profileSpecV2 = `{"agents":{"fixture":{"enabled":true,"version":"2.0.0"}}}`

func (f *fixture) seedMachine(name, profileSpec string) {
	f.t.Helper()
	if err := f.profiles.Create(f.ctx, &domain.AgentProfile{
		Metadata: domain.ObjectMeta{Name: "default"}, Spec: json.RawMessage(profileSpec),
	}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.machines.Create(f.ctx, &domain.Machine{
		Metadata: domain.ObjectMeta{Name: name},
		Spec:     json.RawMessage(`{"managementMode":"agentd","profileRef":"default"}`),
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) updateProfile(spec string) {
	f.t.Helper()
	p, err := f.profiles.Get(f.ctx, "default")
	if err != nil {
		f.t.Fatal(err)
	}
	p.SetSpecJSON(json.RawMessage(spec))
	if err := f.profiles.Update(f.ctx, p); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) mustOp(opID string) *domain.Operation {
	f.t.Helper()
	op, err := f.ops.Get(f.ctx, opID)
	if err != nil {
		f.t.Fatal(err)
	}
	return op
}

func (f *fixture) status(machine string) domain.MachineStatus {
	f.t.Helper()
	m, err := f.machines.Get(f.ctx, machine)
	if err != nil {
		f.t.Fatal(err)
	}
	st, err := domain.ParseMachineStatus(m.StatusJSON())
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func (f *fixture) mustCondition(st domain.MachineStatus, cond, wantStatus, wantReason string) {
	f.t.Helper()
	c, ok := st.GetCondition(cond)
	if !ok {
		f.t.Fatalf("condition %s missing (status=%+v)", cond, st)
	}
	if c.Status != wantStatus {
		f.t.Fatalf("condition %s = %s(%s), want %s", cond, c.Status, c.Reason, wantStatus)
	}
	if wantReason != "" && c.Reason != wantReason {
		f.t.Fatalf("condition %s reason = %s, want %s", cond, c.Reason, wantReason)
	}
}

// observationPayload 组装一份观测 JSON（drift 判据两侧 + 投影版本）。
func observationPayload(t *testing.T, desired, observed, version string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(domain.ObservedState{
		InventorySeq: 0, Full: true,
		CanonicalizationVersion:  version,
		DesiredProjectionDigest:  desired,
		ObservedProjectionDigest: observed,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func evidenceEqual(digest string, seq int64) *domain.VerifyEvidence {
	return &domain.VerifyEvidence{
		DesiredProjectionDigest: digest, ObservedProjectionDigest: digest,
		CanonicalizationVersion: "fixture-projection-v1", InventorySeq: seq,
		AdapterHealth: domain.AdapterHealthPassed,
	}
}

// T2：派发点获取 + 互斥（含 AwaitingConfirmation）+ 确认/取消/跳过三例外 +
// 计划超时释放（§4.3/§9.3/T2）。
func TestReconcileMutexAndConfirmationWindow(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("ws-1", profileSpecV1)

	// 正常派发：Pending + ExecuteOperation 下发。
	op1, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if op1.Status.Phase != domain.OperationPhasePending {
		t.Fatalf("phase = %s, want Pending", op1.Status.Phase)
	}
	if n := f.disp.sentExec("ws-1"); n != 1 {
		t.Fatalf("dispatched %d times, want 1", n)
	}
	// 未决期间第二个变更操作 → 409 MachineBusy（FR-1.10）。
	if _, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{}); !errors.Is(err, domain.ErrMachineBusy) {
		t.Fatalf("second reconcile err = %v, want ErrMachineBusy", err)
	}
	ref := f.rec.UnresolvedRef(f.ctx, "ws-1")
	if ref == nil || ref.ID != op1.Metadata.Name {
		t.Fatalf("unresolved ref = %+v", ref)
	}

	// 构造确认窗口：plan → AwaitingConfirmation；互斥仍持有。
	planOp, err := f.rec.Cancel(f.ctx, "ws-1", op1.Metadata.Name)
	if err != nil {
		t.Fatalf("cancel pending op: %v", err)
	}
	if planOp.Status.Phase != domain.OperationPhaseCancelRequested {
		t.Fatalf("cancel of pending dispatch = %s", planOp.Status.Phase)
	}
	// 节点确认取消（可中断阶段前）→ 终态释放。
	if _, err := f.rec.OnResult(f.ctx, "ws-1", OperationResultMsg{
		OperationID: op1.Metadata.Name, Phase: domain.OperationPhaseFailed,
		Reason: "Cancelled", TerminalModifier: domain.ModifierCancelled,
	}); err != nil {
		t.Fatal(err)
	}

	planOp2, err := f.rec.RequestPlanConfirmation(f.ctx, "ws-1", "sha256:plan-1", false)
	if err != nil {
		t.Fatalf("request plan confirmation: %v", err)
	}
	if planOp2.Status.Phase != domain.OperationPhaseAwaitingConfirmation {
		t.Fatalf("plan op = %s", planOp2.Status.Phase)
	}
	if _, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{}); !errors.Is(err, domain.ErrMachineBusy) {
		t.Fatal("AwaitingConfirmation must hold machine mutex (v1.1.2 P-1)")
	}
	// 错误计划确认 → ReplanRequired。
	if _, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{ConfirmPlanDigest: "sha256:other"}); !errors.Is(err, domain.ErrReplanRequired) {
		t.Fatalf("wrong digest err = %v, want ErrReplanRequired", err)
	}
	// 正确确认 → Running 且下发 apply；不创建第二条未决操作。
	confirmed, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{ConfirmPlanDigest: "sha256:plan-1"})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Metadata.Name != planOp2.Metadata.Name || confirmed.Status.Phase != domain.OperationPhaseRunning {
		t.Fatalf("confirm mutated wrong op or phase: %s/%s", confirmed.Metadata.Name, confirmed.Status.Phase)
	}
	if n := f.disp.sentExec("ws-1"); n != 2 {
		t.Fatalf("dispatched %d times after confirm, want 2", n)
	}
	// 终结后互斥释放。
	if _, err := f.rec.OnResult(f.ctx, "ws-1", OperationResultMsg{
		OperationID: confirmed.Metadata.Name, Phase: domain.OperationPhaseSucceeded,
		Verify: evidenceEqual("sha256:d", 1),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{}); err != nil {
		t.Fatalf("reconcile after terminal: %v", err)
	}
}

// T2 扩展：计划超时 → Failed 终结 + 互斥释放（§9.3 第 4 条）。
func TestPlanTimeoutReleasesMutex(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("ws-1", profileSpecV1)
	op, err := f.rec.RequestPlanConfirmation(f.ctx, "ws-1", "sha256:plan-t", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rec.ScanPlanTimeout(f.ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// 未到期不终结。
	if got := f.mustOp(op.Metadata.Name); got.Status.Phase != domain.OperationPhaseAwaitingConfirmation {
		t.Fatalf("early timeout: %s", got.Status.Phase)
	}
	if err := f.rec.ScanPlanTimeout(f.ctx, time.Now().UTC().Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got := f.mustOp(op.Metadata.Name)
	if got.Status.Phase != domain.OperationPhaseFailed || got.Status.PlanExpiresAt == nil {
		t.Fatalf("timeout state: %s expires=%v", got.Status.Phase, got.Status.PlanExpiresAt)
	}
	if got.Spec.PlanDigest != "sha256:plan-t" {
		t.Fatal("plan digest lost on timeout (audit requirement)")
	}
	// 互斥已释放。
	if _, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{}); err != nil {
		t.Fatalf("mutex not released after plan timeout: %v", err)
	}
}

// T3：迟到操作代绑定（FR-9.10）：op@gen12 成功但当前已 gen13 → Succeeded(Superseded)，
// 不写 Reconciled=True，按当前代重新求值 → Unknown(ObservationPredatesDesired)。
func TestLateResultSupersededAndObservationPredatesDesired(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("ws-1", profileSpecV1)
	op1, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{})
	if err != nil {
		t.Fatal(err)
	}
	gen12 := op1.Spec.DesiredGeneration
	// gen12 期间的周期观测（真实 daemon 流程中存在）。
	rec12 := &domain.ObservedStateRecord{Machine: "ws-1", InventorySeq: 2,
		Payload: observationPayload(t, "sha256:gen12-pre", "sha256:gen12-pre", "fixture-projection-v1")}
	f.rec.AssignObservationGeneration(f.ctx, rec12)
	if rec12.ObservedGeneration != gen12 {
		t.Fatalf("gen12 obs bound to %d", rec12.ObservedGeneration)
	}
	if _, err := f.observed.Store(f.ctx, rec12); err != nil {
		t.Fatal(err)
	}
	// op1 执行中 profile 变更 → gen13 物化。
	f.updateProfile(profileSpecV2)
	if _, changed, err := f.rec.EnsureSnapshot(f.ctx, "ws-1"); err != nil || !changed {
		t.Fatalf("materialize gen13: changed=%v err=%v", changed, err)
	}
	cur, _ := f.snapshots.Current(f.ctx, "ws-1")
	if cur.Generation != gen12+1 {
		t.Fatalf("current gen = %d, want %d", cur.Generation, gen12+1)
	}
	// op1 完成：节点按 gen12 快照 verify 通过。
	outcome, err := f.rec.OnResult(f.ctx, "ws-1", OperationResultMsg{
		OperationID: op1.Metadata.Name, Phase: domain.OperationPhaseSucceeded,
		Verify: evidenceEqual("sha256:gen12", 3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Superseded {
		t.Fatal("late success not marked superseded (FR-9.10)")
	}
	got := f.mustOp(op1.Metadata.Name)
	if got.Status.Phase != domain.OperationPhaseSucceeded || got.Status.TerminalModifier != domain.ModifierSuperseded {
		t.Fatalf("terminal = %s(%s), want Succeeded(Superseded)", got.Status.Phase, got.Status.TerminalModifier)
	}
	// 不写 Reconciled=True。
	if st := f.status("ws-1"); true {
		f.mustCondition(st, domain.ConditionReconciled, domain.ConditionUnknown, domain.DriftReasonObservationPredatesDesir)
		f.mustCondition(st, domain.ConditionDrifted, domain.ConditionUnknown, domain.DriftReasonObservationPredatesDesir)
	}
	// 随后 op@gen13 正常收敛 → Drifted=False + Reconciled=True。
	op2, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{})
	if err != nil {
		t.Fatalf("reconcile gen13: %v", err)
	}
	// apply 后观测绑定 op2（因果归属：AssignObservationGeneration）。
	rec := &domain.ObservedStateRecord{Machine: "ws-1", InventorySeq: 9, OperationID: op2.Metadata.Name,
		Payload: observationPayload(t, "sha256:gen13", "sha256:gen13", "fixture-projection-v1")}
	f.rec.AssignObservationGeneration(f.ctx, rec)
	if rec.ObservedGeneration != gen12+1 {
		t.Fatalf("observation bound to gen %d, want %d", rec.ObservedGeneration, gen12+1)
	}
	if _, err := f.observed.Store(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rec.OnResult(f.ctx, "ws-1", OperationResultMsg{
		OperationID: op2.Metadata.Name, Phase: domain.OperationPhaseSucceeded,
		Verify: evidenceEqual("sha256:gen13", 9),
	}); err != nil {
		t.Fatal(err)
	}
	st := f.status("ws-1")
	f.mustCondition(st, domain.ConditionDrifted, domain.ConditionFalse, "")
	f.mustCondition(st, domain.ConditionReconciled, domain.ConditionTrue, "")
}

// T4：观测有序性（FR-8.7）——落后 inventorySeq 的报文不改写条件。
func TestStaleObservationDoesNotRewriteConditions(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("ws-1", profileSpecV1)
	op, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// 先落一份 drift 报文（seq 5）→ Drifted=True。
	rec5 := &domain.ObservedStateRecord{Machine: "ws-1", InventorySeq: 5,
		Payload: observationPayload(t, "sha256:wanted", "sha256:actual", "v1")}
	f.rec.AssignObservationGeneration(f.ctx, rec5)
	if _, err := f.observed.Store(f.ctx, rec5); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rec.EvaluateDrift(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	f.mustCondition(f.status("ws-1"), domain.ConditionDrifted, domain.ConditionTrue, "")
	// 迟到的 seq 3 报文：Store 高水位拒绝；条件保持 True。
	rec3 := &domain.ObservedStateRecord{Machine: "ws-1", InventorySeq: 3,
		Payload: observationPayload(t, "sha256:wanted", "sha256:wanted", "v1")}
	accepted, err := f.observed.Store(f.ctx, rec3)
	if err != nil {
		t.Fatal(err)
	}
	if accepted {
		t.Fatal("stale observation accepted (FR-8.7 violated)")
	}
	if _, err := f.rec.EvaluateDrift(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	f.mustCondition(f.status("ws-1"), domain.ConditionDrifted, domain.ConditionTrue, "")
	_ = op
}

// T17：投影规范化版本不可比 → 不产生 drift 判定，置 Unknown + ProjectionVersionMismatch。
func TestProjectionVersionMismatchYieldsUnknown(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("ws-1", profileSpecV1)
	if _, _, err := f.rec.EnsureSnapshot(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	// 第一份观测建立存储版本 v1（一致）。
	rec1 := &domain.ObservedStateRecord{Machine: "ws-1", InventorySeq: 1,
		Payload: observationPayload(t, "sha256:same", "sha256:same", "v1")}
	f.rec.AssignObservationGeneration(f.ctx, rec1)
	if _, err := f.observed.Store(f.ctx, rec1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rec.EvaluateDrift(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	f.mustCondition(f.status("ws-1"), domain.ConditionDrifted, domain.ConditionFalse, "")
	// 新观测携带不同 canonicalizationVersion → 不可比。
	rec2 := &domain.ObservedStateRecord{Machine: "ws-1", InventorySeq: 2,
		Payload: observationPayload(t, "sha256:same", "sha256:same", "v2")}
	f.rec.AssignObservationGeneration(f.ctx, rec2)
	if _, err := f.observed.Store(f.ctx, rec2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rec.EvaluateDrift(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	f.mustCondition(f.status("ws-1"), domain.ConditionDrifted, domain.ConditionUnknown, domain.ProjectionVersionMismatch)
}

// T19：三态——从未采集 / 期望代已变未采集 / 新鲜度过期（FR-1.9/§6.2）。
func TestDriftTriStateUnknownReasons(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("ws-1", profileSpecV1)
	if _, _, err := f.rec.EnsureSnapshot(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	// NeverInventoried。
	if _, err := f.rec.EvaluateDrift(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	f.mustCondition(f.status("ws-1"), domain.ConditionDrifted, domain.ConditionUnknown, domain.DriftReasonNeverInventoried)
	// 观测到 gen1；升代后 → ObservationPredatesDesired。
	rec1 := &domain.ObservedStateRecord{Machine: "ws-1", InventorySeq: 1,
		Payload: observationPayload(t, "sha256:a", "sha256:a", "v1")}
	f.rec.AssignObservationGeneration(f.ctx, rec1)
	if _, err := f.observed.Store(f.ctx, rec1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rec.EvaluateDrift(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	f.mustCondition(f.status("ws-1"), domain.ConditionDrifted, domain.ConditionFalse, "")
	f.updateProfile(profileSpecV2)
	f.rec.EnsureSnapshot(f.ctx, "ws-1") //nolint:errcheck
	if _, err := f.rec.EvaluateDrift(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	f.mustCondition(f.status("ws-1"), domain.ConditionDrifted, domain.ConditionUnknown, domain.DriftReasonObservationPredatesDesir)
	// StaleObservation：新鲜度窗口扫描（把窗口调小验证）。
	recNow := &domain.ObservedStateRecord{Machine: "ws-1", InventorySeq: 2,
		Payload: observationPayload(t, "sha256:b", "sha256:b", "v1")}
	f.rec.AssignObservationGeneration(f.ctx, recNow)
	if _, err := f.observed.Store(f.ctx, recNow); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rec.EvaluateDrift(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	// lastInventoryAt 由 Machine 控制器维护；测试直接置位以提供新鲜度基准。
	at := time.Now().UTC()
	if err := f.mstatus.UpdateStatus(f.ctx, "ws-1", func(st *domain.MachineStatus) error {
		st.LastInventoryAt = &at
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.rec.ScanFreshness(f.ctx, time.Now().UTC().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.mustCondition(f.status("ws-1"), domain.ConditionDrifted, domain.ConditionUnknown, domain.DriftReasonStaleObservation)
}

// T14：重复结果幂等（不产生第二条变更链）；重启自愈 Running→Unknown 且互斥保持；
// outbox 重报使 Unknown 终结并释放。
func TestDuplicateResultsRestartRecoveryAndOutboxConvergence(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("ws-1", profileSpecV1)
	op, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rec.OnStarted(f.ctx, "ws-1", op.Metadata.Name); err != nil {
		t.Fatal(err)
	}
	res := OperationResultMsg{
		OperationID: op.Metadata.Name, Phase: domain.OperationPhaseSucceeded,
		Verify: evidenceEqual("sha256:x", 2),
	}
	if _, err := f.rec.OnResult(f.ctx, "ws-1", res); err != nil {
		t.Fatal(err)
	}
	// 重放同一结果 → Duplicate（服务端幂等，spec §11.5/FR-13.8）。
	outcome, err := f.rec.OnResult(f.ctx, "ws-1", res)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Duplicate {
		t.Fatal("duplicate result not deduped")
	}
	// 互斥已释放（终态）：新 reconcile 可创建，先将其终结以便后续场景。
	after, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{})
	if err != nil {
		t.Fatalf("reconcile after dup: %v", err)
	}
	if _, err := f.rec.OnResult(f.ctx, "ws-1", OperationResultMsg{
		OperationID: after.Metadata.Name, Phase: domain.OperationPhaseSucceeded,
		Verify: evidenceEqual("sha256:x", 2),
	}); err != nil {
		t.Fatal(err)
	}

	// 重启自愈：Running → Unknown，不移出未决集合（FR-15.3/§4.3 第 4 条）。
	op2, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.rec.OnStarted(f.ctx, "ws-1", op2.Metadata.Name) //nolint:errcheck
	if err := f.rec.RecoverInflight(f.ctx); err != nil {
		t.Fatal(err)
	}
	got := f.mustOp(op2.Metadata.Name)
	if got.Status.Phase != domain.OperationPhaseUnknown {
		t.Fatalf("recovery phase = %s, want Unknown", got.Status.Phase)
	}
	if _, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{}); !errors.Is(err, domain.ErrMachineBusy) {
		t.Fatal("Unknown must keep machine mutex (FR-15.4)")
	}
	// 路径 A：节点 outbox 重报 → Unknown 终结并释放互斥（§9.6）。
	if _, err := f.rec.OnResult(f.ctx, "ws-1", OperationResultMsg{
		OperationID: op2.Metadata.Name, Phase: domain.OperationPhaseSucceeded,
		Verify: evidenceEqual("sha256:x", 3),
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.mustOp(op2.Metadata.Name); got.Status.Phase != domain.OperationPhaseSucceeded {
		t.Fatalf("outbox convergence failed: %s", got.Status.Phase)
	}
	if _, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{}); err != nil {
		t.Fatalf("mutex not released after outbox convergence: %v", err)
	}
}

// T21（服务端）：取消——CancelRequested 保持等待节点；节点不可达不单方面置
// Cancelled；节点确认后 Failed(Cancelled)；重复取消幂等。
func TestCancelSemantics(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("ws-1", profileSpecV1)
	op, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.rec.OnStarted(f.ctx, "ws-1", op.Metadata.Name) //nolint:errcheck
	// 取消 → CancelRequested + 下发 CancelOperation。
	c1, err := f.rec.Cancel(f.ctx, "ws-1", op.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Status.Phase != domain.OperationPhaseCancelRequested {
		t.Fatalf("cancel phase = %s", c1.Status.Phase)
	}
	if len(f.disp.cancels) != 1 {
		t.Fatalf("cancel dispatched %d times, want 1", len(f.disp.cancels))
	}
	// 重复取消幂等。
	if _, err := f.rec.Cancel(f.ctx, "ws-1", op.Metadata.Name); err != nil {
		t.Fatal(err)
	}
	if len(f.disp.cancels) != 1 {
		t.Fatal("repeat cancel re-dispatched")
	}
	// 节点确认停止 → Failed(Cancelled)，互斥释放。
	if _, err := f.rec.OnResult(f.ctx, "ws-1", OperationResultMsg{
		OperationID: op.Metadata.Name, Phase: domain.OperationPhaseFailed,
		Reason: "Cancelled", TerminalModifier: domain.ModifierCancelled,
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.mustOp(op.Metadata.Name); got.Status.Phase != domain.OperationPhaseFailed ||
		got.Status.TerminalModifier != domain.ModifierCancelled {
		t.Fatalf("terminal = %s(%s)", got.Status.Phase, got.Status.TerminalModifier)
	}
	// 节点不可达：CancelRequested 不被单方面终结。
	op2, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.disp.fail = true
	if _, err := f.rec.Cancel(f.ctx, "ws-1", op2.Metadata.Name); err != nil {
		t.Fatal(err)
	}
	if got := f.mustOp(op2.Metadata.Name); got.Status.Phase != domain.OperationPhaseCancelRequested {
		t.Fatalf("unreachable cancel phase = %s, want CancelRequested (FR-13.9)", got.Status.Phase)
	}
}

// 机器级回滚：物化新代（内容 = 目标代内容），不把机器拉回旧代（§7.2/FR-7.5）。
func TestMachineRollbackMaterializesNewGeneration(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("ws-1", profileSpecV1)
	if _, _, err := f.rec.EnsureSnapshot(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	f.updateProfile(profileSpecV2)
	if _, _, err := f.rec.EnsureSnapshot(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	cur, _ := f.snapshots.Current(f.ctx, "ws-1")
	if cur.Generation != 2 {
		t.Fatalf("precondition gen = %d", cur.Generation)
	}
	op, err := f.rec.Rollback(f.ctx, "ws-1", 1)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if op.Spec.Type != domain.OperationTypeRollback || op.Spec.DesiredGeneration != 3 {
		t.Fatalf("rollback op = %s@gen%d", op.Spec.Type, op.Spec.DesiredGeneration)
	}
	newCur, _ := f.snapshots.Current(f.ctx, "ws-1")
	if newCur.Generation != 3 {
		t.Fatalf("rollback did not materialize new generation: %d", newCur.Generation)
	}
	g1, err := f.snapshots.Get(f.ctx, "ws-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if newCur.Digest != g1.Digest {
		t.Fatalf("rollback content != target content: %s vs %s", newCur.Digest, g1.Digest)
	}
	// 不存在的代 → RollbackUnsupported。
	if _, err := f.rec.Rollback(f.ctx, "ws-1", 99); !errors.Is(err, domain.ErrRollbackUnsupported) {
		t.Fatalf("missing generation err = %v, want ErrRollbackUnsupported", err)
	}
}

// 恢复冲突 → Degraded=True（§14.2）。
func TestRestoreConflictSetsDegraded(t *testing.T) {
	f := newFixture(t)
	f.seedMachine("ws-1", profileSpecV1)
	op, err := f.rec.Reconcile(f.ctx, "ws-1", ReconcileRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.rec.OnStarted(f.ctx, "ws-1", op.Metadata.Name) //nolint:errcheck
	if _, err := f.rec.OnResult(f.ctx, "ws-1", OperationResultMsg{
		OperationID: op.Metadata.Name, Phase: domain.OperationPhaseFailed,
		Reason: domain.ReasonRestoreConflict, Message: "restore failed",
	}); err != nil {
		t.Fatal(err)
	}
	f.mustCondition(f.status("ws-1"), domain.ConditionDegraded, domain.ConditionTrue, domain.ReasonRestoreConflict)
}
