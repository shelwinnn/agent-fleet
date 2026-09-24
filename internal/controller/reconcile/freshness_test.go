package reconcile

// KM-26 核查回归（必改 2）：Unknown(StaleObservation) 必须"粘住"。
// 核查复现：ScanFreshness 置上 Unknown(StaleObservation) 后，任何一次
// EvaluateDrift（例如 GET /machines/{id}/drift 的落库路径）都会把它改写成
// Drifted=False——超窗观测等于又给出了"一致"的确定判决（§6.2 契约）。

import (
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// staleFixture 造一台"观测与期望不一致"的机器：先按新鲜观测求值一次（写出
// Drifted=True 的确定判决），再把 lastInventoryAt 拨旧 age（age=0 表示不拨旧），
// 模拟"时间流逝后观测超窗"的真实顺序。
func staleFixture(t *testing.T, age time.Duration, unresolved bool) *fixture {
	t.Helper()
	f := newFixture(t)
	f.seedMachine("ws-1", profileSpecV1)
	if _, _, err := f.rec.EnsureSnapshot(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	rec := &domain.ObservedStateRecord{Machine: "ws-1", InventorySeq: 7,
		Payload: observationPayload(t, "sha256:wanted", "sha256:actual", "v1")}
	f.rec.AssignObservationGeneration(f.ctx, rec)
	if _, err := f.observed.Store(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	// 新鲜观测 → Drifted=True（确定判决）。
	if _, err := f.rec.EvaluateDrift(f.ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	f.mustCondition(f.status("ws-1"), domain.ConditionDrifted, domain.ConditionTrue, "")
	if age == 0 {
		return f
	}
	old := time.Now().UTC().Add(-age)
	if err := f.mstatus.UpdateStatus(f.ctx, "ws-1", func(st *domain.MachineStatus) error {
		st.LastInventoryAt = &old
		if unresolved {
			st.UnresolvedOperation = &domain.UnresolvedOperationRef{ID: "op-x", Phase: domain.OperationPhaseRunning}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestStaleObservationSticksAcrossEvaluateDrift：扫描置 Unknown 后，再次求值
// （drift 端点路径）不得改写成 True/False。
func TestStaleObservationSticksAcrossEvaluateDrift(t *testing.T) {
	f := staleFixture(t, 2*time.Hour, false)
	if err := f.rec.ScanFreshness(f.ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	f.mustCondition(f.status("ws-1"), domain.ConditionDrifted, domain.ConditionUnknown,
		domain.DriftReasonStaleObservation)

	// 核查复现的那一步：再来一次求值。
	for i := 0; i < 2; i++ {
		eval, err := f.rec.EvaluateDrift(f.ctx, "ws-1")
		if err != nil {
			t.Fatal(err)
		}
		if eval.DriftStatus != domain.ConditionUnknown || eval.DriftReason != domain.DriftReasonStaleObservation {
			t.Fatalf("evaluate #%d = %s(%s), want Unknown(StaleObservation)", i+1, eval.DriftStatus, eval.DriftReason)
		}
		f.mustCondition(f.status("ws-1"), domain.ConditionDrifted, domain.ConditionUnknown,
			domain.DriftReasonStaleObservation)
	}
}

// TestFreshObservationStillEvaluates：窗口内的观测必须照常求值——证明上面的
// "粘住"不是因为求值只会返回 Unknown。
func TestFreshObservationStillEvaluates(t *testing.T) {
	f := staleFixture(t, 0, false)
	eval, err := f.rec.EvaluateDrift(f.ctx, "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if eval.DriftStatus != domain.ConditionTrue {
		t.Fatalf("fresh observation should evaluate to Drifted=True, got %s(%s)", eval.DriftStatus, eval.DriftReason)
	}
}

// TestUnresolvedOperationSuppressesStaleness：存在未决操作时不按新鲜度改写（§6.2）。
func TestUnresolvedOperationSuppressesStaleness(t *testing.T) {
	f := staleFixture(t, 2*time.Hour, true)
	eval, err := f.rec.EvaluateDrift(f.ctx, "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if eval.DriftStatus != domain.ConditionTrue {
		t.Fatalf("with an unresolved operation drift must still evaluate to True, got %s(%s)",
			eval.DriftStatus, eval.DriftReason)
	}
}
