package deployment

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/controller/reconcile"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
)

// fixture 装配真实 reconcile 控制器（fakeDispatcher）+ 真实 deployment 控制器，
// 走 §4.4 的完整推进路径。
type depFixture struct {
	t        *testing.T
	ctx      context.Context
	machines domain.MachineRepository
	profiles domain.ProfileRepository
	snap     domain.SnapshotRepository
	ops      domain.OperationRepository
	observed domain.ObservedStateRepository
	rec      *reconcile.Controller
	dep      *Controller
}

func newDepFixture(t *testing.T) *depFixture {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), quietLogDep()); err != nil {
		t.Fatal(err)
	}
	f := &depFixture{
		t:   t,
		ctx: context.Background(),
	}
	machines := sqlite.NewMachineStore(db)
	status := sqlite.NewMachineStatusStore(db)
	profiles := sqlite.NewProfileStore(db)
	snap := sqlite.NewSnapshotStore(db)
	ops := sqlite.NewOperationStore(db)
	observed := sqlite.NewObservedStateStore(db)

	disp := &depDispatcher{}
	render := reconcile.NewRenderer(machines, profiles, sqlite.NewSkillStore(db),
		sqlite.NewProviderStore(db), "fixture/v1")
	rec := reconcile.NewController(machines, status, snap, ops, observed, render,
		reconcile.Config{PlanTimeout: time.Minute, FreshnessWindow: time.Hour}, quietLogDep())
	rec.SetDispatcher(disp)

	f.machines, f.profiles, f.snap, f.ops, f.observed = machines, profiles, snap, ops, observed
	f.rec = rec
	f.dep = NewController(sqlite.NewDeploymentStore(db), sqlite.NewDeploymentTargetStore(db),
		machines, snap, ops, observed, rec, quietLogDep())
	return f
}

// depDispatcher 是 reconcile 控制器的派发桩（节点视为可达）。
type depDispatcher struct{ failed int }

func (d *depDispatcher) ExecuteOperation(_ context.Context, _ string, _ *domain.Operation, _ *domain.DesiredStateSnapshot) error {
	return nil
}

func (d *depDispatcher) CancelOperation(_ context.Context, _, _ string) error { d.failed++; return nil }

