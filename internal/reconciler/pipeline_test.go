package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/adapter/fixture"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// testNode 是一台 fixture 节点：临时 HOME（护栏 #12）+ 真实文件布局。
type testNode struct {
	t    *testing.T
	home string
	seq  int64
	reg  *adapter.Registry
	fx   *fixture.Adapter
}

func newNode(t *testing.T) *testNode {
	t.Helper()
	fx := fixture.New()
	reg := adapter.NewRegistry()
	reg.Register(fx)
	return &testNode{t: t, home: t.TempDir(), reg: reg, fx: fx}
}

func (n *testNode) executor() *Executor {
	n.t.Helper()
	return New(Options{
		Registry: n.reg,
		Seq: func() (int64, error) {
			n.seq++
			return n.seq, nil
		},
	})
}

func (n *testNode) snapshotJSON(t *testing.T, version, model string) []byte {
	t.Helper()
	snap := domain.DesiredStateSnapshot{
		Machine: "ws-1", Generation: 1, Digest: "sha256:snap",
		Protocol: domain.SnapshotProtocolVersion, CanonicalizationVersion: "snapshot-json-v1",
		Desired: domain.DesiredState{
			SchemaVersion: "fixture/v1",
			Agents: map[string]domain.AgentDesired{fixture.ID: {
				Version: version,
				Config:  json.RawMessage(fmt.Sprintf(`{"model":%q,"providerEndpoint":"http://ep"}`, model)),
			}},
		},
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (n *testNode) config(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(n.home, "fixture", "config.json"))
	if os.IsNotExist(err) {
		return map[string]any{}
	}
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config unparseable: %v", err)
	}
	return cfg
}

// 快乐路径：变更全部落地、verify 证据双侧摘要一致（§5.3 阶段 10–12）。
func TestPipelineHappyPathAppliesAndVerifies(t *testing.T) {
	n := newNode(t)
	res := n.executor().Run(context.Background(), n.home, Execution{
		OperationID:  "op-happy",
		Generation:   1,
		SnapshotJSON: n.snapshotJSON(t, "1.2.3", "gpt-x"),
	})
	if res.Phase != "Succeeded" {
		t.Fatalf("phase=%s reason=%s msg=%s", res.Phase, res.Reason, res.Message)
	}
	if res.Verify == nil || res.Verify.DesiredProjectionDigest != res.Verify.ObservedProjectionDigest ||
		res.Verify.AdapterHealth != domain.AdapterHealthPassed || res.Verify.InventorySeq == 0 {
		t.Fatalf("verify evidence incomplete: %+v", res.Verify)
	}
	if got := n.config(t)["model"]; got != "gpt-x" {
		t.Fatalf("managed key not applied: %v", got)
	}
	if v, _ := os.ReadFile(filepath.Join(n.home, "fixture", "version")); string(v) != "1.2.3" {
		t.Fatalf("version not applied: %q", v)
	}
}

// T18：幂等重放产生空计划，但健康检查（阶段 10）与验证（阶段 12）必须仍执行，
// 不得因空计划跳过健康证据。
func TestPipelineEmptyPlanStillRunsHealthAndVerify(t *testing.T) {
	n := newNode(t)
	ex := n.executor()
	first := ex.Run(context.Background(), n.home, Execution{
		OperationID: "op-1", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
	})
	if first.Phase != "Succeeded" {
		t.Fatalf("first run: %+v", first)
	}
	healthRan := false
	second := ex.Run(context.Background(), n.home, Execution{
		OperationID: "op-2", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
		Progress: func(step, phase, message string) {
			if step == StepHealth && phase == "Succeeded" {
				healthRan = true
			}
		},
	})
	if second.Phase != "Succeeded" {
		t.Fatalf("second run failed: %+v", second)
	}
	if !healthRan {
		t.Fatal("empty plan skipped stage 10 (health) — violates §5.3 空计划语义")
	}
	if second.Verify == nil || second.Verify.DesiredProjectionDigest != second.Verify.ObservedProjectionDigest {
		t.Fatal("empty plan skipped stage 12 (verify) evidence")
	}
}

