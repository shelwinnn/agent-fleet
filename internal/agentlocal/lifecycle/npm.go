package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/kevinelliott/agentmanager/pkg/agent"
	"github.com/kevinelliott/agentmanager/pkg/catalog"
	"github.com/kevinelliott/agentmanager/pkg/detector"
	"github.com/kevinelliott/agentmanager/pkg/installer"
	"github.com/kevinelliott/agentmanager/pkg/platform"
)

// DefaultOpTimeout 是单个生命周期操作的默认超时上限。上游包不在命令上设
// deadline，超时必须由接入方沿 context 提供（上游研究笔记的明确要求）。
const DefaultOpTimeout = 10 * time.Minute

// exactVersionRe 只接受精确版本：x.y.z 可带 -prerelease / +build。
// 拒绝 v 前缀、range、latest、*、不完整三段式。
var exactVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

// NpmEngine 是基于 agentmanager v1.5.0 npm provider 的窄适配实现。
//
// 对上游的三处收窄（均有隔离环境运行证据，见 cmd/agentmanager-verify 与
// docs/KM-46-agentmanager-接入核查报告.md）：
//
//  1. 固定版本：上游 Manager.Install 原样执行 method.Package，把
//     "pkg@x.y.z" 放进 Package 即得精确安装；Update 没有目标版本参数
//     （catalog 的 update_cmd 是 `npm update -g <pkg>`，永远取 latest），
//     因此 UpgradeToExact 走 Install 入口、绝不委托 Update。
//  2. 版本核验：带版本 spec 时上游安装后读版本恒为零值（npm list 以
//     spec 为参数检索，命中串 "pkg@ver@" 不会出现）——上游 Result.Version
//     一律不采信，操作后用 detector 重新发现并核验。
//  3. 失败路径：检测用可传播错误的 DetectByMethod 入口，零值或不可解析
//     的版本一律判失败；DetectAll 吞错返回空 inventory，不用于判定。
type NpmEngine struct {
	mgr  *installer.Manager
	det  *detector.Detector
	plat platform.Platform

	// OpTimeout 是单操作超时上限；零值取 DefaultOpTimeout。
	OpTimeout time.Duration
}

// NewNpmEngine 创建引擎；plat 为 nil 时用 platform.Current()。
func NewNpmEngine(plat platform.Platform) *NpmEngine {
	if plat == nil {
		plat = platform.Current()
	}
	return &NpmEngine{
		mgr:  installer.NewManager(plat),
		det:  detector.New(plat),
		plat: plat,
	}
}

// Detect 实现.Engine。
func (e *NpmEngine) Detect(ctx context.Context, req Request) (Detected, error) {
	ctx, cancel := e.opCtx(ctx)
	defer cancel()
	inst, err := e.detectNpm(ctx, npmAgentDef(req, false))
	if err != nil {
		return Detected{}, err
	}
	if inst == nil {
		return Detected{Installed: false}, nil
	}
	return Detected{
		Installed:      true,
		Version:        inst.InstalledVersion.Raw,
		Method:         string(agent.MethodNPM),
		ExecutablePath: inst.ExecutablePath,
	}, nil
}

// InstallExact 实现.Engine。上游命令返回后必须核验实际版本与请求一致，
// 否则整体失败——命令成功不等于安装成功。
func (e *NpmEngine) InstallExact(ctx context.Context, req Request) (Outcome, error) {
	return e.installExact(ctx, req, OpInstall, "install")
}

// UpgradeToExact 实现.Engine。语义与 InstallExact 相同（npm 的带版本全局
// 安装会替换既有版本，向下亦然——降级证据见 engine_test）；升级失败的典型
// 形态是“命令成功但版本没变”，由核验步骤兜底。
func (e *NpmEngine) UpgradeToExact(ctx context.Context, req Request) (Outcome, error) {
	return e.installExact(ctx, req, OpUpgrade, "upgrade")
}

// installExact 是两条入口的共享实现：带版本 spec 走上游 Install 入口，
// 之后独立核验（不采信上游 Result.Version）。errVerb 用于错误信息动词。
func (e *NpmEngine) installExact(ctx context.Context, req Request, op Op, errVerb string) (Outcome, error) {
	if err := validateRequest(req); err != nil {
		return Outcome{}, err
	}
	ctx, cancel := e.opCtx(ctx)
	defer cancel()
	ad := npmAgentDef(req, true)
	out := Outcome{Op: op, AgentID: req.AgentID, Package: req.Package, RequestedVersion: req.Version}
	res, err := e.mgr.Install(ctx, ad, ad.InstallMethods["npm"], false)
	if err != nil {
		return Outcome{}, fmt.Errorf("lifecycle: %s %s@%s: %w", errVerb, req.Package, req.Version, err)
	}
	out.UpstreamOutput, out.ExecutablePath = res.Output, res.ExecutablePath
	v, err := e.verifyVersion(ctx, req)
	if err != nil {
		return Outcome{}, err
	}
	out.VerifiedVersion = v
	return out, nil
}