func quietLogDep() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func (f *depFixture) seedMachine(name string) {
	f.t.Helper()
	if err := f.machines.Create(f.ctx, &domain.Machine{
		Metadata: domain.ObjectMeta{Name: name},
		Spec:     json.RawMessage(`{"managementMode":"agentd","profileRef":"default"}`),
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *depFixture) setProfile(spec string) {
	f.t.Helper()
	if err := f.profiles.Create(f.ctx, &domain.AgentProfile{
		Metadata: domain.ObjectMeta{Name: "default"}, Spec: json.RawMessage(spec),
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *depFixture) updateProfile(spec string) {
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

func (f *depFixture) materialize(name string) {
	f.t.Helper()
	if _, _, err := f.rec.EnsureSnapshot(f.ctx, name); err != nil {
		f.t.Fatal(err)
	}
}

// applyObservation 模拟一次操作成功 + 其 apply 后绑定观测（门禁证据）。
func (f *depFixture) applyObservation(machine, opID string, gen int64, digest string, seq int64) {
	f.t.Helper()
	if _, err := f.rec.OnResult(f.ctx, machine, reconcile.OperationResultMsg{
		OperationID: opID, Phase: domain.OperationPhaseSucceeded,
		Verify: &domain.VerifyEvidence{
			DesiredProjectionDigest: digest, ObservedProjectionDigest: digest,
			CanonicalizationVersion: "fixture-projection-v1", InventorySeq: seq,
			AdapterHealth: domain.AdapterHealthPassed,
		},
	}); err != nil {
		f.t.Fatal(err)
	}
	payload, err := json.Marshal(domain.ObservedState{
		InventorySeq: seq, Full: true, OperationID: opID,
		CanonicalizationVersion:  "fixture-projection-v1",
		DesiredProjectionDigest:  digest,
		ObservedProjectionDigest: digest,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	rec := &domain.ObservedStateRecord{Machine: machine, InventorySeq: seq,
		OperationID: opID, Payload: payload}
	f.rec.AssignObservationGeneration(f.ctx, rec)
	if _, err := f.observed.Store(f.ctx, rec); err != nil {
		f.t.Fatal(err)
	}
}

func (f *depFixture) targets(name string) map[string]domain.DeploymentTargetStatus {
	f.t.Helper()
	ts, err := f.dep.targets.List(f.ctx, name)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]domain.DeploymentTargetStatus{}
	for _, t := range ts {
		out[t.Machine] = t
	}
	return out
}

func depSpec(machines []string, target, rollbackOf int64) json.RawMessage {
	b, err := json.Marshal(map[string]any{
		"machineNames": machines, "targetGeneration": target, "rollbackOf": rollbackOf,
	})
	if err != nil {
		panic(err)
	}
	return b
}

// T16 扩展：三台机器 gen1→gen2→gen3 后回滚到 gen1——每台各自物化新代、无目标
// Superseded、Deployment 终态 Succeeded（FR-10.4/10.6 v1.1.2 分支）。
func TestRollbackDeploymentMaterializesPerMachineGeneration(t *testing.T) {
	f := newDepFixture(t)
	for _, m := range []string{"ws-1", "ws-2", "ws-3"} {
		f.seedMachine(m)
	}
	f.setProfile(`{"agents":{"fixture":{"enabled":true,"version":"1.0.0"}}}`)
	for _, m := range []string{"ws-1", "ws-2", "ws-3"} {
		f.materialize(m) // gen1 = 内容 A
	}
	g1, err := f.snap.Get(f.ctx, "ws-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	f.updateProfile(`{"agents":{"fixture":{"enabled":true,"version":"2.0.0"}}}`)
	for _, m := range []string{"ws-1", "ws-2", "ws-3"} {
		f.materialize(m) // gen2 = 内容 B
	}
	f.updateProfile(`{"agents":{"fixture":{"enabled":true,"version":"3.0.0"}}}`)
	for _, m := range []string{"ws-1", "ws-2", "ws-3"} {
		f.materialize(m) // gen3 = 内容 C
	}

	// 回滚 Deployment：内容代 = gen1（rollbackOf 指向 gen3）。
	dep := &domain.Deployment{Metadata: domain.ObjectMeta{Name: "rb-1"},
		Spec: depSpec([]string{"ws-1", "ws-2", "ws-3"}, 1, 3)}
	if err := f.dep.Create(f.ctx, dep); err != nil {
		t.Fatalf("create rollback deployment: %v", err)
	}

	// 驱动到终态：每轮 Advance 按批次（默认 maxUnavailable=1）推进一台，
	// 随后节点完成操作并上报绑定观测（门禁证据），直至全部终结。
	completed := map[string]bool{}
	seq := int64(10)
	allSucceeded := false
	for round := 0; round < 12 && !allSucceeded; round++ {
		if err := f.dep.Advance(f.ctx, "rb-1"); err != nil {
			t.Fatal(err)
		}
		ts := f.targets("rb-1")
		if testing.Verbose() {
			t.Logf("round %d: %+v", round, ts)
		}
		allSucceeded = true
		for name, tg := range ts {
			switch tg.Phase {
			case domain.TargetPhaseSucceeded:
				continue
			case domain.TargetPhaseRunning:
				allSucceeded = false
				if !completed[name] {
					// 回滚目标的有效代：每台各自物化的新代（内容 = gen1 内容）。
					if tg.EffectiveGeneration != 4 {
						t.Fatalf("target %s effective gen = %d, want 4 (per-machine new generation)",
							name, tg.EffectiveGeneration)
					}
					g4, err := f.snap.Get(f.ctx, name, 4)
					if err != nil {
						t.Fatalf("gen4 missing for %s: %v", name, err)
					}
					if g4.Digest != g1.Digest {
						t.Fatalf("rollback content mismatch for %s", name)
					}
					seq++
					f.applyObservation(name, tg.OperationID, 4, g1.Digest, seq)
					completed[name] = true
				}
			default:
				// Pending/Blocked：批次（maxUnavailable）未轮到，合法等待。
				allSucceeded = false
			}
		}
	}
	ts := f.targets("rb-1")
	for name, tg := range ts {
		if tg.Phase != domain.TargetPhaseSucceeded {
			t.Fatalf("target %s = %s(%s), want Succeeded", name, tg.Phase, tg.Reason)
		}
	}
	d, err := f.dep.deploys.Get(f.ctx, "rb-1")
	if err != nil {
		t.Fatal(err)
	}
	st, err := parseStatus(d.StatusJSON())
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != domain.DeploymentPhaseSucceeded {
		t.Fatalf("deployment = %s(%s)", st.Phase, st.Reason)
	}

	// 反例：非回滚 Deployment 指向低于当前代的目标 → Superseded，不执行（FR-10.6）。
	old := &domain.Deployment{Metadata: domain.ObjectMeta{Name: "old-1"},
		Spec: depSpec([]string{"ws-1"}, 1, 0)}
	if err := f.dep.Create(f.ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := f.dep.Advance(f.ctx, "old-1"); err != nil {
		t.Fatal(err)
	}
	ts = f.targets("old-1")
	if ts["ws-1"].Phase != domain.TargetPhaseSuperseded ||
		ts["ws-1"].Reason != domain.SupersededByNewerGeneration {
		t.Fatalf("non-rollback target = %s(%s), want Superseded(SupersededByNewerGeneration)",
			ts["ws-1"].Phase, ts["ws-1"].Reason)
	}
}

// FR-10.7：目标机存在未决操作（含 Unknown）时目标阻塞，不计失败也不推进。
func TestDeploymentTargetBlockedByUnresolvedOperation(t *testing.T) {
	f := newDepFixture(t)
	f.seedMachine("ws-1")
	f.setProfile(`{"agents":{"fixture":{"enabled":true,"version":"1.0.0"}}}`)
	f.materialize("ws-1")

	// 先制造一个未决操作（派发后不回结果）。
	op, err := f.rec.Reconcile(f.ctx, "ws-1", reconcile.ReconcileRequest{})
	if err != nil {
		t.Fatal(err)
	}
	dep := &domain.Deployment{Metadata: domain.ObjectMeta{Name: "d-1"},
		Spec: depSpec([]string{"ws-1"}, 1, 0)}
	if err := f.dep.Create(f.ctx, dep); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := f.dep.Advance(f.ctx, "d-1"); err != nil {
			t.Fatal(err)
		}
	}
	ts := f.targets("d-1")
	if ts["ws-1"].Phase != domain.TargetPhaseBlocked {
		t.Fatalf("target = %s(%s), want Blocked (FR-10.7)", ts["ws-1"].Phase, ts["ws-1"].Reason)
	}
	// 显式跳过（留审计）→ Skipped。
	if err := f.dep.SkipTarget(f.ctx, "d-1", "ws-1", "operator decided"); err != nil {
		t.Fatal(err)
	}
	ts = f.targets("d-1")
	if ts["ws-1"].Phase != domain.TargetPhaseSkipped {
		t.Fatalf("after skip = %s", ts["ws-1"].Phase)
	}
	_ = op
}

// pauseOnFailure：任一目标失败立即暂停（FR-10.2）。
func TestDeploymentPausesOnFailure(t *testing.T) {
	f := newDepFixture(t)
	f.seedMachine("ws-1")
	f.seedMachine("ws-2")
	f.setProfile(`{"agents":{"fixture":{"enabled":true,"version":"1.0.0"}}}`)
	f.materialize("ws-1")
	f.materialize("ws-2")

	dep := &domain.Deployment{
		Metadata: domain.ObjectMeta{Name: "d-pause"},
		Spec:     depSpecFailFast([]string{"ws-1", "ws-2"}, 1),
	}
	if err := f.dep.Create(f.ctx, dep); err != nil {
		t.Fatal(err)
	}
	if err := f.dep.Advance(f.ctx, "d-pause"); err != nil {
		t.Fatal(err)
	}
	ts := f.targets("d-pause")
	if ts["ws-1"].Phase != domain.TargetPhaseRunning {
		t.Fatalf("first batch target should dispatch: %+v", ts)
	}
	// ws-1 的操作失败（门禁条件 1 不满足）。
	if _, err := f.rec.OnResult(f.ctx, "ws-1", reconcile.OperationResultMsg{
		OperationID: ts["ws-1"].OperationID, Phase: domain.OperationPhaseFailed,
		Reason: domain.ReasonHealthCheckFailed,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.dep.Advance(f.ctx, "d-pause"); err != nil {
		t.Fatal(err)
	}
	d, _ := f.dep.deploys.Get(f.ctx, "d-pause")
	st, err := parseStatus(d.StatusJSON())
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != domain.DeploymentPhasePaused {
		t.Fatalf("deployment = %s, want Paused on failure", st.Phase)
	}
}

func depSpecFailFast(machines []string, target int64) json.RawMessage {
	b, err := json.Marshal(map[string]any{
		"machineNames": machines, "targetGeneration": target,
		"strategy": map[string]any{"pauseOnFailure": true},
	})
	if err != nil {
		panic(err)
	}
	return b
}

// T5（单元）：门禁四条件的因果绑定——缓存/历史/周期上报均不被接受（§4.4）。
func TestGateCausalBinding(t *testing.T) {
	const machine = "ws-1"
	const digest = "sha256:ok"
	op := func(modifier string) *domain.Operation {
		o := &domain.Operation{
			Metadata: domain.ObjectMeta{Name: "op-1"},
			Spec:     domain.OperationSpec{Machine: machine, DesiredGeneration: 7},
			Status: domain.OperationStatus{
				Phase:            domain.OperationPhaseSucceeded,
				TerminalModifier: modifier,
				Verify: &domain.VerifyEvidence{
					DesiredProjectionDigest: digest, ObservedProjectionDigest: digest,
					CanonicalizationVersion: "v1", InventorySeq: 5, AdapterHealth: "passed",
				},
			},
		}
		return o
	}
	obs := func(opID, digestObs, version string) *BoundObservation {
		return &BoundObservation{OperationID: opID, InventorySeq: 5,
			ObservedProjectionDigest: digestObs, DesiredProjectionDigest: digest,
			CanonicalizationVersion: version, AdapterHealth: "passed"}
	}
	in := func(o *domain.Operation, b *BoundObservation) GateInput {
		return GateInput{MachineID: machine, TargetGeneration: 7, Op: o, PostApply: b}
	}

	// 正常路径通过。
	if r := EvaluateGate(in(op(""), obs("op-1", digest, "v1"))); !r.Passed {
		t.Fatalf("happy path blocked: %+v", r)
	}
	// 条件 1：Succeeded(Superseded) 不满足；机器无关操作不满足；代不符不满足。
	if r := EvaluateGate(in(op(domain.ModifierSuperseded), obs("op-1", digest, "v1"))); r.Passed || r.FailedCondition != 1 {
		t.Fatalf("superseded passed gate: %+v", r)
	}
	wrongMachine := op("")
	wrongMachine.Spec.Machine = "ws-2"
	if r := EvaluateGate(in(wrongMachine, obs("op-1", digest, "v1"))); r.Passed || r.FailedCondition != 1 {
		t.Fatalf("foreign machine op passed: %+v", r)
	}
	// 条件 2：周期 inventory（未绑定 operationId）不是证据。
	if r := EvaluateGate(in(op(""), obs("", digest, "v1"))); r.Passed || r.FailedCondition != 2 {
		t.Fatalf("periodic inventory accepted: %+v", r)
	}
	// 条件 3：status 缓存说一致、真实绑定观测说漂移 → 拒绝（A4 缓存放行反例）。
	if r := EvaluateGate(in(op(""), obs("op-1", "sha256:drifted", "v1"))); r.Passed || r.FailedCondition != 3 {
		t.Fatalf("stale cache accepted: %+v", r)
	}
	// 条件 3：跨规范化版本不可比。
	if r := EvaluateGate(in(op(""), obs("op-1", digest, "v2"))); r.Passed || r.FailedCondition != 3 {
		t.Fatalf("version mismatch accepted: %+v", r)
	}
	// 条件 3 的权威判据（§7.1）：操作证据与观测**互相一致**（交叉核对全过），
	// 但两侧摘要本身 desired != observed —— 那就是 drift，必须拒绝。只做交叉
	// 核对会把一台实际漂移的机器放行。
	driftEvidence := op("")
	driftEvidence.Status.Verify.ObservedProjectionDigest = "sha256:drifted"
	if r := EvaluateGate(in(driftEvidence, obs("op-1", "sha256:drifted", "v1"))); r.Passed || r.FailedCondition != 3 {
		t.Fatalf("drift (desired != observed) accepted by gate: %+v", r)
	}
	// 条件 4：健康失败不通过。
	unhealthy := op("")
	unhealthy.Status.Verify.AdapterHealth = "failed"
	if r := EvaluateGate(in(unhealthy, obs("op-1", digest, "v1"))); r.Passed || r.FailedCondition != 4 {
		t.Fatalf("failed health accepted: %+v", r)
	}
	// 条件 4（KM-24 补齐）：观测侧健康结果。
	//  (a) 观测未上报健康 → 退回该操作的 verify 证据；
	noHealthObs := obs("op-1", digest, "v1")
	noHealthObs.AdapterHealth = ""
	if r := EvaluateGate(in(op(""), noHealthObs)); !r.Passed {
		t.Fatalf("verify health must be the fallback when the observation carries none: %+v", r)
	}
	//  (b) 观测上报 passed 而 verify 未带健康 → 观测即证据（healthFromObservation）；
	obsOnly := obs("op-1", digest, "v1")
	opNoHealth := op("")
	opNoHealth.Status.Verify.AdapterHealth = ""
	if r := EvaluateGate(in(opNoHealth, obsOnly)); !r.Passed {
		t.Fatalf("observation-carried health must satisfy condition 4: %+v", r)
	}
	//  (c) 两侧冲突（观测 failed / verify passed）→ 取严，不通过。
	conflict := obs("op-1", digest, "v1")
	conflict.AdapterHealth = domain.AdapterHealthFailed
	if r := EvaluateGate(in(op(""), conflict)); r.Passed || r.FailedCondition != 4 {
		t.Fatalf("conflicting health evidence must fail closed: %+v", r)
	}
	// 无操作记录（如仅凭机器 status 历史 phase）不通过。
	if r := EvaluateGate(GateInput{MachineID: machine, TargetGeneration: 7}); r.Passed || r.FailedCondition != 1 {
		t.Fatalf("no-op evidence accepted: %+v", r)
	}
}

// KM-24 回归：门禁证据必须来自**与操作绑定**的观测，且不得被更高 seq 的
// 周期 inventory 覆盖（§4.4 条件 2）。修复前 observed_states 单行 UPSERT 会
// 让一份无 operationId 的周期报文在数秒内清空绑定，门禁必然以
// `gate condition 2 failed` 收尾。
func TestPeriodicInventoryDoesNotClobberGateBinding(t *testing.T) {
	f := newDepFixture(t)
	f.seedMachine("ws-1")
	f.setProfile(`{"agents":{"fixture":{"enabled":true,"version":"1.0.0"}}}`)
	f.materialize("ws-1")
	snap, err := f.snap.Get(f.ctx, "ws-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	const name = "dep-binding"
	dep := &domain.Deployment{Metadata: domain.ObjectMeta{Name: name},
		Spec: depSpec([]string{"ws-1"}, 1, 0)}
	if err := f.dep.Create(f.ctx, dep); err != nil {
		t.Fatal(err)
	}
	if err := f.dep.Advance(f.ctx, name); err != nil {
		t.Fatal(err)
	}
	tg := f.targets(name)["ws-1"]
	if tg.Phase != domain.TargetPhaseRunning || tg.OperationID == "" {
		t.Fatalf("target = %+v, want Running with an operation", tg)
	}
	f.applyObservation("ws-1", tg.OperationID, 1, snap.Digest, 5)

	// 周期 inventory（无 operationId）以更高 seq 覆盖"每机最新观测"主行。
	payload, err := json.Marshal(domain.ObservedState{
		InventorySeq: 99, Full: true,
		CanonicalizationVersion:  "fixture-projection-v1",
		DesiredProjectionDigest:  snap.Digest,
		ObservedProjectionDigest: snap.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &domain.ObservedStateRecord{Machine: "ws-1", InventorySeq: 99, Payload: payload}
	f.rec.AssignObservationGeneration(f.ctx, rec)
	if _, err := f.observed.Store(f.ctx, rec); err != nil {
		t.Fatal(err)
	}

	if err := f.dep.Advance(f.ctx, name); err != nil {
		t.Fatal(err)
	}
	got := f.targets(name)["ws-1"]
	if got.Phase != domain.TargetPhaseSucceeded {
		t.Fatalf("target = %s(%s), want Succeeded: periodic inventory must not clobber the bound observation",
			got.Phase, got.Reason)
	}
}
