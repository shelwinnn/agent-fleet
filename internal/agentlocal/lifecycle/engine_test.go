package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/lifecycle/testenv"
)

// 本文件在隔离环境中验证 lifecycle 引擎契约（KM-46 验收项 3/4/5）：
// fake npm 替身 + 临时 HOME，不触碰真实系统。Windows 跳过：替身是 POSIX sh，
// Windows 原生（.cmd shim）场景只做源码级分析，真机验证另行执行（未验证）。

var (
	codexReq = Request{
		AgentID:     "codex",
		Package:     "@openai/codex",
		Executables: []string{"codex"},
		Platforms:   []string{"linux", "darwin", "windows"},
	}
	codexUnreq = UninstallRequest{
		AgentID:     "codex",
		Package:     "@openai/codex",
		Executables: []string{"codex"},
		Platforms:   []string{"linux", "darwin", "windows"},
	}
)

func newEnv(t *testing.T) *testenv.Env {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake npm is a POSIX sh double; windows-native flows stay unverified (see KM-46 report)")
	}
	e, err := testenv.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, prefix, registry, path := e.Environ()
	t.Setenv("HOME", home)
	t.Setenv("NPM_PREFIX", prefix)
	t.Setenv("FAKE_NPM_REGISTRY", registry)
	t.Setenv("PATH", path)
	return e
}

func newEngine() *NpmEngine { return NewNpmEngine(nil) }

// TestInstallExactPinnedVersion：安装精确版本后，独立核验读到的版本必须
// 等于请求版本——不采信上游 Result.Version（带版本 spec 时上游恒为零值）。
func TestInstallExactPinnedVersion(t *testing.T) {
	e := newEnv(t)
	if err := e.Publish("@openai/codex", "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
		t.Fatal(err)
	}
	out, err := newEngine().InstallExact(context.Background(), withVersion(codexReq, "0.44.0"))
	if err != nil {
		t.Fatalf("InstallExact: %v", err)
	}
	if out.VerifiedVersion != "0.44.0" {
		t.Fatalf("verified version = %q, want 0.44.0 (upstream zero-value must never surface as success)", out.VerifiedVersion)
	}
	if out.Op != "install" || out.Package != "@openai/codex" || out.RequestedVersion != "0.44.0" {
		t.Fatalf("outcome fields wrong: %+v", out)
	}
	// 程序本体确实落位（fake 前缀里出现 shim），且 HOME 下没有多出东西。
	if _, err := os.Stat(filepath.Join(e.Prefix, "bin", "codex")); err != nil {
		t.Fatalf("shim missing after install: %v", err)
	}
	det, err := newEngine().Detect(context.Background(), codexReq)
	if err != nil {
		t.Fatalf("Detect after install: %v", err)
	}
	if !det.Installed || det.Version != "0.44.0" {
		t.Fatalf("detect after install = %+v, want installed 0.44.0", det)
	}
}

