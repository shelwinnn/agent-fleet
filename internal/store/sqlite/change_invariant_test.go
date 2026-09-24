package sqlite

import (
	"context"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// TestEventRevisionMatchesPersistedResourceVersion 锁定 SSE 事件的核心不变量
// （KM-25 核查必改 M1）：**事件携带的 revision 必须等于提交后落库的
// resourceVersion**。前端按 `revision <= 已见值` 判重，若事件版本比库里的"提前"
// 一版，紧随其后的真实变更就会被当成重复事件丢掉、不触发重取。
func TestEventRevisionMatchesPersistedResourceVersion(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	rec := &changeRecorder{}
	db.SetChangeSink(rec.sink)
	machines := NewMachineStore(db)
	status := NewMachineStatusStore(db)
	profiles := NewProfileStore(db)
	ops := NewOperationStore(db)
	deploys := NewDeploymentStore(db)
	targets := NewDeploymentTargetStore(db)

	assertMatches := func(label string, events []Event, resource, id string, persisted int64) {
		t.Helper()
		if len(events) != 1 {
			t.Fatalf("%s: events = %+v, want 恰好 1 条", label, events)
		}
		if events[0].Revision != persisted {
			t.Fatalf("%s: 事件 revision=%d，落库 resourceVersion=%d（必须相等）",
				label, events[0].Revision, persisted)
		}
		if events[0].Resource != resource || events[0].ID != id {
			t.Fatalf("%s: 事件 = %+v, want %s/%s", label, events[0], resource, id)
		}
	}

	// --- machines：创建 / 状态写 / spec 更新 ---
	m := &domain.Machine{Spec: []byte(`{"managementMode":"agentd"}`)}
	m.Metadata.Name = "ws-1"
	if err := machines.Create(ctx, m); err != nil {
		t.Fatalf("create machine: %v", err)
	}
	got, err := machines.Get(ctx, "ws-1")
	if err != nil {
		t.Fatalf("get machine: %v", err)
	}
	assertMatches("machine.Create", rec.take(), resourceMachines, "ws-1", got.Metadata.ResourceVersion)

	if err := status.UpdateStatus(ctx, "ws-1", func(st *domain.MachineStatus) error {
		st.InventorySeq = 3
		return nil
	}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _ = machines.Get(ctx, "ws-1")
	assertMatches("machine.UpdateStatus", rec.take(), resourceMachines, "ws-1", got.Metadata.ResourceVersion)

	got.Spec = []byte(`{"managementMode":"agentd","profileRef":"default"}`)
	if err := machines.Update(ctx, got); err != nil {
		t.Fatalf("update machine: %v", err)
	}
	got, _ = machines.Get(ctx, "ws-1")
	assertMatches("machine.Update", rec.take(), resourceMachines, "ws-1", got.Metadata.ResourceVersion)

	// --- profiles：创建 / 更新 ---
	p := &domain.AgentProfile{Spec: []byte(`{}`)}
	p.Metadata.Name = "default"
	if err := profiles.Create(ctx, p); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	stored, _ := profiles.Get(ctx, "default")
	assertMatches("profile.Create", rec.take(), "profiles", "default", stored.Metadata.ResourceVersion)
	stored.SetSpecJSON([]byte(`{"agents":{}}`))
	if err := profiles.Update(ctx, stored); err != nil {
		t.Fatalf("update profile: %v", err)
	}
	stored, _ = profiles.Get(ctx, "default")
	assertMatches("profile.Update", rec.take(), "profiles", "default", stored.Metadata.ResourceVersion)

	// --- operations：创建 / 步骤替换 / 相位迁移（M1 的回归点） ---
	op := newOp("ws-1")
	if err := ops.Create(ctx, op); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	persisted, _ := ops.Get(ctx, op.Metadata.Name)
	assertMatches("operation.Create", rec.take(), resourceOperations, op.Metadata.Name, persisted.Metadata.ResourceVersion)

	if err := ops.ReplaceSteps(ctx, op.Metadata.Name, []domain.OperationStepRecord{
		{Seq: 1, Name: "plan", Phase: "Running"},
		{Seq: 2, Name: "apply", Phase: "Running"},
	}); err != nil {
		t.Fatalf("replace steps: %v", err)
	}
	persisted, _ = ops.Get(ctx, op.Metadata.Name)
	assertMatches("operation.ReplaceSteps", rec.take(), resourceOperations, op.Metadata.Name, persisted.Metadata.ResourceVersion)

	// 步骤替换后**下一次真实相位迁移**必须仍然产生"更高"的版本（M1 的后果：
	// 此前事件提前用掉了 rv+1，导致这次迁移被前端判重丢弃）。
	if err := ops.Transition(ctx, op.Metadata.Name, domain.OperationPhasePending,
		domain.OperationPhaseRunning, nil); err != nil {
		t.Fatalf("transition: %v", err)
	}
	after, _ := ops.Get(ctx, op.Metadata.Name)
	assertMatches("operation.Transition(after ReplaceSteps)", rec.take(),
		resourceOperations, op.Metadata.Name, after.Metadata.ResourceVersion)
	if after.Metadata.ResourceVersion <= persisted.Metadata.ResourceVersion {
		t.Fatalf("迁移后的版本 %d 未超过步骤替换后的版本 %d",
			after.Metadata.ResourceVersion, persisted.Metadata.ResourceVersion)
	}

	// --- deployments：创建 / 目标行写入 ---
	d := &domain.Deployment{Spec: []byte(`{"machineNames":["ws-1"],"targetGeneration":1}`)}
	d.Metadata.Name = "rollout-1"
	if err := deploys.Create(ctx, d); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	storedDep, _ := deploys.Get(ctx, "rollout-1")
	assertMatches("deployment.Create", rec.take(), resourceDeployments, "rollout-1", storedDep.Metadata.ResourceVersion)

	if err := targets.Replace(ctx, "rollout-1", []domain.DeploymentTargetStatus{
		{Machine: "ws-1", Phase: domain.TargetPhasePending},
	}); err != nil {
		t.Fatalf("replace targets: %v", err)
	}
	storedDep, _ = deploys.Get(ctx, "rollout-1")
	assertMatches("targets.Replace", rec.take(), resourceDeployments, "rollout-1", storedDep.Metadata.ResourceVersion)

	if err := targets.Update(ctx, "rollout-1", domain.DeploymentTargetStatus{
		Machine: "ws-1", Phase: domain.TargetPhaseFailed, Reason: "AgentDisconnected",
	}); err != nil {
		t.Fatalf("update target: %v", err)
	}
	storedDep, _ = deploys.Get(ctx, "rollout-1")
	assertMatches("targets.Update", rec.take(), resourceDeployments, "rollout-1", storedDep.Metadata.ResourceVersion)
}

// TestTargetUpdateWithoutChangeIsNoop 锁定 S1：控制器每轮扫描重写同一条
// (phase, reason) 时不得写库/发事件——否则被阻塞的目标会每 2 秒制造一条 SSE
// 事件和一次 UI 重取。
func TestTargetUpdateWithoutChangeIsNoop(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	rec := &changeRecorder{}
	db.SetChangeSink(rec.sink)
	deploys := NewDeploymentStore(db)
	targets := NewDeploymentTargetStore(db)

	d := &domain.Deployment{Spec: []byte(`{"machineNames":["ws-1"],"targetGeneration":1}`)}
	d.Metadata.Name = "rollout-1"
	if err := deploys.Create(ctx, d); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	blocked := domain.DeploymentTargetStatus{
		Machine: "ws-1", Phase: domain.TargetPhaseBlocked, Reason: domain.BlockedByNodeLock,
	}
	// 目标行由物化路径（Replace）建立，此后控制器每轮只会 Update 它。
	if err := targets.Replace(ctx, "rollout-1", []domain.DeploymentTargetStatus{blocked}); err != nil {
		t.Fatalf("materialize targets: %v", err)
	}
	rec.take() // 首次写入的事件
	before, _ := deploys.Get(ctx, "rollout-1")

	for i := 0; i < 3; i++ {
		if err := targets.Update(ctx, "rollout-1", blocked); err != nil {
			t.Fatalf("repeat update %d: %v", i, err)
		}
	}
	if events := rec.take(); len(events) != 0 {
		t.Fatalf("重复写入同一状态产生了事件：%+v", events)
	}
	after, _ := deploys.Get(ctx, "rollout-1")
	if after.Metadata.ResourceVersion != before.Metadata.ResourceVersion {
		t.Fatalf("重复写入同一状态 bump 了版本：%d → %d",
			before.Metadata.ResourceVersion, after.Metadata.ResourceVersion)
	}

	// 真正变化时仍必须写库 + 发事件（版本等于落库值）。
	if err := targets.Update(ctx, "rollout-1", domain.DeploymentTargetStatus{
		Machine: "ws-1", Phase: domain.TargetPhaseFailed, Reason: "AgentDisconnected",
	}); err != nil {
		t.Fatalf("changed update: %v", err)
	}
	events := rec.take()
	after, _ = deploys.Get(ctx, "rollout-1")
	if len(events) != 1 || events[0].Revision != after.Metadata.ResourceVersion {
		t.Fatalf("变化后的写入事件 = %+v，落库 rv = %d", events, after.Metadata.ResourceVersion)
	}
}