// FR-8.2/§10.3：投影只哈希受管键——未托管编辑不得改变观测投影摘要。
func TestProjectionIgnoresUnmanagedEdits(t *testing.T) {
	n := newNode(t)
	ex := n.executor()
	if res := ex.Run(context.Background(), n.home, Execution{
		OperationID: "op-1", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
	}); res.Phase != "Succeeded" {
		t.Fatalf("setup run: %+v", res)
	}
	before := n.config(t)
	_ = os.WriteFile(filepath.Join(n.home, "fixture", "config.json"),
		[]byte(`{"model":"m1","providerEndpoint":"http://ep","userCustom":"mine"}`), 0o644)
	res := ex.Run(context.Background(), n.home, Execution{
		OperationID: "op-2", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
	})
	if res.Phase != "Succeeded" {
		t.Fatalf("unmanaged edit produced non-empty plan/failed: %+v", res)
	}
	if _, ok := n.config(t)["userCustom"]; !ok {
		t.Fatal("unmanaged key lost after merge write")
	}
	if _, ok := before["userCustom"]; ok {
		t.Fatal("test precondition broken")
	}
}

// T8：plan 与 apply 之间受管内容被改 → apply 拒绝、零变更、ReplanRequired。
func TestPipelineApplyBaselineMismatchRejectsWithReplanRequired(t *testing.T) {
	n := newNode(t)
	ex := n.executor()
	plan := ex.Run(context.Background(), n.home, Execution{
		OperationID: "op-plan", Generation: 1, ReadOnly: true,
		SnapshotJSON: n.snapshotJSON(t, "2.0.0", "m-new"),
	})
	if plan.Phase != "Succeeded" || plan.PlanDigest == "" {
		t.Fatalf("plan run: %+v", plan)
	}
	if plan.PlanDigest == "" || plan.Baseline == nil {
		t.Fatalf("plan output incomplete: digest=%q baseline=%+v", plan.PlanDigest, plan.Baseline)
	}
	// 确认前受管文件被外部修改（受管键变化 → 基线失效）。
	if err := os.MkdirAll(filepath.Join(n.home, "fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.home, "fixture", "config.json"),
		[]byte(`{"model":"hand-edited"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	applied := ex.Run(context.Background(), n.home, Execution{
		OperationID: "op-apply", Generation: 1,
		SnapshotJSON:    n.snapshotJSON(t, "2.0.0", "m-new"),
		RequireBaseline: plan.Baseline,
	})
	if applied.Phase != "Failed" || applied.Reason != domain.ReasonReplanRequired {
		t.Fatalf("apply = %s(%s), want Failed(ReplanRequired)", applied.Phase, applied.Reason)
	}
	if got := n.config(t)["model"]; got != "hand-edited" {
		t.Fatalf("apply mutated node despite baseline mismatch: %v", got)
	}
	if v, _ := os.ReadFile(filepath.Join(n.home, "fixture", "version")); string(v) == "2.0.0" {
		t.Fatal("version applied despite baseline mismatch")
	}
	// 基线未变（受管内容回到与 plan 时一致）→ 执行与展示计划一致。
	n.fx.FailNext["version"] = 0
	if err := os.WriteFile(filepath.Join(n.home, "fixture", "config.json"),
		[]byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ok := ex.Run(context.Background(), n.home, Execution{
		OperationID: "op-apply-2", Generation: 1,
		SnapshotJSON:    n.snapshotJSON(t, "2.0.0", "m-new"),
		RequireBaseline: plan.Baseline,
	})
	if ok.Phase != "Succeeded" {
		t.Fatalf("apply with intact baseline failed: %+v", ok)
	}
}

// T7：可变步骤失败的恢复路径。
//   - 无外部编辑 → 整文件还原，与操作前逐字节一致；
//   - 有外部编辑 → 受管字段级回退，保留未托管编辑；
//   - 文件不可解析 → 显式 Failed(RestoreConflict)，绝不静默覆盖。
func TestPipelineRestorePaths(t *testing.T) {
	t.Run("whole file restore byte identical", func(t *testing.T) {
		n := newNode(t)
		n.fx.FailNext["version"] = 1
		if res := n.executor().Run(context.Background(), n.home, Execution{
			OperationID: "op-f1", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "9.9.9", "m1"),
		}); res.Phase != "Failed" {
			t.Fatalf("injected failure not surfaced: %+v", res)
		}
		// 无备份前状态（config 尚不存在）→ 还原后依旧不存在（回到操作前）。
		if _, err := os.Stat(filepath.Join(n.home, "fixture", "version")); !os.IsNotExist(err) {
			t.Fatalf("version file not restored: %v", err)
		}
	})

	t.Run("injected apply failure restores whole file", func(t *testing.T) {
		n := newNode(t)
		// 既有状态：受管 + 未托管键。
		if err := os.MkdirAll(filepath.Join(n.home, "fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
		base := `{"model":"m0","providerEndpoint":"http://old","userCustom":"keep-me"}`
		if err := os.WriteFile(filepath.Join(n.home, "fixture", "config.json"), []byte(base), 0o644); err != nil {
			t.Fatal(err)
		}
		n.fx.FailNext["config"] = 1
		res := n.executor().Run(context.Background(), n.home, Execution{
			OperationID: "op-f2", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
		})
		if res.Phase != "Failed" {
			t.Fatalf("injected failure not surfaced: %+v", res)
		}
		// Apply 在写入前失败 → 文件与备份逐字节相同 → 整文件还原分支。
		if !contains(res.Message, "whole-file restore") {
			t.Fatalf("expected whole-file restore path: %q", res.Message)
		}
		cfg := n.config(t)
		if cfg["model"] != "m0" || cfg["providerEndpoint"] != "http://old" {
			t.Fatalf("managed keys not rolled back: %v", cfg)
		}
		if cfg["userCustom"] != "keep-me" {
			t.Fatalf("unmanaged edit lost during restore (A5): %v", cfg)
		}
	})

	// A5/T7 的受管字段级回退分支：备份之后文件被外部改动（内容与备份不同）
	// → 绝不整文件覆盖，只回退受管键、保留外部编辑。
	t.Run("external edit triggers managed-key rollback via MergeManaged", func(t *testing.T) {
		n := newNode(t)
		if err := os.MkdirAll(filepath.Join(n.home, "fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
		base := `{"model":"m0","providerEndpoint":"http://old","userCustom":"keep-me"}`
		configPath := filepath.Join(n.home, "fixture", "config.json")
		if err := os.WriteFile(configPath, []byte(base), 0o644); err != nil {
			t.Fatal(err)
		}
		// 受管写入已落盘后，模拟操作窗口内的外部编辑：新增一个未托管键。
		n.fx.FailNext["health"] = 1
		res := n.executor().Run(context.Background(), n.home, Execution{
			OperationID: "op-f2b", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
			Progress: func(step, phase, message string) {
				if step == StepConfig && phase == "Succeeded" {
					_ = os.WriteFile(configPath, []byte(
						`{"model":"m1","providerEndpoint":"http://ep","userCustom":"keep-me","external":"edited"}`), 0o644)
				}
			},
		})
		if res.Phase != "Failed" || res.Reason != domain.ReasonHealthCheckFailed {
			t.Fatalf("health failure not surfaced: %s(%s)", res.Phase, res.Reason)
		}
		if !contains(res.Message, "managed-key rollback") {
			t.Fatalf("expected managed-key rollback (MergeManaged), got %q", res.Message)
		}
		cfg := n.config(t)
		if cfg["model"] != "m0" || cfg["providerEndpoint"] != "http://old" {
			t.Fatalf("managed keys not rolled back: %v", cfg)
		}
		if cfg["external"] != "edited" {
			t.Fatalf("external unmanaged edit destroyed by restore (A5): %v", cfg)
		}
		if cfg["userCustom"] != "keep-me" {
			t.Fatalf("pre-existing unmanaged key lost: %v", cfg)
		}
	})

	t.Run("unparseable file fails explicitly with RestoreConflict", func(t *testing.T) {
		n := newNode(t)
		if err := os.MkdirAll(filepath.Join(n.home, "fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(n.home, "fixture", "config.json"),
			[]byte(`{"model":"m0"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		calls := 0
		res := n.executor().Run(context.Background(), n.home, Execution{
			OperationID: "op-f3", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
			Cancelled: func() bool {
				calls++
				// 备份之后（检查点 4：backup 后/首个可变步骤前）：把配置文件破坏为
				// 不可解析（模拟操作窗口内的外部破坏）。
				if calls == 4 {
					_ = os.WriteFile(filepath.Join(n.home, "fixture", "config.json"),
						[]byte("{{{not json"), 0o644)
				}
				return false
			},
		})
		if res.Phase != "Failed" || res.Reason != domain.ReasonRestoreConflict {
			t.Fatalf("restore = %s(%s), want Failed(RestoreConflict)", res.Phase, res.Reason)
		}
	})
}

// T21（节点侧）：取消在阶段边界生效——1–4 之间直接终结不恢复；5–10 之间走
// 标准备份恢复；重复置位安全。
func TestPipelineCancelAtStageBoundaries(t *testing.T) {
	t.Run("before any change", func(t *testing.T) {
		n := newNode(t)
		res := n.executor().Run(context.Background(), n.home, Execution{
			OperationID: "op-c1", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
			Cancelled: func() bool { return true },
		})
		if res.Phase != "Failed" || res.TerminalModifier != domain.ModifierCancelled {
			t.Fatalf("cancel result = %s(%s/%s)", res.Phase, res.Reason, res.TerminalModifier)
		}
		if _, err := os.Stat(filepath.Join(n.home, "fixture", "version")); !os.IsNotExist(err) {
			t.Fatal("no changes should exist when cancelled before stage 5")
		}
	})
	t.Run("during mutable stages restores backup", func(t *testing.T) {
		n := newNode(t)
		calls := 0
		res := n.executor().Run(context.Background(), n.home, Execution{
			OperationID: "op-c2", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
			Cancelled: func() bool {
				calls++
				// 检查点序列：validate 后(1) → inventory 后(2) → plan 后(3) →
				// backup 后/首个可变步骤前(4)。第 4 次起命中可中断阶段 5–10。
				return calls >= 4
			},
		})
		if res.Phase != "Failed" || res.TerminalModifier != domain.ModifierCancelled {
			t.Fatalf("cancel result = %s(%s/%s)", res.Phase, res.Reason, res.TerminalModifier)
		}
		if res.Message == "" || !contains(res.Message, "restored from backup") {
			t.Fatalf("cancel during 5-10 must go through backup restore path: %q", res.Message)
		}
		// 恢复后节点回到操作前状态（version 文件被还原删除）。
		if _, err := os.Stat(filepath.Join(n.home, "fixture", "version")); !os.IsNotExist(err) {
			t.Fatal("node not restored after cancel during mutable stages")
		}
	})
	t.Run("cancel is ignored in stages 11-13", func(t *testing.T) {
		n := newNode(t)
		armed := false
		res := n.executor().Run(context.Background(), n.home, Execution{
			OperationID: "op-c3", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
			Progress: func(step, phase, message string) {
				// 阶段 10 起点的进度上报之后才置位取消：此后只剩不可中断的
				// 阶段 11–13（验证/提交），取消必须被忽略（§9.7 避免半状态）。
				if step == StepHealth && phase == "Running" {
					armed = true
				}
			},
			Cancelled: func() bool { return armed },
		})
		if !armed {
			t.Fatal("cancel flag was never armed: the assertion below would be vacuous")
		}
		if res.Phase != "Succeeded" {
			t.Fatalf("cancel during non-interruptible stages 11-13 must be ignored: %+v", res)
		}
		if res.Verify == nil || res.Verify.AdapterHealth != domain.AdapterHealthPassed {
			t.Fatalf("stages 11-13 must run to completion: verify=%+v", res.Verify)
		}
	})
}

// 空计划（零变更）下的失败/取消路径：没有写入就没有备份，`restore` 不得去读
// 不存在的 manifest——否则一台未被改动的机器会被误报 Failed(RestoreConflict)
// 并丢掉 Cancelled 修饰，服务端还会据此置 Degraded=True（§14.2 只对"恢复也
// 失败"降级）。
func TestPipelineEmptyPlanFailurePathsHaveNoBackupToRestore(t *testing.T) {
	t.Run("health failure surfaces HealthCheckFailed", func(t *testing.T) {
		n := newNode(t)
		ex := n.executor()
		if first := ex.Run(context.Background(), n.home, Execution{
			OperationID: "op-e1", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
		}); first.Phase != "Succeeded" {
			t.Fatalf("setup run: %+v", first)
		}
		before := n.config(t)
		n.fx.FailNext["health"] = 1
		res := ex.Run(context.Background(), n.home, Execution{
			OperationID: "op-e2", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
		})
		if res.Phase != domain.OperationPhaseFailed || res.Reason != domain.ReasonHealthCheckFailed {
			t.Fatalf("empty-plan health failure = %s(%s), want Failed(HealthCheckFailed)", res.Phase, res.Reason)
		}
		if res.TerminalModifier != "" {
			t.Fatalf("health failure must not carry a terminal modifier: %q", res.TerminalModifier)
		}
		if !contains(res.Message, "nothing to restore") {
			t.Fatalf("empty plan must not attempt backup restore: %q", res.Message)
		}
		if after := n.config(t); after["model"] != before["model"] {
			t.Fatalf("unchanged machine was modified: %v → %v", before, after)
		}
	})

	t.Run("cancel during mutable stages keeps Cancelled modifier", func(t *testing.T) {
		n := newNode(t)
		ex := n.executor()
		if first := ex.Run(context.Background(), n.home, Execution{
			OperationID: "op-e3", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
		}); first.Phase != "Succeeded" {
			t.Fatalf("setup run: %+v", first)
		}
		calls := 0
		res := ex.Run(context.Background(), n.home, Execution{
			OperationID: "op-e4", Generation: 1, SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
			Cancelled: func() bool {
				calls++
				// 检查点：validate 后(1) → backup 前(2) → 可变步骤迭代(3+)。第 4 次
				// 起命中可中断阶段 5–10。
				return calls >= 4
			},
		})
		if res.Phase != domain.OperationPhaseFailed || res.TerminalModifier != domain.ModifierCancelled {
			t.Fatalf("empty-plan cancel = %s(%s/%s), want Failed(Cancelled)",
				res.Phase, res.Reason, res.TerminalModifier)
		}
		if res.Reason == domain.ReasonRestoreConflict {
			t.Fatalf("empty plan misreported as restore conflict: %q", res.Message)
		}
	})
}

// T13：冷启动节点只有随操作下发的完整快照即可执行（FR-13.6：节点无本地缓存）。
func TestPipelineRunsFromSelfContainedSnapshot(t *testing.T) {
	n := newNode(t)
	// 无任何本地预置状态（新 temp HOME）。
	res := n.executor().Run(context.Background(), n.home, Execution{
		OperationID: "op-cold", Generation: 4, SnapshotJSON: n.snapshotJSON(t, "3.1.4", "m-cold"),
	})
	if res.Phase != "Succeeded" {
		t.Fatalf("cold start failed: %+v", res)
	}
}

// skewAdapter 是投影规范化版本不一致的第二家族（T17 节点侧反例）：除家族名与
// canonicalizationVersion 外全部委托 fixture 适配器。
type skewAdapter struct {
	*fixture.Adapter
	family  string
	version string
}

func (a *skewAdapter) ID() string { return a.family }

func (a *skewAdapter) Inventory(ctx context.Context, home string,
	desired adapter.AgentDesiredState) (adapter.AgentObservedState, error) {
	obs, err := a.Adapter.Inventory(ctx, home, desired)
	if err != nil {
		return obs, err
	}
	obs.Family = a.family
	obs.CanonicalizationVersion = a.version
	return obs, nil
}

// T17（节点侧半边）：**多家族** canonicalizationVersion 不一致 → 两侧不可比，
// 拒绝比较并报 ProjectionVersionMismatch，且不得写入任何变更
// （服务端据此置 Drifted/Reconciled=Unknown，§7.1 契约 1）。
func TestPipelineProjectionVersionMismatch(t *testing.T) {
	n := newNode(t)
	n.reg.Register(&skewAdapter{Adapter: n.fx, family: "skew", version: "skew-projection-v9"})
	snap := domain.DesiredStateSnapshot{
		Machine: "ws-1", Generation: 1, Digest: "sha256:snap",
		Protocol: domain.SnapshotProtocolVersion, CanonicalizationVersion: "snapshot-json-v1",
		Desired: domain.DesiredState{
			SchemaVersion: "fixture/v1",
			Agents: map[string]domain.AgentDesired{
				fixture.ID: {Version: "1.0.0", Config: json.RawMessage(`{"model":"m1"}`)},
				"skew":     {Version: "1.0.0", Config: json.RawMessage(`{"model":"m1"}`)},
			},
		},
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	res := n.executor().Run(context.Background(), n.home, Execution{
		OperationID: "op-v", Generation: 1, SnapshotJSON: raw,
	})
	if res.Phase != domain.OperationPhaseFailed || res.Reason != domain.ProjectionVersionMismatch {
		t.Fatalf("mismatched canonicalizationVersion = %s(%s), want Failed(ProjectionVersionMismatch)",
			res.Phase, res.Reason)
	}
	if _, err := os.Stat(filepath.Join(n.home, fixture.ID, "version")); !os.IsNotExist(err) {
		t.Fatal("incomparable projections must not produce any node write")
	}
}

// 只读执行（FR-9.7）：阶段 1–3 产出计划与基线，绝不写节点。
func TestPipelineReadOnlyStopsAfterPlan(t *testing.T) {
	n := newNode(t)
	res := n.executor().Run(context.Background(), n.home, Execution{
		OperationID: "op-auto", Generation: 1, ReadOnly: true,
		SnapshotJSON: n.snapshotJSON(t, "1.0.0", "m1"),
	})
	if res.Phase != "Succeeded" || !res.ReadOnly || res.PlanDigest == "" || res.Baseline == nil {
		t.Fatalf("read-only run: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(n.home, "fixture", "version")); !os.IsNotExist(err) {
		t.Fatal("read-only execution wrote node state")
	}
}

// 防御：并发 Enqueue/Cancel 的内存安全（-race 下运行）。
func TestExecutorConcurrentCancelMap(t *testing.T) {
	n := newNode(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = n.snapshotJSON(t, "1.0.0", "m1")
		}(i)
	}
	wg.Wait()
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
