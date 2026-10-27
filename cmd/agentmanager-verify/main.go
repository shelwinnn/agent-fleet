// agentmanager-verify 是 KM-46 的最小验证程序：在自建的隔离环境（临时
// HOME、受控注册表替身、fake npm）里驱动 agentmanager v1.5.0 走完
// 探测 → 固定版本安装 → 固定版本升级/降级 → 上游 Update 漂移取证 →
// 卸载隔离 → 探测失败路径 → 用户数据保护对比，逐项输出 PASS/FAIL 与证据。
//
// 安全边界（KM-46 全局约束）：
//   - 不在真实主机安装/卸载任何软件。PATH 首位是被替换的 fake npm，
//     上游构造的 npm 命令只会命中替身；真实 npm 即使存在也不会被调用。
//   - HOME 指向临时目录，~/.codex 是测试替身；数据保护用全 HOME 快照
//     前后对比证明“零变更”。
//   - 运行证据针对 fake npm 替身取得（替身语义对齐 npm 公开文档行为）；
//     真实 npm 与真机（Windows 原生/macOS/Linux 实装）行为属后续验收，
//     报告中一律标注“未验证”。
//
// 运行：go run ./cmd/agentmanager-verify   （退出码 0 = 全部通过）
package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/kevinelliott/agentmanager/pkg/catalog"
	"github.com/kevinelliott/agentmanager/pkg/installer"
	"github.com/kevinelliott/agentmanager/pkg/platform"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/lifecycle"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/lifecycle/testenv"
)

const codexPkg = "@openai/codex"

var codexReq = lifecycle.Request{
	AgentID:     "codex",
	Package:     codexPkg,
	Executables: []string{"codex"},
	Platforms:   []string{"linux", "darwin", "windows"},
}

// verifier 顺序执行检查项并累计失败数；输出即证据，结构保持线性可读。
type verifier struct {
	failed int
}

type check struct {
	name string
	run  func() (string, error)
}

func (v *verifier) runChecks(checks []check) {
	for _, c := range checks {
		detail, err := c.run()
		if err != nil {
			v.failed++
			fmt.Printf("[FAIL] %s\n       %v\n", c.name, err)
			continue
		}
		fmt.Printf("[PASS] %s\n       %s\n", c.name, detail)
	}
}

