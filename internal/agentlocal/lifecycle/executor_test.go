package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// fakeEngine 是编排层测试的引擎替身：记录调用并按脚本应答。
type fakeEngine struct {
	detect     func(ctx context.Context, req Request) (Detected, error)
	install    func(ctx context.Context, req Request) (Outcome, error)
	upgrade    func(ctx context.Context, req Request) (Outcome, error)
	uninstall  func(ctx context.Context, req UninstallRequest) (Outcome, error)
	calls      []string
	lastCtx    context.Context
	cancelSeen bool
}

func (e *fakeEngine) Detect(ctx context.Context, req Request) (Detected, error) {
	e.calls = append(e.calls, "detect")
	e.lastCtx = ctx
	if e.detect == nil {
		return Detected{Installed: false}, nil
	}
	return e.detect(ctx, req)
}

func (e *fakeEngine) InstallExact(ctx context.Context, req Request) (Outcome, error) {
	e.calls = append(e.calls, "install")
	e.lastCtx = ctx
	if e.install == nil {
		return Outcome{Op: OpInstall, VerifiedVersion: req.Version}, nil
	}
	return e.install(ctx, req)
}

func (e *fakeEngine) UpgradeToExact(ctx context.Context, req Request) (Outcome, error) {
	e.calls = append(e.calls, "upgrade")
	e.lastCtx = ctx
	if e.upgrade == nil {
		return Outcome{Op: OpUpgrade, VerifiedVersion: req.Version}, nil
	}
	return e.upgrade(ctx, req)
}

func (e *fakeEngine) Uninstall(ctx context.Context, req UninstallRequest) (Outcome, error) {
	e.calls = append(e.calls, "uninstall")
	e.lastCtx = ctx
	if e.uninstall == nil {
		return Outcome{Op: OpUninstall}, nil
	}
	return e.uninstall(ctx, req)
}

// allowPolicy 放行一切组合（生产路径用 NewRegistryPolicy，见专用测试）。
type allowPolicy struct{}

func (allowPolicy) Check(string, string, string) error { return nil }

func execWith(eng Engine) *NodeExecutor {
	return NewNodeExecutor(NodeExecutorConfig{
		Engines: map[string]Engine{"npm": eng},
		Policy:  allowPolicy{},
		Timeout: 30 * time.Second,
	})
}

func installTarget() TargetSpec {
	return TargetSpec{Machine: "m1", Type: domain.LifecycleOpInstall, User: "alice",
		Agent: "codex", Method: "npm", Package: "@openai/codex", Version: "0.44.0"}
}

// TestExecuteInstallSucceedsWithVerifiedEvidence：成功路径的证据必须来自引擎
// 的独立核验值（不是上游返回值），状态/原因落 domain 白名单。
func TestExecuteInstallSucceedsWithVerifiedEvidence(t *testing.T) {
	eng := &fakeEngine{install: func(_ context.Context, req Request) (Outcome, error) {
		return Outcome{Op: OpInstall, VerifiedVersion: req.Version, ExecutablePath: "/usr/local/bin/codex"}, nil
	}}
	rep := execWith(eng).Execute(context.Background(), OpRequest{
		OpID: "op-1", Type: domain.LifecycleOpInstall, Version: "0.44.0",
		Targets: []TargetSpec{installTarget()},
	})
	if len(rep.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(rep.Results))
	}
	r := rep.Results[0]
	if r.State != domain.TargetSucceeded || r.Reason != domain.ReasonVerifiedPresent {
		t.Fatalf("state/reason = %s/%s, want succeeded/verified-present", r.State, r.Reason)
	}
	if r.Evidence.ObservedVersion != "0.44.0" || r.Evidence.ExecutablePath != "/usr/local/bin/codex" {
		t.Fatalf("evidence = %+v", r.Evidence)
	}
	if r.Evidence.Method != "npm" {
		t.Fatalf("evidence method = %q", r.Evidence.Method)
	}
	if err := domain.ValidateTargetResult(r.State, r.Reason); err != nil {
		t.Fatalf("result violates domain state machine: %v", err)
	}
}

