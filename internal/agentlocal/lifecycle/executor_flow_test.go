package lifecycle

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// 本文件是 KM-49 执行引擎的隔离环境端到端证明（KM-46 C0–C7 同款纪律）：
// 真实 NpmEngine + fake npm 替身 + 临时 HOME，经 NodeExecutor.Execute 完整走
// install → upgrade → uninstall。准入策略注入 allowPolicy 表示"隔离验证配置"
// ——生产装配固定用 NewRegistryPolicy（unverified/blocked 组合执行前拒绝，
// 见 TestExecuteRefusesBlockedAndUnverifiedCombos）；本文件证明的是准入放行后
// 的执行语义：固定版本、重发现核验与数据保护零变更。

// executorCodexTarget 构造带台账平台快照的目标（与运行时一致，通过平台闸）。
func executorCodexTarget(typ, version string) TargetSpec {
	return TargetSpec{
		Machine: "m1", Type: typ, User: "alice", Agent: "codex",
		Method: "npm", Package: "@openai/codex", Version: version,
		Env: "native",
		OS:  runtime.GOOS, Arch: runtime.GOARCH,
		Executables: []string{"codex"},
	}
}

// TestExecutorFlowDataProtectionZeroChange：完整流程逐目标执行后，~/.codex
// 替身与共享目录必须零变更（内容、权限、软链、悬空链），且执行后的逐目标
// 结果都是 succeeded + 引擎核验的实测版本（验收项 3/4 在执行引擎层的证明）。
func TestExecutorFlowDataProtectionZeroChange(t *testing.T) {
	e := newEnv(t)
	for _, v := range []string{"0.44.0", "0.45.0"} {
		if err := e.Publish("@openai/codex", v, "codex", "codex-cli "+v); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.SeedCodexData(); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(e.Home, ".codex")
	before, err := SnapshotDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	x := NewNodeExecutor(NodeExecutorConfig{
		Engines: map[string]Engine{"npm": newEngine()},
		Policy:  allowPolicy{},
		Timeout: 2 * time.Minute,
	})

	// 安装 0.44.0 → 升级 0.45.0 → 卸载，一次 OpRequest 一个阶段（与控制面
	// 逐操作派发同构）。
	flows := []struct {
		typ     string
		version string
		want    string // 期望核验版本；卸载为空
		reason  string
	}{
		{domain.LifecycleOpInstall, "0.44.0", "0.44.0", domain.ReasonVerifiedPresent},
		{domain.LifecycleOpUpgrade, "0.45.0", "0.45.0", domain.ReasonVerifiedPresent},
		{domain.LifecycleOpUninstall, "", "", domain.ReasonVerifiedAbsent},
	}
	for i, f := range flows {
		rep := x.Execute(context.Background(), OpRequest{
			OpID:    fmt.Sprintf("op-flow-%d", i),
			Type:    f.typ,
			Version: f.version,
			Targets: []TargetSpec{executorCodexTarget(f.typ, f.version)},
		})
		if len(rep.Results) != 1 {
			t.Fatalf("step %s: results = %d", f.typ, len(rep.Results))
		}
		r := rep.Results[0]
		if r.State != domain.TargetSucceeded || r.Reason != f.reason {
			t.Fatalf("step %s: state/reason = %s/%s, want %s/%s (message: %s)",
				f.typ, r.State, r.Reason, domain.TargetSucceeded, f.reason, r.Message)
		}
		if r.Evidence.ObservedVersion != f.want {
			t.Fatalf("step %s: verified version %q, want %q", f.typ, r.Evidence.ObservedVersion, f.want)
		}
		if err := domain.ValidateTargetResult(r.State, r.Reason); err != nil {
			t.Fatalf("step %s: %v", f.typ, err)
		}
	}

	after, err := SnapshotDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if diffs := before.Diff(after); len(diffs) != 0 {
		t.Fatalf("user data changed during executor flow:\n%s", before.Report(after))
	}
}

// TestExecutorFixedVersionSurvivesUpstreamDrift：执行期间上游出现新版本不得
// 改变本次目标——安装目标固定在 0.44.0，注册表 latest 漂到 0.45.0 后执行，
// 实测版本必须仍是 0.44.0（验收项 1 的执行层证明）。
func TestExecutorFixedVersionSurvivesUpstreamDrift(t *testing.T) {
	e := newEnv(t)
	for _, v := range []string{"0.44.0", "0.45.0"} {
		if err := e.Publish("@openai/codex", v, "codex", "codex-cli "+v); err != nil {
			t.Fatal(err)
		}
	}
	x := NewNodeExecutor(NodeExecutorConfig{
		Engines: map[string]Engine{"npm": newEngine()},
		Policy:  allowPolicy{},
		Timeout: 2 * time.Minute,
	})

	// "创建时"固定 0.44.0；随后上游发布 0.46.0 并把 latest 指过去（模拟执行
	// 前上游漂移——固定版本意图不随之改变）。
	if err := e.Publish("@openai/codex", "0.46.0", "codex", "codex-cli 0.46.0"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetLatest("@openai/codex", "0.46.0"); err != nil {
		t.Fatal(err)
	}

	rep := x.Execute(context.Background(), OpRequest{
		OpID: "op-fixed", Type: domain.LifecycleOpInstall, Version: "0.44.0",
		Targets: []TargetSpec{executorCodexTarget(domain.LifecycleOpInstall, "0.44.0")},
	})
	r := rep.Results[0]
	if r.State != domain.TargetSucceeded || r.Evidence.ObservedVersion != "0.44.0" {
		t.Fatalf("pinned install drifted: state=%s verified=%q (message: %s)",
			r.State, r.Evidence.ObservedVersion, r.Message)
	}
}

// TestExecutorFailureRecoveryDataProtection：验收项 4 的"失败恢复"证明——
// 安装命令失败、卸载命令失败、以及"失败后重试成功"的恢复路径全程，
// ~/.codex 替身必须逐字节不变（失败不许碰用户数据，恢复也不许碰）。
func TestExecutorFailureRecoveryDataProtection(t *testing.T) {
	e := newEnv(t)
	for _, v := range []string{"0.44.0", "0.45.0"} {
		if err := e.Publish("@openai/codex", v, "codex", "codex-cli "+v); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.SeedCodexData(); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(e.Home, ".codex")
	before, err := SnapshotDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	x := NewNodeExecutor(NodeExecutorConfig{
		Engines: map[string]Engine{"npm": newEngine()},
		Policy:  allowPolicy{},
		Timeout: 2 * time.Minute,
	})
	install := executorCodexTarget(domain.LifecycleOpInstall, "0.44.0")
	uninstall := executorCodexTarget(domain.LifecycleOpUninstall, "")
	upgrade := executorCodexTarget(domain.LifecycleOpUpgrade, "0.45.0")
	// 升级需要已安装的 0.44.0（先直装，随后快照）。
	if err := e.InstallDirect(e.Prefix, "@openai/codex@0.44.0"); err != nil {
		t.Fatal(err)
	}

	// 1) 安装失败：fake npm 注入 .fail-install → failed(op-failed)。
	if err := e.SetFlag(".fail-install", true); err != nil {
		t.Fatal(err)
	}
	rep := x.Execute(context.Background(), OpRequest{
		OpID: "op-fail-install", Type: domain.LifecycleOpInstall, Version: "0.44.0",
		Targets: []TargetSpec{install},
	})
	if r := rep.Results[0]; r.State != domain.TargetFailed || r.Reason != domain.ReasonOpFailed {
		t.Fatalf("failed install = %s/%s, want failed/op-failed", r.State, r.Reason)
	}

	// 2) 升级失败（同样注入）→ failed；随后卸载失败 → failed。
	rep = x.Execute(context.Background(), OpRequest{
		OpID: "op-fail-upgrade", Type: domain.LifecycleOpUpgrade, Version: "0.45.0",
		Targets: []TargetSpec{upgrade},
	})
	if r := rep.Results[0]; r.State != domain.TargetFailed || r.Reason != domain.ReasonOpFailed {
		t.Fatalf("failed upgrade = %s/%s", r.State, r.Reason)
	}
	if err := e.SetFlag(".fail-uninstall", true); err != nil {
		t.Fatal(err)
	}
	rep = x.Execute(context.Background(), OpRequest{
		OpID: "op-fail-uninstall", Type: domain.LifecycleOpUninstall,
		Targets: []TargetSpec{uninstall},
	})
	if r := rep.Results[0]; r.State != domain.TargetFailed || r.Reason != domain.ReasonOpFailed {
		t.Fatalf("failed uninstall = %s/%s", r.State, r.Reason)
	}

	// 3) 失败恢复：解除注入后按同一固定版本意图重试，全部成功。
	if err := e.SetFlag(".fail-install", false); err != nil {
		t.Fatal(err)
	}
	if err := e.SetFlag(".fail-uninstall", false); err != nil {
		t.Fatal(err)
	}
	for i, f := range []struct {
		typ, version string
		want         string
	}{
		{domain.LifecycleOpUpgrade, "0.45.0", "0.45.0"},
		{domain.LifecycleOpUninstall, "", ""},
	} {
		rep := x.Execute(context.Background(), OpRequest{
			OpID:    fmt.Sprintf("op-recover-%d", i),
			Type:    f.typ,
			Version: f.version,
			Targets: []TargetSpec{executorCodexTarget(f.typ, f.version)},
		})
		if r := rep.Results[0]; r.State != domain.TargetSucceeded {
			t.Fatalf("recovery step %s: %s/%s (%s)", f.typ, r.State, r.Reason, r.Message)
		}
		if r := rep.Results[0]; r.Evidence.ObservedVersion != f.want {
			t.Fatalf("recovery step %s: verified %q, want %q", f.typ, r.Evidence.ObservedVersion, f.want)
		}
	}

	// 全程（含失败与恢复）用户数据零变更。
	after, err := SnapshotDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if diffs := before.Diff(after); len(diffs) != 0 {
		t.Fatalf("user data changed across failure/recovery flow:\n%s", before.Report(after))
	}
}
