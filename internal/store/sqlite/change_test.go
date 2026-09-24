package sqlite

import (
	"context"
	"sync"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// changeRecorder 收集 ChangeSink 通知（KM-25：SSE 事件源的存储层测试）。
type changeRecorder struct {
	mu     sync.Mutex
	events []Event
}

// Event 是测试用的通知快照（与 sse.Event 同形，避免库层测试 import 服务层）。
type Event struct {
	Resource string
	ID       string
	Revision int64
}

func (r *changeRecorder) sink(resource, id string, revision int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, Event{Resource: resource, ID: id, Revision: revision})
}

func (r *changeRecorder) take() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func wantEvent(t *testing.T, got []Event, resource, id string, revision int64) {
	t.Helper()
	for _, ev := range got {
		if ev.Resource == resource && ev.ID == id && ev.Revision == revision {
			return
		}
	}
	t.Fatalf("events = %+v, want %s/%s@%d", got, resource, id, revision)
}

func TestMachineWritesEmitChangeEvents(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	rec := &changeRecorder{}
	db.SetChangeSink(rec.sink)
	machines := NewMachineStore(db)
	status := NewMachineStatusStore(db)

	m := &domain.Machine{Spec: []byte(`{"managementMode":"agentd"}`)}
	m.Metadata.Name = "ws-1"
	if err := machines.Create(ctx, m); err != nil {
		t.Fatalf("create machine: %v", err)
	}
	wantEvent(t, rec.take(), resourceMachines, "ws-1", 1)

	// 控制器写 status（观测上报 / conditions）也必须产生事件：UI 的新鲜度与
	// 未决操作展示依赖它（FR-1.9/FR-1.10）。
	if err := status.UpdateStatus(ctx, "ws-1", func(st *domain.MachineStatus) error {
		st.ObservedGeneration = 3
		return nil
	}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	wantEvent(t, rec.take(), resourceMachines, "ws-1", 2)

	if err := machines.Delete(ctx, "ws-1"); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	// 删除事件的版本 = 删除前 rv+1（该资源最后一次可见变更的版本）。
	wantEvent(t, rec.take(), resourceMachines, "ws-1", 3)

	// 删除不存在的资源：不产生事件（也没有 404 之外的副作用）。
	if err := machines.Delete(ctx, "ws-1"); err == nil {
		t.Fatal("delete missing machine: want error")
	}
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("events after failed delete = %+v, want none", got)
	}
}

func TestResourceWritesEmitChangeEvents(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	rec := &changeRecorder{}
	db.SetChangeSink(rec.sink)
	profiles := NewProfileStore(db)

	p := &domain.AgentProfile{Spec: []byte(`{"agents":{}}`)}
	p.Metadata.Name = "default-dev"
	if err := profiles.Create(ctx, p); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	wantEvent(t, rec.take(), "profiles", "default-dev", 1)

	upd, err := profiles.Get(ctx, "default-dev")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	upd.SetSpecJSON([]byte(`{"agents":{"codex":{}}}`))
	if err := profiles.Update(ctx, upd); err != nil {
		t.Fatalf("update profile: %v", err)
	}
	wantEvent(t, rec.take(), "profiles", "default-dev", 2)

	if err := profiles.Delete(ctx, "default-dev"); err != nil {
		t.Fatalf("delete profile: %v", err)
	}
	wantEvent(t, rec.take(), "profiles", "default-dev", 3)
}

func TestOperationWritesEmitChangeEvents(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	rec := &changeRecorder{}
	db.SetChangeSink(rec.sink)
	ops := NewOperationStore(db)

	op := newOp("ws-1")
	if err := ops.Create(ctx, op); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	wantEvent(t, rec.take(), resourceOperations, op.Metadata.Name, 1)

	if err := ops.Transition(ctx, op.Metadata.Name, domain.OperationPhasePending,
		domain.OperationPhaseRunning, nil); err != nil {
		t.Fatalf("transition: %v", err)
	}
	wantEvent(t, rec.take(), resourceOperations, op.Metadata.Name, 2)
}

// 目标推进必须让 deployments 资源产生事件：目标 phase/reason 是 UI 呈现
// Superseded/Skipped 的数据来源（FR-14.5），而它写在 deployment_targets 表。
func TestDeploymentTargetWriteEmitsDeploymentEvent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	rec := &changeRecorder{}
	db.SetChangeSink(rec.sink)
	deploys := NewDeploymentStore(db)
	targets := NewDeploymentTargetStore(db)

	d := &domain.Deployment{Spec: []byte(`{"targetGeneration":1}`)}
	d.Metadata.Name = "rollout-1"
	if err := deploys.Create(ctx, d); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	rec.take() // 创建事件

	if err := targets.Replace(ctx, "rollout-1", []domain.DeploymentTargetStatus{
		{Machine: "ws-1", Phase: domain.TargetPhasePending},
	}); err != nil {
		t.Fatalf("replace targets: %v", err)
	}
	wantEvent(t, rec.take(), resourceDeployments, "rollout-1", 2)

	if err := targets.Update(ctx, "rollout-1", domain.DeploymentTargetStatus{
		Machine: "ws-1", Phase: domain.TargetPhaseSuperseded, Reason: domain.SupersededByNewerGeneration,
	}); err != nil {
		t.Fatalf("update target: %v", err)
	}
	wantEvent(t, rec.take(), resourceDeployments, "rollout-1", 3)

	// 版本号随目标推进单调递增（UI 据此判定"是否比已渲染的更新"）。
	got, err := deploys.Get(ctx, "rollout-1")
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got.Metadata.ResourceVersion != 3 {
		t.Fatalf("deployment resourceVersion = %d, want 3", got.Metadata.ResourceVersion)
	}
}

// 未注入 sink 时写路径照常工作（库层不依赖 SSE）。
func TestWritesWithoutSink(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	machines := NewMachineStore(db)
	m := &domain.Machine{Spec: []byte(`{}`)}
	m.Metadata.Name = "ws-1"
	if err := machines.Create(ctx, m); err != nil {
		t.Fatalf("create without sink: %v", err)
	}
}