// TestExecuteEngineErrorIsFailedNotSuccess：引擎错误必须如实落 failed(op-failed)，
// 绝不降级为成功；错误文本脱敏且限长。
func TestExecuteEngineErrorIsFailedNotSuccess(t *testing.T) {
	eng := &fakeEngine{install: func(_ context.Context, _ Request) (Outcome, error) {
		return Outcome{}, errors.New("npm ERR! _authToken=super-secret-value leaked")
	}}
	rep := execWith(eng).Execute(context.Background(), OpRequest{
		OpID: "op-1", Type: domain.LifecycleOpInstall, Version: "0.44.0",
		Targets: []TargetSpec{installTarget()},
	})
	r := rep.Results[0]
	if r.State != domain.TargetFailed || r.Reason != domain.ReasonOpFailed {
		t.Fatalf("state/reason = %s/%s, want failed/op-failed", r.State, r.Reason)
	}
	if strings.Contains(r.Message, "super-secret-value") {
		t.Fatalf("sensitive value leaked into message: %q", r.Message)
	}
	if !strings.Contains(r.Message, "[redacted]") {
		t.Fatalf("expected redaction marker in %q", r.Message)
	}
	if len(r.Message) > 512+len("…(truncated)") {
		t.Fatalf("message not truncated: %d chars", len(r.Message))
	}
}

// TestExecuteRefusesBlockedAndUnverifiedCombos：生产准入（NewRegistryPolicy）
// 必须在执行前拒绝未验证与阻塞组合——引擎一次都不能被调用（禁止静默替换），
// 且拒绝原因是 unsupported-combo。
func TestExecuteRefusesBlockedAndUnverifiedCombos(t *testing.T) {
	eng := &fakeEngine{}
	x := NewNodeExecutor(NodeExecutorConfig{Engines: map[string]Engine{"npm": eng, "brew": eng}})
	cases := []struct {
		name   string
		method string
		os     string // 注入的"运行时平台"经由 target 快照与 policy 双闸
		arch   string
	}{
		{"windows npm (blocked-pending-windows)", "npm", "windows", "amd64"},
		{"linux npm (unverified-isolated)", "npm", "linux", "amd64"},
		{"darwin arm64 npm (unverified)", "npm", "darwin", "arm64"},
		{"binary channel (blocked upstream defect)", "binary", "linux", "amd64"},
	}
	// platformOverride 让 policy.Check 看到目标平台：通过替换 runtime 不可行，
	// 直接断言 policy 层 + 用引擎调用计数证明"执行前拒绝"。
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := NewRegistryPolicy()
			err := policy.Check(tc.os, tc.arch, tc.method)
			if err == nil {
				t.Fatalf("combo %s/%s+%s must be refused by production registry policy", tc.os, tc.arch, tc.method)
			}
			if !strings.Contains(err.Error(), "refused before execution") {
				t.Fatalf("refusal should explain itself: %v", err)
			}
		})
	}
	// 平台未验证组合到达节点时（ledger 快照一致），执行引擎零调用：
	rep := x.Execute(context.Background(), OpRequest{
		OpID: "op-1", Type: domain.LifecycleOpInstall, Version: "0.44.0",
		Targets: []TargetSpec{installTarget()},
	})
	// 测试运行平台的真实组合（linux/amd64 = unverified）被生产 policy 拒绝：
	if rep.Results[0].State != domain.TargetFailed || rep.Results[0].Reason != domain.ReasonUnsupportedCombo {
		t.Fatalf("unverified combo must be refused at execution: %+v", rep.Results[0])
	}
	if len(eng.calls) != 0 {
		t.Fatalf("engine must not be called for refused combos, got %v", eng.calls)
	}
}