// Uninstall 实现.Engine。method.Package 用裸包名（带 spec 的卸载语义依赖
// npm 版本，不必要）；卸载后核验实例确已消失。卸载前已不存在视为幂等满足。
func (e *NpmEngine) Uninstall(ctx context.Context, req UninstallRequest) (Outcome, error) {
	if req.Package == "" {
		return Outcome{}, errors.New("lifecycle: package is required")
	}
	ctx, cancel := e.opCtx(ctx)
	defer cancel()
	ad := npmAgentDef(Request{
		AgentID: req.AgentID, Package: req.Package,
		Executables: req.Executables, Platforms: req.Platforms,
	}, false)
	out := Outcome{Op: OpUninstall, AgentID: req.AgentID, Package: req.Package}
	before, err := e.detectNpm(ctx, ad)
	if err != nil {
		return Outcome{}, fmt.Errorf("lifecycle: pre-uninstall detect %s: %w", req.Package, err)
	}
	if before != nil {
		if err := e.mgr.Uninstall(ctx, before, ad.InstallMethods["npm"]); err != nil {
			return Outcome{}, fmt.Errorf("lifecycle: uninstall %s: %w", req.Package, err)
		}
	}
	after, err := e.detectNpm(ctx, ad)
	if err != nil {
		return Outcome{}, fmt.Errorf("lifecycle: post-uninstall verify %s: %w", req.Package, err)
	}
	if after != nil {
		return Outcome{}, fmt.Errorf("lifecycle: post-uninstall verify: %s@%s still present after uninstall",
			req.Package, after.InstalledVersion.Raw)
	}
	return out, nil
}

// detectNpm 用上游可传播错误的入口重新发现目标实例；空结果返回 (nil, nil)。
// 零值或不可解析版本一律报错：探测失败不能被包装成“已安装但版本未知”。
func (e *NpmEngine) detectNpm(ctx context.Context, ad catalog.AgentDef) (*agent.Installation, error) {
	insts, err := e.det.DetectByMethod(ctx, agent.MethodNPM, []catalog.AgentDef{ad})
	if err != nil {
		return nil, fmt.Errorf("lifecycle: npm detection for %s: %w", ad.ID, err)
	}
	switch len(insts) {
	case 0:
		return nil, nil
	case 1:
	default:
		return nil, fmt.Errorf("lifecycle: agent %q reports %d npm installations, expected at most one", ad.ID, len(insts))
	}
	inst := insts[0]
	v := inst.InstalledVersion
	if v.IsZero() || !exactVersionRe.MatchString(v.Raw) {
		return nil, fmt.Errorf("lifecycle: agent %q: installed version %q is not a readable exact version; "+
			"refusing to treat zero or unparsed values as success", ad.ID, v.Raw)
	}
	return inst, nil
}

func (e *NpmEngine) verifyVersion(ctx context.Context, req Request) (string, error) {
	inst, err := e.detectNpm(ctx, npmAgentDef(req, false))
	if err != nil {
		return "", fmt.Errorf("lifecycle: post-op verify %s@%s: %w", req.Package, req.Version, err)
	}
	if inst == nil {
		return "", fmt.Errorf("lifecycle: post-op verify %s@%s: instance absent after successful command",
			req.Package, req.Version)
	}
	if got := inst.InstalledVersion.Raw; got != req.Version {
		return "", fmt.Errorf("lifecycle: post-op verify %s: installed %s, want %s", req.Package, got, req.Version)
	}
	return inst.InstalledVersion.Raw, nil
}

func (e *NpmEngine) opCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	limit := e.OpTimeout
	if limit == 0 {
		limit = DefaultOpTimeout
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= limit {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, limit)
}

func validateRequest(req Request) error {
	if req.Package == "" {
		return errors.New("lifecycle: package is required")
	}
	if !exactVersionRe.MatchString(req.Version) {
		return fmt.Errorf("lifecycle: version %q is not an exact x.y.z version; ranges and tags are rejected", req.Version)
	}
	return nil
}

// npmAgentDef 构造上游需要的 catalog 定义。pinned 时把固定版本 spec 写入
// method.Package（上游 Install 原样执行该字段）；核验/卸载用裸包名。
func npmAgentDef(req Request, pinned bool) catalog.AgentDef {
	spec := req.Package
	if pinned {
		spec = req.Package + "@" + req.Version
	}
	return catalog.AgentDef{
		ID:   req.AgentID,
		Name: req.AgentID,
		InstallMethods: map[string]catalog.InstallMethodDef{
			"npm": {
				Method:    "npm",
				Package:   spec,
				Command:   "npm install -g " + spec,
				Platforms: req.Platforms,
			},
		},
		Detection: catalog.DetectionDef{Executables: req.Executables},
	}
}