func main() {
	failed, err := run(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: fatal: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("\nSUMMARY: %d check(s) failed\n", failed)
	if failed != 0 {
		fmt.Println("RESULT: NO-GO（隔离环境存在未通过项，见上方证据）")
		os.Exit(1)
	}
	fmt.Println("RESULT: 隔离环境全部通过（GO 倾向）。真机（Windows 原生/macOS/Linux 实装）仍属未验证，范围见核查报告。")
}

func run(ctx context.Context) (int, error) {
	fmt.Println("[KM-46] agentmanager 接入核查 · 最小验证程序")
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/kevinelliott/agentmanager" {
				fmt.Printf("pinned dependency: %s %s\n", d.Path, d.Version)
			}
		}
	}

	root, err := os.MkdirTemp("", "km46-verify-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(root)
	e, err := testenv.New(root)
	if err != nil {
		return 0, err
	}
	home, prefix, registry, path := e.Environ()
	for _, kv := range [][2]string{
		{"HOME", home}, {"NPM_PREFIX", prefix},
		{"FAKE_NPM_REGISTRY", registry}, {"PATH", path},
	} {
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			return 0, err
		}
	}
	for _, spec := range []struct{ name, ver, bins, out string }{
		{codexPkg, "0.44.0", "codex", "codex-cli 0.44.0"},
		{codexPkg, "0.45.0", "codex", "codex-cli 0.45.0"},
		{"other-agent", "1.0.0", "other-agent", "other-agent 1.0.0"},
	} {
		if err := e.Publish(spec.name, spec.ver, spec.bins, spec.out); err != nil {
			return 0, err
		}
	}
	if err := e.SeedCodexData(); err != nil {
		return 0, err
	}
	// 第二前缀布置同名同版本的另一安装实例（C5 的“其他实例”）。
	if err := e.InstallDirect(e.Prefix2, codexPkg+"@0.44.0"); err != nil {
		return 0, err
	}
	eng := lifecycle.NewNpmEngine(nil)
	v := &verifier{}

	// C0 取证在数据保护基线**之前**执行：上游 DetectAll 的 pip 策略会在
	// 宿主装有 uv/pip/pipx 时调用它们，uv 会向 $HOME 写 .cache/uv——这
	// 本身就是“DetectAll 不能用于生命周期判定”的副作用证据，同时避免污
	// 染 C7 的零变更对比。
	v.runChecks([]check{{
		name: "C0 上游 DetectAll 吞错陷阱取证（npm 彻底失败时）",
		run: func() (string, error) {
			if err := e.SetFlag(".fail-list", true); err != nil {
				return "", err
			}
			defer warnReset(func() error { return e.SetFlag(".fail-list", false) })
			insts, err := lifecycle.DetectAllInventory(ctx, codexReq)
			if err != nil {
				return "", fmt.Errorf("DetectAll 意外返回错误: %w", err)
			}
			if len(insts) != 0 {
				return "", fmt.Errorf("期望空 inventory，得到 %d 项", len(insts))
			}
			detail := "DetectAll=(空 inventory, nil error)；引擎不使用 DetectAll 做判定"
			if n := countEntries(filepath.Join(e.Home, ".cache", "uv")); n > 0 {
				detail += fmt.Sprintf("；副作用：pip 检测策略执行了宿主 uv，$HOME/.cache/uv 新增 %d 项（宿主相关，非生命周期操作）", n)
			}
			return detail, nil
		},
	}})

	// 数据保护基线：整个临时 HOME（含 ~/.codex 替身与 shared 目录）。
	homeBefore, err := lifecycle.SnapshotDir(e.Home)
	if err != nil {
		return v.failed, err
	}

	v.runChecks([]check{
		{"C1 未安装探测：空环境不得报已安装", func() (string, error) {
			det, err := eng.Detect(ctx, codexReq)
			if err != nil {
				return "", err
			}
			if det.Installed {
				return "", fmt.Errorf("empty prefix reported as installed: %+v", det)
			}
			return `installed=false, version=""`, nil
		}},
		{"C2 固定版本安装 0.44.0 + 独立核验", func() (string, error) {
			out, err := eng.InstallExact(ctx, withVersion(codexReq, "0.44.0"))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("verified=%s exe=%q upstream-stdout=%dB",
				out.VerifiedVersion, out.ExecutablePath, len(out.UpstreamOutput)), nil
		}},
		{"C2b 上游 Result.Version 零值陷阱（带版本 spec 安装后恒为零值）", func() (string, error) {
			// 用 raw Manager 再装一个 other-agent@1.0.0：上游读版本用
			// `npm list -g other-agent@1.0.0`，命中串 "spec@" 不出现 → 零值。
			mgr := installer.NewManager(platform.Current())
			ad := catalog.AgentDef{
				ID:   "other-agent",
				Name: "other-agent",
				InstallMethods: map[string]catalog.InstallMethodDef{
					"npm": {Method: "npm", Package: "other-agent@1.0.0",
						Command: "npm install -g other-agent@1.0.0"},
				},
				Detection: catalog.DetectionDef{Executables: []string{"other-agent"}},
			}
			res, err := mgr.Install(ctx, ad, ad.InstallMethods["npm"], false)
			if err != nil {
				return "", err
			}
			if !res.Version.IsZero() {
				return "", fmt.Errorf("expected zero Result.Version for pinned-spec install, got %+v", res.Version)
			}
			det, err := eng.Detect(ctx, lifecycle.Request{AgentID: "other-agent", Package: "other-agent",
				Executables: []string{"other-agent"}, Platforms: []string{"linux"}})
			if err != nil || !det.Installed || det.Version != "1.0.0" {
				return "", fmt.Errorf("strict detect must read 1.0.0 independently, got %+v err=%v", det, err)
			}
			return "upstream Result.Version=0 (zero), strict detect=1.0.0 → 零值不得当成功，引擎独立核验成立", nil
		}},
		{"C3 固定版本升级 0.44.0 → 0.45.0", func() (string, error) {
			out, err := eng.UpgradeToExact(ctx, withVersion(codexReq, "0.45.0"))
			if err != nil {
				return "", err
			}
			if out.VerifiedVersion != "0.45.0" {
				return "", fmt.Errorf("verified=%s", out.VerifiedVersion)
			}
			return "verified=0.45.0", nil
		}},
		{"C3b 固定版本降级 0.45.0 → 0.44.0（latest=0.45.0 时仍落在 0.44.0）", func() (string, error) {
			out, err := eng.UpgradeToExact(ctx, withVersion(codexReq, "0.44.0"))
			if err != nil {
				return "", err
			}
			if out.VerifiedVersion != "0.44.0" {
				return "", fmt.Errorf("verified=%s, want 0.44.0（证明走固定版本而非 latest）", out.VerifiedVersion)
			}
			return "verified=0.44.0（registry latest=0.45.0）→ 固定版本语义成立", nil
		}},
		{"C4 上游 Update 漂移取证：catalog 原生 update_cmd 无法固定目标", func() (string, error) {
			drifted, err := lifecycle.UpstreamUpdateDrift(ctx, codexPkg, codexReq.Executables)
			if err != nil {
				return "", err
			}
			if drifted.VerifiedVersion != "0.45.0" {
				return "", fmt.Errorf("drift landed on %q, want registry latest 0.45.0", drifted.VerifiedVersion)
			}
			return "pinned=0.44.0 在装，Update 后实测 0.45.0 → Update 不可用于固定版本升级（引擎已改走 Install 入口）", nil
		}},
		{"C5 卸载目标实例；同前缀其他包与第二前缀同实例不受影响", func() (string, error) {
			if _, err := eng.Uninstall(ctx, lifecycle.UninstallRequest{
				AgentID: codexReq.AgentID, Package: codexPkg,
				Executables: codexReq.Executables, Platforms: codexReq.Platforms,
			}); err != nil {
				return "", err
			}
			det, err := eng.Detect(ctx, codexReq)
			if err != nil {
				return "", err
			}
			if det.Installed {
				return "", fmt.Errorf("target still present: %+v", det)
			}
			if _, err := os.Stat(filepath.Join(e.Prefix, "bin", "other-agent")); err != nil {
				return "", fmt.Errorf("other-agent must survive: %w", err)
			}
			if err := os.Setenv("NPM_PREFIX", e.Prefix2); err != nil {
				return "", err
			}
			defer warnReset(func() error { return os.Setenv("NPM_PREFIX", e.Prefix) })
			det2, err := eng.Detect(ctx, codexReq)
			if err != nil || !det2.Installed || det2.Version != "0.44.0" {
				return "", fmt.Errorf("second-prefix instance must be intact: %+v err=%v", det2, err)
			}
			return "目标消失；other-agent 保留；第二前缀 codex@0.44.0 原样", nil
		}},
		{"C6 探测失败必须报错且不得谎报已安装", func() (string, error) {
			if err := e.SetFlag(".fail-list", true); err != nil {
				return "", err
			}
			defer warnReset(func() error { return e.SetFlag(".fail-list", false) })
			det, err := eng.Detect(ctx, codexReq)
			if err == nil {
				return "", fmt.Errorf("hard npm failure returned no error: %+v", det)
			}
			if det.Installed || det.Version != "" {
				return "", fmt.Errorf("failed detection fabricated state: %+v", det)
			}
			return "npm 彻底失败时 Detect 返回错误、不产出任何状态", nil
		}},
		{"C7 数据保护：全程 ~/.codex（与整个 HOME）零变更", func() (string, error) {
			homeAfter, err := lifecycle.SnapshotDir(e.Home)
			if err != nil {
				return "", err
			}
			if diffs := homeBefore.Diff(homeAfter); len(diffs) != 0 {
				return "", fmt.Errorf("HOME changed:\n%s", homeBefore.Report(homeAfter))
			}
			return fmt.Sprintf("全 HOME 快照 %d 项，前后一致（文件集合/内容/权限/软链）", len(homeBefore)), nil
		}},
	})

	fmt.Println("\nWindows 原生分析（源码级，真机未验证）：pkg/platform/windows.go 的" +
		"FindExecutable/IsExecutableInPath 只认 .exe——npm shim（npm.cmd/codex.cmd）不在视野内；" +
		"标准 Node for Windows 不带 npm.exe，NPMProvider.IsAvailable 大概率为 false。" +
		"该组合标 BLOCKED-PENDING-WINDOWS，见核查报告。")
	return v.failed, nil
}

func withVersion(req lifecycle.Request, v string) lifecycle.Request {
	req.Version = v
	return req
}

// warnReset 在检查结束后恢复环境；恢复失败打印警告——故障注入残留会让
// 后续检查失真，宁可显式失败也不要静默。
func warnReset(reset func() error) {
	if err := reset(); err != nil {
		fmt.Fprintf(os.Stderr, "verify: WARNING: environment reset failed: %v\n", err)
	}
}

// countEntries 统计目录下条目数（含根）；根不存在返回 0。
func countEntries(root string) int {
	n := 0
	err := filepath.WalkDir(root, func(string, fs.DirEntry, error) error {
		n++
		return nil
	})
	if err != nil {
		return 0
	}
	return n
}