// TestExecuteRefusesPlatformMismatch：台账快照与运行时平台不一致的目标拒绝执行。
func TestExecuteRefusesPlatformMismatch(t *testing.T) {
	eng := &fakeEngine{}
	x := execWith(eng)
	tt := installTarget()
	tt.OS, tt.Arch = "darwin", "arm64" // 与测试运行时（linux/amd64）不符
	rep := x.Execute(context.Background(), OpRequest{
		OpID: "op-1", Type: domain.LifecycleOpInstall, Version: "0.44.0", Targets: []TargetSpec{tt},
	})
	r := rep.Results[0]
	if r.Reason != domain.ReasonUnsupportedCombo || !strings.Contains(r.Message, "does not match runtime") {
		t.Fatalf("platform mismatch must be refused with reason: %+v", r)
	}
	if len(eng.calls) != 0 {
		t.Fatalf("engine called despite mismatch: %v", eng.calls)
	}
}

// TestExecuteRefusesUnknownMethodAndEnv：无引擎适配的安装方式与异构执行环境
//（如 wsl 目标打到 native agentd）一律执行前拒绝。
func TestExecuteRefusesUnknownMethodAndEnv(t *testing.T) {
	eng := &fakeEngine{}
	x := execWith(eng)
	unknown := installTarget()
	unknown.Method = "scoop"
	wsl := installTarget()
	wsl.Env = "wsl:ubuntu"

	rep := x.Execute(context.Background(), OpRequest{
		OpID: "op-1", Type: domain.LifecycleOpInstall, Version: "0.44.0",
		Targets: []TargetSpec{unknown, wsl},
	})
	for i, want := range []string{"no lifecycle engine adapter", "does not match this executor env"} {
		r := rep.Results[i]
		if r.State != domain.TargetFailed || r.Reason != domain.ReasonUnsupportedCombo ||
			!strings.Contains(r.Message, want) {
			t.Fatalf("case %d: %+v, want refused with %q", i, r, want)
		}
	}
	if len(eng.calls) != 0 {
		t.Fatalf("engine called: %v", eng.calls)
	}
}

// TestUninstallRequiresIdentifiedSource：来源未识别的卸载在执行前拒绝（防绕过：
// 创建层已强制，执行层再闸一道）。
func TestUninstallRequiresIdentifiedSource(t *testing.T) {
	eng := &fakeEngine{}
	x := execWith(eng)
	tt := installTarget()
	tt.Type = domain.LifecycleOpUninstall
	tt.Method = "" // 来源未识别
	rep := x.Execute(context.Background(), OpRequest{
		OpID: "op-1", Type: domain.LifecycleOpUninstall, Targets: []TargetSpec{tt},
	})
	r := rep.Results[0]
	if r.Reason != domain.ReasonUnsupportedCombo || !strings.Contains(r.Message, "identified source") {
		t.Fatalf("uninstall without source must be refused: %+v", r)
	}
	if len(eng.calls) != 0 {
		t.Fatalf("engine called: %v", eng.calls)
	}
}

