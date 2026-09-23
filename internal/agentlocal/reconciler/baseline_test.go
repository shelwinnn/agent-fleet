package reconciler

// KM-26 核查回归（FR-12.7）：apply 前基线比对必须包含 inventorySeq。
// "摘要相同但 seq 没有前进"意味着没有真正重采观测（例如复用了 state/last-observed.json
// 里的旧摘要），必须与摘要变化同样处理：零变更 + Failed(ReplanRequired)。

import (
	"context"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

func TestBaselineSeqNotAdvancedIsReplanRequired(t *testing.T) {
	n := newNode(t)
	ex := n.executor()
	ctx := context.Background()
	snap := n.snapshotJSON(t, "1.0.0", "m1")

	// 第一次：正常 plan+apply，得到当时的 seq。
	first := ex.Run(ctx, n.home, Execution{OperationID: "op-1", Generation: 1, SnapshotJSON: snap})
	if first.Phase != domain.OperationPhaseSucceeded {
		t.Fatalf("first run failed: %+v", first)
	}
	planSeq := first.Baseline.InventorySeq

	// 把 seq 固定回计划时的值：模拟"没有重新采集观测"。
	ex2 := New(Options{
		Registry: n.reg,
		Seq:      func() (int64, error) { return planSeq, nil },
	})
	res := ex2.Run(ctx, n.home, Execution{
		OperationID: "op-2", Generation: 1, SnapshotJSON: snap,
		RequireBaseline: &Baseline{
			ObservedProjectionDigest: first.Baseline.ObservedProjectionDigest,
			InventorySeq:             planSeq,
		},
	})
	if res.Phase != domain.OperationPhaseFailed {
		t.Fatalf("phase = %s, want Failed", res.Phase)
	}
	if res.Reason != domain.ReasonReplanRequired {
		t.Fatalf("reason = %s, want %s", res.Reason, domain.ReasonReplanRequired)
	}
}

func TestBaselineDigestChangeIsReplanRequired(t *testing.T) {
	n := newNode(t)
	ex := n.executor()
	ctx := context.Background()
	snap := n.snapshotJSON(t, "1.0.0", "m1")
	first := ex.Run(ctx, n.home, Execution{OperationID: "op-1", Generation: 1, SnapshotJSON: snap})
	if first.Phase != domain.OperationPhaseSucceeded {
		t.Fatalf("first run failed: %+v", first)
	}
	// 摘要不同（内容变了）：即使 seq 前进也必须拒绝。
	res := ex.Run(ctx, n.home, Execution{
		OperationID: "op-2", Generation: 1, SnapshotJSON: snap,
		RequireBaseline: &Baseline{
			ObservedProjectionDigest: "sha256:" + "00",
			InventorySeq:             first.Baseline.InventorySeq,
		},
	})
	if res.Phase != domain.OperationPhaseFailed || res.Reason != domain.ReasonReplanRequired {
		t.Fatalf("want Failed(ReplanRequired), got %s(%s)", res.Phase, res.Reason)
	}
}
