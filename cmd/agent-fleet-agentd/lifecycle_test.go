package main

import (
	"context"
	"runtime"
	"testing"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/lifecycle"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// countingEngine 记录执行次数（宿主层幂等语义的探针）。
type countingEngine struct {
	calls int
}

func (e *countingEngine) Detect(_ context.Context, _ lifecycle.Request) (lifecycle.Detected, error) {
	e.calls++
	return lifecycle.Detected{Installed: false}, nil
}
func (e *countingEngine) InstallExact(_ context.Context, req lifecycle.Request) (lifecycle.Outcome, error) {
	e.calls++
	return lifecycle.Outcome{Op: lifecycle.OpInstall, VerifiedVersion: req.Version}, nil
}
func (e *countingEngine) UpgradeToExact(_ context.Context, req lifecycle.Request) (lifecycle.Outcome, error) {
	e.calls++
	return lifecycle.Outcome{Op: lifecycle.OpUpgrade, VerifiedVersion: req.Version}, nil
}
func (e *countingEngine) Uninstall(_ context.Context, _ lifecycle.UninstallRequest) (lifecycle.Outcome, error) {
	e.calls++
	return lifecycle.Outcome{Op: lifecycle.OpUninstall}, nil
}

func lifecycleHostSetup(t *testing.T) (*lifecycleExecutor, chan *fleetv1.LifecycleOperationResult, *countingEngine, context.CancelFunc) {
	t.Helper()
	eng := &countingEngine{}
	node := lifecycle.NewNodeExecutor(lifecycle.NodeExecutorConfig{
		Engines:     map[string]lifecycle.Engine{"npm": eng},
		Policy:      allowAllPolicy{},
		Timeout:     5 * time.Second,
		RuntimeUser: func() string { return "alice" },
	})
	x := newLifecycleExecutor("ws-1", t.TempDir(), node, quietLogger())
	msgs := make(chan *fleetv1.LifecycleOperationResult, 8)
	ctx, cancel := context.WithCancel(context.Background())
	go x.RunWorker(ctx, msgs)
	return x, msgs, eng, cancel
}

type allowAllPolicy struct{}

func (allowAllPolicy) Check(string, string, string) error { return nil }

// execLifecycleMsg 构造一条安装指令（目标平台与运行时一致，过执行闸）。
func execLifecycleMsg(opID string) *fleetv1.ExecuteLifecycleOperation {
	return &fleetv1.ExecuteLifecycleOperation{
		Spec: &fleetv1.LifecycleOperationSpec{
			OpId: opID, Type: domain.LifecycleOpInstall, IdempotencyKey: "k-" + opID,
			Version: "0.44.0",
			Targets: []*fleetv1.LifecycleTarget{{
				MachineId: "ws-1", User: "alice", Agent: "codex",
				Method: "npm", Package: "@openai/codex",
				Os: runtime.GOOS, Arch: runtime.GOARCH,
			}},
		},
		DeadlineUnix: time.Now().Add(time.Minute).Unix(),
	}
}

// TestLifecycleReportSatisfiesControlPlaneContract：worker 产出的上行消息必须
// 携带控制面对位落库所需的全部身份（KM-51 链路缺陷修复的回归锁）。此前的
// 转换不带幂等键与逐目标身份，真实链路上控制面按"键不匹配/未知目标"拒收
// 每一份结果——单元测试各自为战测不出来，E2E（internal/e2e）才暴露。
func TestLifecycleReportSatisfiesControlPlaneContract(t *testing.T) {
	x, msgs, _, cancel := lifecycleHostSetup(t)
	defer cancel()

	msg := execLifecycleMsg("op-contract")
	if res := x.Enqueue(msg); res != nil {
		t.Fatalf("unexpected replay: %+v", res)
	}
	var wire *fleetv1.LifecycleOperationResult
	select {
	case wire = <-msgs:
	case <-time.After(5 * time.Second):
		t.Fatal("no result from worker")
	}

	if got := wire.GetIdempotencyKey(); got != msg.GetSpec().GetIdempotencyKey() {
		t.Fatalf("uplink idempotency key = %q, want %q (control plane rejects results on key mismatch)", got, msg.GetSpec().GetIdempotencyKey())
	}
	if n := len(wire.GetResults()); n != 1 {
		t.Fatalf("results = %d, want 1", n)
	}
	tgt := wire.GetResults()[0].GetTarget()
	if tgt == nil {
		t.Fatal("uplink target identity missing (control plane files results by target key)")
	}

	// 按服务端 lifecycleResultFromProto 的读取方式还原目标身份，键必须与
	// 派发时的目标键逐段一致（对位落库的唯一依据）。
	served := domain.LifecycleTarget{
		Machine: tgt.GetMachineId(), Env: tgt.GetEnv(), User: tgt.GetUser(),
		Agent: tgt.GetAgent(), Method: tgt.GetMethod(), Package: tgt.GetPackage(),
	}
	want := domain.LifecycleTarget{Machine: "ws-1", Env: "native", User: "alice",
		Agent: "codex", Method: "npm", Package: "@openai/codex"}.Key()
	if got := served.Normalize().Key(); got != want {
		t.Fatalf("served target key = %q, want %q", got, want)
	}
	if wire.GetIdempotencyKey() != msg.GetSpec().GetIdempotencyKey() {
		t.Fatalf("served idempotency key = %q", wire.GetIdempotencyKey())
	}
}

// TestLifecycleHostQueueDedupeAndOutboxReplay：重复派发幂等——执行中/已入队
// 忽略、已终结从 outbox 重发；Ack 后淘汰。
func TestLifecycleHostQueueDedupeAndOutboxReplay(t *testing.T) {
	x, msgs, eng, cancel := lifecycleHostSetup(t)
	defer cancel()

	if res := x.Enqueue(execLifecycleMsg("op-a")); res != nil {
		t.Fatalf("first enqueue replayed: %+v", res)
	}
	// 重复派发（未执行完/已入队）→ 忽略。
	if res := x.Enqueue(execLifecycleMsg("op-a")); res != nil {
		t.Fatalf("duplicate enqueue should be ignored, got %+v", res)
	}
	select {
	case res := <-msgs:
		if res.GetOpId() != "op-a" || len(res.GetResults()) != 1 ||
			res.GetResults()[0].GetState() != domain.TargetSucceeded {
			t.Fatalf("worker result mismatch: %+v", res)
		}
		// 身份逐段回显（KM-49 修复波/T7 断链修复）：控制面按目标身份键对位，
		// 缺 Target 的结果会被按"未知目标"丢弃。
		tgt := res.GetResults()[0].GetTarget()
		if tgt.GetMachineId() != "ws-1" || tgt.GetUser() != "alice" ||
			tgt.GetMethod() != "npm" || tgt.GetPackage() != "@openai/codex" {
			t.Fatalf("target identity not echoed: %+v", tgt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no result from worker")
	}
	// 已终结（outbox 命中）→ 重发终态，引擎不重复执行。
	if res := x.Enqueue(execLifecycleMsg("op-a")); res == nil || res.GetOpId() != "op-a" {
		t.Fatalf("terminal dispatch must replay from outbox: %+v", res)
	}
	if eng.calls != 1 {
		t.Fatalf("engine calls = %d, want 1 (dedupe must not re-execute)", eng.calls)
	}
	// Ack 淘汰后重复派发会真正重执行（控制面重复派发已终结操作属异常，
	// 引擎幂等语义兜底——这里只锁 Ack 行为）。
	if !x.Forget("op-a") {
		t.Fatal("ack must evict outbox entry")
	}
	if x.Forget("op-a") {
		t.Fatal("second ack must not hit")
	}
}

// TestLifecycleHostOutboxSurvivesRestart：已终结未 Ack 的结果必须落盘并在重启
// 后可重发（与配置 outbox 同一可靠性纪律）。
func TestLifecycleHostOutboxSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	eng := &countingEngine{}
	newHost := func() *lifecycleExecutor {
		node := lifecycle.NewNodeExecutor(lifecycle.NodeExecutorConfig{
			Engines:     map[string]lifecycle.Engine{"npm": eng},
			Policy:      allowAllPolicy{},
			Timeout:     5 * time.Second,
			RuntimeUser: func() string { return "alice" },
		})
		return newLifecycleExecutor("ws-1", dir, node, quietLogger())
	}
	x := newHost()
	msgs := make(chan *fleetv1.LifecycleOperationResult, 8)
	ctx, cancel := context.WithCancel(context.Background())
	go x.RunWorker(ctx, msgs)
	if res := x.Enqueue(execLifecycleMsg("op-p")); res != nil {
		t.Fatalf("unexpected replay: %+v", res)
	}
	select {
	case <-msgs:
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
	}
	cancel()

	// "重启"：新宿主从磁盘恢复 outbox。
	reborn := newHost()
	pending := reborn.PendingResults()
	if len(pending) != 1 || pending[0].GetOpId() != "op-p" {
		t.Fatalf("outbox not recovered across restart: %+v", pending)
	}
}