// TestRetryRediscoveryPaths：重试首派的重发现闸——
//   - 已在固定版本 → succeeded（意图已被现实满足，不重复执行）；
//   - 在其他版本 → needs_reconfirmation（影响变化，操作者重新确认前不执行）；
//   - 已缺席（install）→ 正常执行；
//   - 卸载目标已缺席 → succeeded(verified-absent)。
func TestRetryRediscoveryPaths(t *testing.T) {
	t.Run("already at fixed version", func(t *testing.T) {
		eng := &fakeEngine{detect: func(_ context.Context, _ Request) (Detected, error) {
			return Detected{Installed: true, Version: "0.44.0", Method: "npm"}, nil
		}}
		rep := execWith(eng).Execute(context.Background(), OpRequest{
			OpID: "op-r1", Type: domain.LifecycleOpUpgrade, Version: "0.44.0",
			RediscoveryRequired: true, Targets: []TargetSpec{upgradeTarget()},
		})
		r := rep.Results[0]
		if r.State != domain.TargetSucceeded || r.Reason != domain.ReasonVerifiedPresent {
			t.Fatalf("state/reason = %s/%s, want succeeded/verified-present", r.State, r.Reason)
		}
		for _, c := range eng.calls {
			if c == "upgrade" {
				t.Fatalf("upgrade must not re-run when already at fixed version: %v", eng.calls)
			}
		}
		if rep.NeedsReconfirmation {
			t.Fatal("already-satisfied intent must not require reconfirmation")
		}
	})
	t.Run("impact changed requires reconfirmation", func(t *testing.T) {
		eng := &fakeEngine{detect: func(_ context.Context, _ Request) (Detected, error) {
			return Detected{Installed: true, Version: "0.45.0", Method: "npm"}, nil
		}}
		rep := execWith(eng).Execute(context.Background(), OpRequest{
			OpID: "op-r2", Type: domain.LifecycleOpUpgrade, Version: "0.44.0",
			RediscoveryRequired: true, Targets: []TargetSpec{upgradeTarget()},
		})
		r := rep.Results[0]
		// 逐目标以 failed(version-mismatch) 落定（本尝试未执行、未变更任何东西）；
		// 操作级 needs_reconfirmation 由控制面呈现给操作者，继续走新重试操作。
		if r.State != domain.TargetFailed || r.Reason != domain.ReasonVersionMismatch {
			t.Fatalf("state/reason = %s/%s, want failed/version-mismatch", r.State, r.Reason)
		}
		if err := domain.ValidateTargetResult(r.State, r.Reason); err != nil {
			t.Fatalf("result violates domain state machine: %v", err)
		}
		if !rep.NeedsReconfirmation || rep.ReconfirmReason == "" {
			t.Fatalf("needsReconfirmation/reason = %v/%q", rep.NeedsReconfirmation, rep.ReconfirmReason)
		}
		if !strings.Contains(rep.ReconfirmReason, "0.45.0") || !strings.Contains(rep.ReconfirmReason, "0.44.0") {
			t.Fatalf("reconfirm reason must name both versions: %q", rep.ReconfirmReason)
		}
		for _, c := range eng.calls {
			if c == "upgrade" {
				t.Fatalf("must not execute before reconfirmation: %v", eng.calls)
			}
		}
	})
	t.Run("absent proceeds to execute", func(t *testing.T) {
		eng := &fakeEngine{}
		rep := execWith(eng).Execute(context.Background(), OpRequest{
			OpID: "op-r3", Type: domain.LifecycleOpUpgrade, Version: "0.44.0",
			RediscoveryRequired: true, Targets: []TargetSpec{upgradeTarget()},
		})
		if rep.Results[0].State != domain.TargetSucceeded {
			t.Fatalf("state = %s, want succeeded", rep.Results[0].State)
		}
		if len(eng.calls) != 2 { // detect + upgrade
			t.Fatalf("calls = %v, want detect+upgrade", eng.calls)
		}
	})
	t.Run("uninstall already absent", func(t *testing.T) {
		eng := &fakeEngine{detect: func(_ context.Context, _ Request) (Detected, error) {
			return Detected{Installed: false}, nil
		}}
		rep := execWith(eng).Execute(context.Background(), OpRequest{
			OpID: "op-r4", Type: domain.LifecycleOpUninstall,
			RediscoveryRequired: true, Targets: []TargetSpec{uninstallTarget()},
		})
		r := rep.Results[0]
		if r.State != domain.TargetSucceeded || r.Reason != domain.ReasonVerifiedAbsent {
			t.Fatalf("state/reason = %s/%s, want succeeded/verified-absent", r.State, r.Reason)
		}
		for _, c := range eng.calls {
			if c == "uninstall" {
				t.Fatalf("uninstall must not re-run when already absent: %v", eng.calls)
			}
		}
	})
	t.Run("detect failure is discovery-failed", func(t *testing.T) {
		eng := &fakeEngine{detect: func(_ context.Context, _ Request) (Detected, error) {
			return Detected{}, errors.New("npm list failed")
		}}
		rep := execWith(eng).Execute(context.Background(), OpRequest{
			OpID: "op-r5", Type: domain.LifecycleOpUpgrade, Version: "0.44.0",
			RediscoveryRequired: true, Targets: []TargetSpec{upgradeTarget()},
		})
		r := rep.Results[0]
		if r.State != domain.TargetFailed || r.Reason != domain.ReasonDiscoveryFailed {
			t.Fatalf("state/reason = %s/%s, want failed/discovery-failed", r.State, r.Reason)
		}
	})
}