// TestInstallExactRejectsRange：非精确版本必须在触碰任何命令之前被拒绝。
func TestInstallExactRejectsRange(t *testing.T) {
	e := newEnv(t)
	if err := e.Publish("@openai/codex", "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "latest", "*", ">=0.44.0", "0.44", "0.44.x", "v0.44.0"} {
		if _, err := newEngine().InstallExact(context.Background(), withVersion(codexReq, bad)); err == nil {
			t.Fatalf("version %q must be rejected", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(e.Prefix, "lib", "node_modules", "@openai")); !os.IsNotExist(err) {
		t.Fatalf("rejected request must not touch the prefix: %v", err)
	}
}

// TestInstallExactSurfacesCommandFailure：安装命令失败必须显式报错。
func TestInstallExactSurfacesCommandFailure(t *testing.T) {
	e := newEnv(t)
	if err := e.Publish("@openai/codex", "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetFlag(".fail-install", true); err != nil {
		t.Fatal(err)
	}
	if _, err := newEngine().InstallExact(context.Background(), withVersion(codexReq, "0.44.0")); err == nil {
		t.Fatal("injected install failure must surface as error")
	}
}

// TestUpgradeToExactDowngradeProvesPinning：latest=0.45.0 时升级到 0.44.0
// 必须落在 0.44.0——证明走的是固定版本而不是“取最新”。
func TestUpgradeToExactDowngradeProvesPinning(t *testing.T) {
	e := newEnv(t)
	if err := e.Publish("@openai/codex", "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
		t.Fatal(err)
	}
	if err := e.Publish("@openai/codex", "0.45.0", "codex", "codex-cli 0.45.0"); err != nil {
		t.Fatal(err)
	}
	eng := newEngine()
	if _, err := eng.InstallExact(context.Background(), withVersion(codexReq, "0.44.0")); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	out, err := eng.UpgradeToExact(context.Background(), withVersion(codexReq, "0.44.0"))
	if err != nil {
		t.Fatalf("UpgradeToExact downgrade: %v", err)
	}
	if out.VerifiedVersion != "0.44.0" {
		t.Fatalf("downgrade verified version = %q, want 0.44.0 (registry latest is 0.45.0)", out.VerifiedVersion)
	}
	if out.Op != "upgrade" {
		t.Fatalf("op = %q, want upgrade", out.Op)
	}
	// 反向同样成立：0.44.0 → 0.45.0。
	out, err = eng.UpgradeToExact(context.Background(), withVersion(codexReq, "0.45.0"))
	if err != nil {
		t.Fatalf("UpgradeToExact 0.45.0: %v", err)
	}
	if out.VerifiedVersion != "0.45.0" {
		t.Fatalf("upgrade verified version = %q, want 0.45.0", out.VerifiedVersion)
	}
}

// TestUpstreamUpdateDriftsFromPinnedTarget：catalog 原生的 update_cmd 是
// `npm update -g <pkg>`（无目标版本参数）——安装 0.44.0 后驱动上游 Update，
// 结果漂到注册表 latest（0.45.0）。这是引擎拒绝委托 Update、改用“带版本
// spec 走 Install 入口”的运行证据（验收项 3）。
func TestUpstreamUpdateDriftsFromPinnedTarget(t *testing.T) {
	e := newEnv(t)
	if err := e.Publish("@openai/codex", "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
		t.Fatal(err)
	}
	if err := e.Publish("@openai/codex", "0.45.0", "codex", "codex-cli 0.45.0"); err != nil {
		t.Fatal(err)
	}
	eng := newEngine()
	if _, err := eng.InstallExact(context.Background(), withVersion(codexReq, "0.44.0")); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	drifted, err := UpstreamUpdateDrift(context.Background(), codexReq.Package, codexReq.Executables)
	if err != nil {
		t.Fatalf("upstream Update: %v", err)
	}
	if drifted.VerifiedVersion == "0.44.0" {
		t.Fatalf("upstream Update unexpectedly kept pinned target: %+v", drifted)
	}
	if drifted.VerifiedVersion != "0.45.0" {
		t.Fatalf("drift landed on %q, want registry latest 0.45.0", drifted.VerifiedVersion)
	}
	t.Logf("evidence: pinned 0.44.0 installed, upstream Update landed on %s", drifted.VerifiedVersion)
}

// TestUninstallRemovesOnlyTargetInstance：卸载后目标实例消失；同前缀的其他
// 包与第二前缀的同名实例都原样保留（验收项 4 后半）。
func TestUninstallRemovesOnlyTargetInstance(t *testing.T) {
	e := newEnv(t)
	if err := e.Publish("@openai/codex", "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
		t.Fatal(err)
	}
	if err := e.Publish("other-agent", "1.0.0", "other-agent", "other-agent 1.0.0"); err != nil {
		t.Fatal(err)
	}
	eng := newEngine()
	if _, err := eng.InstallExact(context.Background(), withVersion(codexReq, "0.44.0")); err != nil {
		t.Fatalf("install codex: %v", err)
	}
	otherReq := Request{AgentID: "other", Package: "other-agent", Executables: []string{"other-agent"},
		Platforms: []string{"linux"}}
	if _, err := eng.InstallExact(context.Background(), withVersion(otherReq, "1.0.0")); err != nil {
		t.Fatalf("install other-agent: %v", err)
	}
	// 第二前缀里的同版本 codex：另一个安装实例。
	if err := e.InstallDirect(e.Prefix2, "@openai/codex@0.44.0"); err != nil {
		t.Fatalf("seed second instance: %v", err)
	}

	if _, err := eng.Uninstall(context.Background(), codexUnreq); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	det, err := eng.Detect(context.Background(), codexReq)
	if err != nil {
		t.Fatalf("detect after uninstall: %v", err)
	}
	if det.Installed {
		t.Fatalf("target instance still present after uninstall: %+v", det)
	}
	if _, err := os.Stat(filepath.Join(e.Prefix, "bin", "codex")); !os.IsNotExist(err) {
		t.Fatalf("target shim still present: %v", err)
	}
	// 其他实例一：同前缀 other-agent。
	if _, err := os.Stat(filepath.Join(e.Prefix, "bin", "other-agent")); err != nil {
		t.Fatalf("other-agent must survive: %v", err)
	}
	// 其他实例二：第二前缀的 codex。
	if err := os.Setenv("NPM_PREFIX", e.Prefix2); err != nil {
		t.Fatal(err)
	}
	det2, err := eng.Detect(context.Background(), codexReq)
	if err != nil || !det2.Installed || det2.Version != "0.44.0" {
		t.Fatalf("second-prefix codex must be intact, got %+v err=%v", det2, err)
	}
}

// TestDetectFailurePaths（验收项 5）：探测命令失败、版本为空、版本不可解析
// 都必须报错；DetectAll 吞错返回空 inventory 的陷阱必须有运行证据。
func TestDetectFailurePaths(t *testing.T) {
	t.Run("npm list fails hard", func(t *testing.T) {
		e := newEnv(t)
		if err := e.Publish("@openai/codex", "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
			t.Fatal(err)
		}
		if err := e.SetFlag(".fail-list", true); err != nil {
			t.Fatal(err)
		}
		det, err := newEngine().Detect(context.Background(), codexReq)
		if err == nil {
			t.Fatalf("hard npm failure must error, got %+v", det)
		}
		if det.Installed || det.Version != "" {
			t.Fatalf("failed detection must not fabricate an answer: %+v", det)
		}
	})
	t.Run("empty version in inventory", func(t *testing.T) {
		e := newEnv(t)
		// 直接布置一个 version 为空的已安装包（绕过引擎的精确版本校验）。
		if err := e.Publish("@openai/codex", "", "codex", "codex-cli (broken)"); err != nil {
			t.Fatal(err)
		}
		if err := e.InstallDirect(e.Prefix, "@openai/codex@"); err != nil {
			t.Fatal(err)
		}
		if _, err := newEngine().Detect(context.Background(), codexReq); err == nil {
			t.Fatal("empty version must be rejected, not reported as installed")
		}
	})
	t.Run("garbage version in inventory", func(t *testing.T) {
		e := newEnv(t)
		if err := e.Publish("@openai/codex", "not-a-version", "codex", "codex-cli (broken)"); err != nil {
			t.Fatal(err)
		}
		if err := e.InstallDirect(e.Prefix, "@openai/codex"); err != nil {
			t.Fatal(err)
		}
		// 上游 ParseVersion("not-a-version") 返回 {Raw:...} 且无错误——
		// 引擎必须在 Raw 不满足精确版本形态时拒绝。
		if _, err := newEngine().Detect(context.Background(), codexReq); err == nil {
			t.Fatal("garbage version must be rejected")
		}
	})
	t.Run("detect-before-install is not an error", func(t *testing.T) {
		newEnv(t)
		det, err := newEngine().Detect(context.Background(), codexReq)
		if err != nil {
			t.Fatalf("detect on empty prefix: %v", err)
		}
		if det.Installed {
			t.Fatalf("nothing installed but detect claims %+v", det)
		}
	})
	t.Run("DetectAll swallows strategy errors (upstream trap, documented)", func(t *testing.T) {
		e := newEnv(t)
		if err := e.Publish("@openai/codex", "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
			t.Fatal(err)
		}
		if err := e.SetFlag(".fail-list", true); err != nil {
			t.Fatal(err)
		}
		insts, err := DetectAllInventory(context.Background(), codexReq)
		if err != nil {
			t.Fatalf("DetectAll should swallow errors and return nil error: %v", err)
		}
		if len(insts) != 0 {
			t.Fatalf("expected empty inventory, got %d", len(insts))
		}
		t.Log("evidence: DetectAll returned empty inventory + nil error while npm was failing hard")
	})
}

// TestUninstallWhenAbsentIsAccepted：卸载前探测确认未安装，视为已满足（幂等）。
func TestUninstallWhenAbsentIsAccepted(t *testing.T) {
	newEnv(t)
	out, err := newEngine().Uninstall(context.Background(), codexUnreq)
	if err != nil {
		t.Fatalf("uninstall of absent instance must be idempotent success: %v", err)
	}
	if out.VerifiedVersion != "" {
		t.Fatalf("uninstall outcome must carry no version, got %q", out.VerifiedVersion)
	}
}

// TestUninstallSurfacesCommandFailure：卸载命令失败必须显式报错，且目标
// 实例仍在（未达到“消失”就不算成功）。
func TestUninstallSurfacesCommandFailure(t *testing.T) {
	e := newEnv(t)
	if err := e.Publish("@openai/codex", "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
		t.Fatal(err)
	}
	if err := e.InstallDirect(e.Prefix, "@openai/codex@0.44.0"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetFlag(".fail-uninstall", true); err != nil {
		t.Fatal(err)
	}
	if _, err := newEngine().Uninstall(context.Background(), codexUnreq); err == nil {
		t.Fatal("injected uninstall failure must surface as error")
	}
	if err := e.SetFlag(".fail-uninstall", false); err != nil {
		t.Fatal(err)
	}
	det, err := newEngine().Detect(context.Background(), codexReq)
	if err != nil || !det.Installed {
		t.Fatalf("instance must survive failed uninstall, got %+v err=%v", det, err)
	}
}

func withVersion(req Request, v string) Request {
	req.Version = v
	return req
}
