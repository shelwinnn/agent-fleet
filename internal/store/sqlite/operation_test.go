package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

func newOp(machine string) *domain.Operation {
	return &domain.Operation{
		Spec: domain.OperationSpec{Machine: machine, Type: domain.OperationTypeReconcile, Transport: "agentd"},
	}
}

// 互斥谓词包含 AwaitingConfirmation 与 Unknown（§4.3 v1.1.2 P-1/T2 存储层）：
// 未决集合内任何相位都阻止第二个未决操作；终态释放。
func TestOperationMutexCoversAwaitingConfirmation(t *testing.T) {
	db := openTestDB(t)
	ops := NewOperationStore(db)
	ctx := context.Background()

	first := newOp("ws-1")
	if err := ops.Create(ctx, first); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Pending → AwaitingConfirmation（确认窗口内互斥仍持有，§6.4）。
	if err := ops.Transition(ctx, first.Metadata.Name, domain.OperationPhasePending,
		domain.OperationPhaseAwaitingConfirmation, nil); err != nil {
		t.Fatalf("to awaiting: %v", err)
	}
	if err := ops.Create(ctx, newOp("ws-1")); !errors.Is(err, domain.ErrMachineBusy) {
		t.Fatalf("create during AwaitingConfirmation = %v, want ErrMachineBusy", err)
	}
	// AwaitingConfirmation → CancelRequested 仍占用。
	if err := ops.Transition(ctx, first.Metadata.Name, domain.OperationPhaseAwaitingConfirmation,
		domain.OperationPhaseCancelRequested, nil); err != nil {
		t.Fatalf("to cancel requested: %v", err)
	}
	if err := ops.Create(ctx, newOp("ws-1")); !errors.Is(err, domain.ErrMachineBusy) {
		t.Fatalf("create during CancelRequested = %v, want ErrMachineBusy", err)
	}
	// 终态（含修饰）释放互斥。
	err := ops.Transition(ctx, first.Metadata.Name, "", domain.OperationPhaseFailed,
		func(o *domain.Operation) error {
			o.Status.TerminalModifier = domain.ModifierCancelled
			return nil
		})
	if err != nil {
		t.Fatalf("terminal: %v", err)
	}
	second := newOp("ws-1")
	if err := ops.Create(ctx, second); err != nil {
		t.Fatalf("create after terminal: %v", err)
	}
	// CAS 迁移：from 不匹配 → ErrOpState（§6.4 状态机纪律）。
	if err := ops.Transition(ctx, second.Metadata.Name, domain.OperationPhaseRunning,
		domain.OperationPhaseSucceeded, nil); !errors.Is(err, domain.ErrOpState) {
		t.Fatalf("bad CAS err = %v, want ErrOpState", err)
	}
}

// 计划超时与 verify 证据的持久化（§6.4/FR-15.5）。
func TestOperationStatusColumnsRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ops := NewOperationStore(db)
	ctx := context.Background()

	op := newOp("ws-1")
	op.Spec.PlanDigest = "sha256:plan"
	op.Spec.DesiredGeneration = 7
	expires := time.Now().UTC().Add(30 * time.Minute)
	if err := ops.Create(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err := ops.Transition(ctx, op.Metadata.Name, domain.OperationPhasePending,
		domain.OperationPhaseAwaitingConfirmation, func(o *domain.Operation) error {
			o.Status.PlanExpiresAt = &expires
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	got, err := ops.Get(ctx, op.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.PlanDigest != "sha256:plan" || got.Spec.DesiredGeneration != 7 {
		t.Fatalf("spec columns lost: %+v", got.Spec)
	}
	if got.Status.Phase != domain.OperationPhaseAwaitingConfirmation || got.Status.PlanExpiresAt == nil {
		t.Fatalf("status columns lost: %+v", got.Status)
	}
	verify := &domain.VerifyEvidence{
		DesiredProjectionDigest: "sha256:d", ObservedProjectionDigest: "sha256:d",
		CanonicalizationVersion: "v1", InventorySeq: 42, AdapterHealth: "passed",
	}
	if err := ops.Transition(ctx, op.Metadata.Name, "", domain.OperationPhaseSucceeded,
		func(o *domain.Operation) error {
			o.Status.Verify = verify
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	got, err = ops.Get(ctx, op.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.Verify == nil || got.Status.Verify.InventorySeq != 42 ||
		got.Status.Verify.ObservedProjectionDigest != "sha256:d" {
		t.Fatalf("verify evidence lost: %+v", got.Status.Verify)
	}
	// ListUnresolved：终态不出现。
	un, err := ops.ListUnresolved(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(un) != 0 {
		t.Fatalf("terminal op listed unresolved: %d", len(un))
	}
}