// TestFirstDispatchSkipsRediscovery：非重试操作不重发现（首派直接执行，
// 执行后核验由引擎承担）。
func TestFirstDispatchSkipsRediscovery(t *testing.T) {
	eng := &fakeEngine{}
	rep := execWith(eng).Execute(context.Background(), OpRequest{
		OpID: "op-1", Type: domain.LifecycleOpInstall, Version: "0.44.0",
		Targets: []TargetSpec{installTarget()},
	})
	if rep.Results[0].State != domain.TargetSucceeded {
		t.Fatalf("state = %s", rep.Results[0].State)
	}
	for _, c := range eng.calls {
		if c == "detect" {
			t.Fatalf("first dispatch must not pre-detect: %v", eng.calls)
		}
	}
}

// TestTargetDeadlinePropagation：控制面 deadline 早于单目标上限时，目标 context
// 必须继承更早的 deadline（外部执行超时沿调用链传播）。
func TestTargetDeadlinePropagation(t *testing.T) {
	eng := &fakeEngine{install: func(ctx context.Context, _ Request) (Outcome, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			return Outcome{}, errors.New("no deadline on engine ctx")
		}
		if time.Until(dl) > 20*time.Second {
			return Outcome{}, fmt.Errorf("deadline too far: %v", time.Until(dl))
		}
		return Outcome{VerifiedVersion: "0.44.0"}, nil
	}}
	deadline := time.Now().Add(10 * time.Second)
	rep := execWith(eng).Execute(context.Background(), OpRequest{
		OpID: "op-1", Type: domain.LifecycleOpInstall, Version: "0.44.0",
		Deadline: deadline, Targets: []TargetSpec{installTarget()},
	})
	if rep.Results[0].State != domain.TargetSucceeded {
		t.Fatalf("deadline must propagate to engine ctx: %+v", rep.Results[0])
	}
}

// TestNodeExecutorDefaultDeadline：未携带整单 deadline 时，目标 context 仍必须
// 有单目标超时上限（DefaultOpTimeout）——外部执行不允许无界等待。
func TestNodeExecutorDefaultDeadline(t *testing.T) {
	eng := &fakeEngine{install: func(ctx context.Context, _ Request) (Outcome, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			return Outcome{}, errors.New("no deadline on engine ctx")
		}
		if d := time.Until(dl); d <= 0 || d > DefaultOpTimeout {
			return Outcome{}, fmt.Errorf("deadline %v outside (0,%v]", d, DefaultOpTimeout)
		}
		return Outcome{VerifiedVersion: "0.44.0"}, nil
	}}
	rep := execWith(eng).Execute(context.Background(), OpRequest{
		OpID: "op-1", Type: domain.LifecycleOpInstall, Version: "0.44.0",
		Targets: []TargetSpec{installTarget()},
	})
	if rep.Results[0].State != domain.TargetSucceeded {
		t.Fatalf("default deadline missing: %+v", rep.Results[0])
	}
}

func upgradeTarget() TargetSpec {
	t := installTarget()
	t.Type = domain.LifecycleOpUpgrade
	return t
}

func uninstallTarget() TargetSpec {
	t := installTarget()
	t.Type = domain.LifecycleOpUninstall
	t.Version = ""
	return t
}
