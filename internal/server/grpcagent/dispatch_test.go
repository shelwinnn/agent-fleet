package grpcagent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/controller/reconcile"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// §9.2 daemon 闭环（gRPC 真实栈）：reconcile → ExecuteOperation 携带完整快照下发
// → 节点回 Started/Result（verify 证据）+ apply 后绑定观测 → 条件按当前代置位 →
// 互斥释放。
func TestDispatchExecuteOperationAndResultClosedLoop(t *testing.T) {
	h := newHarness(t, Config{OfflineAfter: time.Minute})
	ctx := context.Background()
	if err := h.machines.Create(ctx, &domain.Machine{
		Metadata: domain.ObjectMeta{Name: "ws-op"},
		Spec:     json.RawMessage(`{"managementMode":"agentd","profileRef":"default"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.profiles.Create(ctx, &domain.AgentProfile{
		Metadata: domain.ObjectMeta{Name: "default"},
		Spec:     json.RawMessage(`{"agents":{"fixture":{"enabled":true,"version":"1.0.0"}}}`),
	}); err != nil {
		t.Fatal(err)
	}
	ac := newAgentCert(t, "ws-op")
	resp, err := h.enroll("ws-op", h.enrollToken("ws-op", ac, time.Minute), ac)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	client, err := h.connectClient(resp.GetCertPem(), ac.priv)
	if err != nil {
		t.Fatal(err)
	}
	stream, _, err := h.connectStream(client, "ws-op", "0.2.0")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// 周期上报让 AgentConnected/InventoryReady 就绪（派发不依赖它，但闭环语义完整）。
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_ObservedState{
		ObservedState: obsFor(1, "ws-op-host"),
	}}); err != nil {
		t.Fatal(err)
	}

	// 服务端触发 reconcile：经活跃流下发 ExecuteOperation（完整快照，FR-13.6）。
	op, err := h.rec.Reconcile(ctx, "ws-op", reconcile.ReconcileRequest{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := h.snapshots.Current(ctx, "ws-op"); err != nil {
		t.Fatalf("snapshot not materialized: %v", err)
	}

	// 节点收到 ExecuteOperation。
	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv execute: %v", err)
	}
	ex := msg.GetExecuteOperation()
	if ex == nil {
		t.Fatalf("expected ExecuteOperation, got %T", msg.GetPayload())
	}
	if ex.GetOperationId() != op.Metadata.Name || ex.GetDesiredGeneration() != op.Spec.DesiredGeneration {
		t.Fatalf("execute mismatch: %+v", ex)
	}
	if len(ex.GetSnapshot().GetSnapshotJson()) == 0 || ex.GetSnapshot().GetDigest() == "" {
		t.Fatal("ExecuteOperation must carry the full self-contained snapshot (FR-13.6)")
	}

	// 节点：Started → 伪造执行 → 终态（verify 证据）+ apply 后绑定观测。
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_OperationStarted{
		OperationStarted: &fleetv1.OperationStarted{OperationId: op.Metadata.Name},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_OperationResult{
		OperationResult: &fleetv1.OperationResult{
			OperationId: op.Metadata.Name, Phase: domain.OperationPhaseSucceeded,
			DesiredGeneration: op.Spec.DesiredGeneration,
			Verify: &fleetv1.VerifyEvidence{
				DesiredProjectionDigest: "sha256:loop", ObservedProjectionDigest: "sha256:loop",
				CanonicalizationVersion: "fixture-projection-v1", InventorySeq: 7,
				AdapterHealth: domain.AdapterHealthPassed,
			},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	// 服务端 Ack（outbox 淘汰依据）。
	ack, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv ack: %v", err)
	}
	if ack.GetOperationResultAck().GetOperationId() != op.Metadata.Name {
		t.Fatalf("ack mismatch: %+v", ack)
	}
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_ObservedState{
		ObservedState: &fleetv1.ObservedState{
			InventorySeq: 7, Full: true, OperationId: op.Metadata.Name,
			CanonicalizationVersion:  "fixture-projection-v1",
			DesiredProjectionDigest:  "sha256:loop",
			ObservedProjectionDigest: "sha256:loop",
			Machine:                  &fleetv1.MachineInfo{Os: "linux", Arch: "amd64", Hostname: "ws-op-host"},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	// 终态：操作 Succeeded、verify 证据落库、互斥释放、条件按当前代置位。
	var terminal *domain.Operation
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := h.ops.Get(ctx, op.Metadata.Name)
		if err == nil && domain.IsTerminal(got.Status.Phase) {
			if got.Status.Phase != domain.OperationPhaseSucceeded {
				t.Fatalf("terminal = %s(%s)", got.Status.Phase, got.Status.TerminalModifier)
			}
			terminal = got
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("operation not terminal in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// FR-15.5/§4.4：随结果上报的 verify 证据必须落库。缺了它，Reconciled 永远
	// 不会置 True、门禁也没有证据可评估（本用例扮演节点；节点侧把 evidence 放进
	// proto 的转换由 cmd/agent-fleet-agentd 的用例覆盖）。
	if terminal.Status.Verify == nil {
		t.Fatal("verify evidence not persisted on the operation (FR-15.5)")
	}
	if terminal.Status.Verify.InventorySeq != 7 ||
		terminal.Status.Verify.DesiredProjectionDigest != "sha256:loop" ||
		terminal.Status.Verify.AdapterHealth != domain.AdapterHealthPassed {
		t.Fatalf("verify evidence mismatch: %+v", terminal.Status.Verify)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		st := h.machineStatus("ws-op")
		if c, ok := st.GetCondition(domain.ConditionReconciled); ok && c.Status == domain.ConditionTrue {
			h.mustCondition(st, domain.ConditionDrifted, domain.ConditionFalse, "")
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Reconciled=True not reached: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 互斥已释放：可以再次派发。
	if _, err := h.rec.Reconcile(ctx, "ws-op", reconcile.ReconcileRequest{}); err != nil {
		t.Fatalf("mutex not released after closed loop: %v", err)
	}
}
