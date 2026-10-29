package grpcagent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// KM-49 生命周期通道闭环（gRPC 真实栈）：创建+确认安装操作 → 编排经活跃流下发
// ExecuteLifecycleOperation（固定版本意图）→ 节点回逐目标结果 → 服务端逐目标
// 落库 → Ack（outbox 淘汰依据）。
func TestDispatchLifecycleOperationAndResultClosedLoop(t *testing.T) {
	h := newHarness(t, Config{OfflineAfter: time.Minute})
	ctx := context.Background()
	if err := h.machines.Create(ctx, &domain.Machine{
		Metadata: domain.ObjectMeta{Name: "lc-op"},
		Spec:     json.RawMessage(`{"managementMode":"agentd"}`),
	}); err != nil {
		t.Fatal(err)
	}
	ac := newAgentCert(t, "lc-op")
	resp, err := h.enroll("lc-op", h.enrollToken("lc-op", ac, time.Minute), ac)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	client, err := h.connectClient(resp.GetCertPem(), ac.priv)
	if err != nil {
		t.Fatal(err)
	}
	stream, _, err := h.connectStream(client, "lc-op", "0.2.0")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// 创建并确认操作（目标 OS/Arch 缺省 → 台账无观测时保持空；节点以运行时
	// 平台判定，通配阻塞不能绕过）。
	op, created, err := h.lc.CreateOperation(ctx, domain.LifecycleOperationSpec{
		Type:           domain.LifecycleOpInstall,
		IdempotencyKey: "lc-loop-1",
		Version:        "0.44.0",
		Targets: []domain.LifecycleTarget{{Machine: "lc-op", User: "alice",
			Agent: "codex", Method: "npm", Package: "@openai/codex"}},
	})
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if err := h.lc.Confirm(ctx, op.Metadata.Name, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.lc.ScanOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// 节点收到 ExecuteLifecycleOperation：固定版本、幂等键、超时齐备。
	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv execute: %v", err)
	}
	ex := msg.GetExecuteLifecycleOperation()
	if ex == nil {
		t.Fatalf("expected ExecuteLifecycleOperation, got %T", msg.GetPayload())
	}
	if ex.GetSpec().GetOpId() != op.Metadata.Name || ex.GetSpec().GetVersion() != "0.44.0" {
		t.Fatalf("spec mismatch: %+v", ex.GetSpec())
	}
	if ex.GetSpec().GetIdempotencyKey() != "lc-loop-1" {
		t.Fatalf("idempotency key not carried: %+v", ex.GetSpec())
	}
	if ex.GetDeadlineUnix() <= time.Now().Unix() {
		t.Fatalf("deadline not set: %d", ex.GetDeadlineUnix())
	}
	if n := len(ex.GetSpec().GetTargets()); n != 1 {
		t.Fatalf("targets = %d, want 1", n)
	}
	tgt := ex.GetSpec().GetTargets()[0]
	if tgt.GetUser() != "alice" || tgt.GetMethod() != "npm" || tgt.GetPackage() != "@openai/codex" {
		t.Fatalf("target mismatch: %+v", tgt)
	}

	// 节点回逐目标结果（成功 + 引擎核验证据）。
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_LifecycleOperationResult{
		LifecycleOperationResult: &fleetv1.LifecycleOperationResult{
			OpId:           op.Metadata.Name,
			IdempotencyKey: "lc-loop-1",
			Results: []*fleetv1.LifecycleTargetResult{{
				Target: tgt, State: domain.TargetSucceeded,
				Reason:  domain.ReasonVerifiedPresent,
				Message: "verified",
				Evidence: &fleetv1.LifecycleEvidence{
					ObservedVersion: "0.44.0", Method: "npm",
					ExecutablePath: "/usr/local/bin/codex",
					RecordedAtUnix: time.Now().Unix(),
				},
			}},
			FinishedAtUnix: time.Now().Unix(),
		},
	}}); err != nil {
		t.Fatal(err)
	}

	// 服务端 Ack（outbox 淘汰依据，§8.2 同纪律）。
	ack, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv ack: %v", err)
	}
	if ack.GetOperationResultAck().GetOperationId() != op.Metadata.Name {
		t.Fatalf("ack mismatch: %+v", ack)
	}

	// 终态：逐目标 succeeded + 证据落库，相位 Completed。
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := h.lcRepo.GetOperation(ctx, op.Metadata.Name)
		if err == nil && got.Status.Phase == domain.OperationPhaseCompleted {
			r := got.Status.Results[0]
			if r.State != domain.TargetSucceeded || r.Reason != domain.ReasonVerifiedPresent {
				t.Fatalf("result = %s/%s", r.State, r.Reason)
			}
			if r.Evidence == nil || r.Evidence.ObservedVersion != "0.44.0" {
				t.Fatalf("evidence not persisted: %+v", r.Evidence)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("lifecycle operation not completed in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
