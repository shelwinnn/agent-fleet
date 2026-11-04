// 节点侧生命周期执行引擎（KM-49；依据 docs/superpowers/specs/2026-10-27-codex-lifecycle-spec.md
// 与 KM-47 的操作契约）。NodeExecutor 把一次生命周期操作（固定版本意图）逐目标执行
// 并在目标环境重新发现、验证实际结果——命令退出码不算数，看到的才算数：
//
//   - 版本只用创建操作时固定的精确版本（执行期间上游出现新版本不改变本次目标）；
//   - 不兼容目标（OS/架构/来源/安装方式未验证或明确不支持）在执行前拒绝并展示原因，
//     禁止静默替换引擎或版本；
//   - 每个目标执行后重新发现并验证实际版本或实例缺席（NpmEngine 内建；编排层只
//     如实映射，不把引擎错误降级为成功）；
//   - 卸载按已识别的安装来源执行，来源未知拒绝；
//   - 重试目标先重新发现：意图已被现实满足的如实上报成功；影响变化（实际版本与
//     固定版本意图前提不一致）上报 needs_reconfirmation，未获重新确认不执行；
//   - 外部执行一律设超时（沿 context 传播，上限 10 分钟——窄适配层纪律）；
//   - 结果与证据不记录敏感值（token 形态的串脱敏 + 限长）。
//
// 数据保护边界：本层与引擎层（npm.go）只允许程序本体变化，绝不触碰 HOME 下的
// Codex 用户数据路径；隔离环境的零变更证明见 dataprotection 与 executor 测试。
package lifecycle

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// NativeEnv 是本执行引擎的执行环境标识（spec：最小目标身份含 OS 环境；
// Windows 原生与 WSL 以不同 Env 区分）。当前 agentd 只代表 native 环境；
// env 归一化规则与 domain.LifecycleTarget.Normalize 一致（空 = native）。
const NativeEnv = "native"

// CapabilityPolicy 是执行前的组合准入判定（验收标准：不兼容目标在执行前拒绝
// 并展示原因）。生产实现是 RegistryPolicy——KM-46 §7 能力矩阵的执行侧落实；
// 测试可注入替身以在隔离环境验证编排逻辑（生产装配没有旁路）。
type CapabilityPolicy interface {
	// Check 返回 nil 表示允许执行；非 nil 错误的文本是给操作者看的原因。
	Check(os, arch, method string) error
}

// NewRegistryPolicy 返回能力矩阵（KM-46 §7 文档数据）的生产判定：明确阻塞与
// 未验证的组合一律拒绝——supported 必须有真机证据（T8 登记之前为空集），
// 未验证不标支持，也不悄悄放行。
func NewRegistryPolicy() CapabilityPolicy { return registryPolicy{} }

type registryPolicy struct{}

func (registryPolicy) Check(os, arch, method string) error {
	lc := domain.LookupLifecycleCapability(os, arch, method)
	if lc.State == domain.CapabilitySupported {
		return nil
	}
	reason := lc.Reason
	if reason == "" {
		reason = "capability state " + lc.State
	}
	// 未验证与阻塞同拒：差别只在原因文本（未验证 ≠ 支持；阻塞 = 上游缺陷等）。
	return fmt.Errorf("combo %s/%s+%s refused before execution (%s): %s",
		os, arch, method, lc.State, reason)
}

// TargetSpec 是一个执行目标（控制面 proto 的无协议依赖形态；daemon 转换后传入）。
type TargetSpec struct {
	// Machine 是目标所属机器（参与目标身份键，与控制面 domain.LifecycleTarget.Key
	// 逐段一致——上报按 opID+键对位落库）。
	Machine string
	Type    string // domain.LifecycleOpInstall / Upgrade / Uninstall（CheckUpdate 不到节点）
	User    string
	Agent   string
	Method  string
	Package string
	// Version 是创建操作时固定的精确版本；仅 Install/Upgrade 携带。
	Version string
	// Env 是目标执行环境（空 = native）。与本引擎环境不符的目标拒绝执行，
	// 防止在错误环境（如 WSL 与 Windows 原生混淆）里动程序本体。
	Env string
	// OS/Arch 是创建时从机器台账快照的平台事实（审计用）。与本节点运行时
	// 平台不一致时拒绝——"当时以为是哪个平台"与"实际在哪执行"必须一致。
	OS   string
	Arch string
	// Executables 是程序入口名（定位可执行文件用，如 ["codex"]）。
	Executables []string
}

// Key 返回目标身份键（与 domain.LifecycleTarget.Key 同构：\x1f 分隔）。
func (t TargetSpec) Key() string {
	return strings.Join([]string{t.Machine, normalizeEnv(t.Env), t.User, t.Agent, t.Method, t.Package}, "\x1f")
}

// TargetOutcome 是一个目标的执行结果（domain 状态机常量，白名单原因）。
// needsReconfirm 为 true 表示"发现影响变化、目标保持 pending 等操作者重新
// 确认"——它不是状态机的状态，是随上报携带的闸门标记。
type TargetOutcome struct {
	TargetKey string
	State     string
	Reason    string
	Message   string
	Evidence  domain.LifecycleEvidence

	needsReconfirm bool
}

// OpRequest 是一次生命周期操作（已确认意图；节点按 OpID 幂等去重）。
type OpRequest struct {
	OpID    string
	Type    string
	Version string
	// RediscoveryRequired 为 true 时（重试操作首派）逐目标先重新发现：
	// 意图已被现实满足 → 如实上报成功；影响变化 → needs_reconfirmation。
	RediscoveryRequired bool
	// Deadline 是控制面设定的整单超时（零值 = 每目标 DefaultOpTimeout）。
	Deadline time.Time
	Targets  []TargetSpec
}

// Report 是整份操作的结果上报（daemon 转 proto 后经通道回传控制面）。
type Report struct {
	OpID    string
	Results []TargetOutcome
	// NeedsReconfirmation：重试首派重发现发现影响变化时置位；控制面须等
	// 操作者重新确认后才可继续派发（重派不再携带 rediscovery 标记）。
	NeedsReconfirmation bool
	ReconfirmReason     string
	FinishedAt          time.Time
}

// NodeExecutorConfig 是节点执行引擎的装配参数。
type NodeExecutorConfig struct {
	// Engines 是安装方式 → 引擎的注册表。未注册的 method 拒绝执行
	//（npm → NewNpmEngine；binary/brew 等组合已被能力矩阵阻塞或未验证）。
	Engines map[string]Engine
	// Policy 是组合准入判定；nil 用生产 RegistryPolicy。
	Policy CapabilityPolicy
	// Env 是本引擎代表的执行环境；空 = native。
	Env string
	// Timeout 是单目标执行超时上限；零值取 DefaultOpTimeout。
	Timeout time.Duration
	// Now 供测试注入时钟。
	Now func() time.Time
	// Log 供诊断；可为 nil。
	Log Logger
}

// Logger 是执行引擎的最小日志接口（daemon 侧 slog 适配）。
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

// NodeExecutor 是节点侧执行引擎：无状态地执行一个 OpRequest，逐目标返回
// domain 状态机结果。串行化（队列/去重/outbox）由宿主（agentd daemon）负责。
type NodeExecutor struct {
	engines map[string]Engine
	policy  CapabilityPolicy
	env     string
	timeout time.Duration
	now     func() time.Time
	log     Logger
}

// NewNodeExecutor 装配节点执行引擎。
func NewNodeExecutor(cfg NodeExecutorConfig) *NodeExecutor {
	if cfg.Policy == nil {
		cfg.Policy = NewRegistryPolicy()
	}
	if cfg.Env == "" {
		cfg.Env = NativeEnv
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultOpTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &NodeExecutor{
		engines: cfg.Engines,
		policy:  cfg.Policy,
		env:     cfg.Env,
		timeout: cfg.Timeout,
		now:     cfg.Now,
		log:     cfg.Log,
	}
}

// Execute 执行整份操作并返回逐目标结果。它不返回 error：每个目标的失败都是
// 一个如实的 failed 结果（执行层错误不该让整份上报丢失——逐目标持久化是验收
// 标准）。Engine 返回的错误映射为 failed(op-failed)，重发现失败映射为
// failed(discovery-failed)，准入拒绝映射为 failed(unsupported-combo)。
func (x *NodeExecutor) Execute(ctx context.Context, op OpRequest) Report {
	rep := Report{OpID: op.OpID, FinishedAt: x.now().UTC()}
	for _, t := range op.Targets {
		out := x.executeTarget(ctx, op, t)
		if out.needsReconfirm {
			// 闸门标记只随上报置操作级 needs_reconfirmation；目标自身以
			// failed(version-mismatch) 落定（本尝试未执行、未变更任何东西）。
			rep.NeedsReconfirmation = true
			if rep.ReconfirmReason == "" {
				rep.ReconfirmReason = out.Message
			}
		}
		rep.Results = append(rep.Results, out)
	}
	return rep
}

// executeTarget 执行单个目标（环境/平台闸 → 准入 → 重发现 → 执行 → 结果映射）。
func (x *NodeExecutor) executeTarget(ctx context.Context, op OpRequest, t TargetSpec) TargetOutcome {
	key := t.Key()
	// 1) 环境匹配：agentd 只代表自己的执行环境说话。
	if normalizeEnv(t.Env) != x.env {
		return refuse(key, fmt.Sprintf("target env %q does not match this executor env %q", t.Env, x.env))
	}
	// 2) 卸载必须有已识别来源（创建时已强制；执行前再闸一道——防绕过）。
	// 先于引擎查找：来源未识别时给出更精确的拒绝原因。
	if op.Type == domain.LifecycleOpUninstall && (t.Method == "" || t.Package == "") {
		return refuse(key, "uninstall requires an identified source (method+package)")
	}
	// 3) 安装方式必须有引擎适配（禁止静默替换引擎）。
	eng, ok := x.engines[t.Method]
	if !ok || eng == nil {
		return refuse(key, fmt.Sprintf("no lifecycle engine adapter for method %q", t.Method))
	}
	// 4) 平台一致性 + 组合准入（未验证/阻塞在执行前拒绝并展示原因）。
	runOS, runArch := runtime.GOOS, runtime.GOARCH
	if (t.OS != "" && t.OS != runOS) || (t.Arch != "" && t.Arch != runArch) {
		return refuse(key, fmt.Sprintf("ledger platform %s/%s does not match runtime %s/%s",
			t.OS, t.Arch, runOS, runArch))
	}
	if err := x.policy.Check(runOS, runArch, t.Method); err != nil {
		return refuse(key, err.Error())
	}
	// 5) 重试首派：先重新发现实际状态（spec story 30）。影响变化的目标按
	// failed(version-mismatch) 落定（本次尝试没有执行、也没有改变任何东西），
	// 同时置操作级 needs_reconfirmation——操作者重新确认后的继续经新重试操作
	// 派发（逐目标状态机没有 running/pending 回退边，同一操作无法二次派发）。
	if op.RediscoveryRequired {
		out, reconfirmWhy, proceed := x.rediscover(ctx, op, t, eng)
		if !proceed {
			return out
		}
		if reconfirmWhy != "" {
			return TargetOutcome{TargetKey: key, State: domain.TargetFailed,
				Reason: domain.ReasonVersionMismatch, Message: reconfirmWhy,
				needsReconfirm: true}
		}
	}
	// 6) 执行：单目标 context 超时 = min(整单 deadline, 单目标上限)。
	tctx, cancel := x.targetCtx(ctx, op)
	defer cancel()
	switch op.Type {
	case domain.LifecycleOpInstall:
		return x.runEngine(tctx, key, t, domain.ReasonVerifiedPresent, eng.InstallExact)
	case domain.LifecycleOpUpgrade:
		return x.runEngine(tctx, key, t, domain.ReasonVerifiedPresent, eng.UpgradeToExact)
	case domain.LifecycleOpUninstall:
		return x.runEngine(tctx, key, t, domain.ReasonVerifiedAbsent,
			func(ctx context.Context, req Request) (Outcome, error) {
				return eng.Uninstall(ctx, UninstallRequest{
					AgentID: req.AgentID, Package: req.Package,
					Executables: req.Executables,
				})
			})
	default:
		return refuse(key, fmt.Sprintf("op type %q never executes on a node", op.Type))
	}
}

// rediscover 是重试首派的重发现闸：
//   - 探测失败 → failed(discovery-failed)，不执行；
//   - install/upgrade 已在固定版本 → succeeded(verified-present)（意图已被
//     现实满足，如实上报，不动程序本体）；
//   - install/upgrade 在其他版本 → needs_reconfirmation（影响变化：执行将
//     替换操作者不知道的版本，须重新确认）；目标保持 pending；
//   - uninstall 已缺席 → succeeded(verified-absent)；
//   - 其余（install/upgrade 缺席、uninstall 在场）→ 前提成立，继续执行。
func (x *NodeExecutor) rediscover(ctx context.Context, op OpRequest, t TargetSpec, eng Engine) (TargetOutcome, string, bool) {
	key := t.Key()
	det, err := eng.Detect(ctx, requestOf(t))
	if err != nil {
		return failDiscovery(key, redact(err.Error())), "", false
	}
	switch op.Type {
	case domain.LifecycleOpInstall, domain.LifecycleOpUpgrade:
		if !det.Installed {
			return TargetOutcome{}, "", true // 缺席：前提成立
		}
		if det.Version == op.Version {
			return TargetOutcome{TargetKey: key, State: domain.TargetSucceeded,
				Reason:   domain.ReasonVerifiedPresent,
				Message:  "pre-execution rediscovery: instance already at requested fixed version " + op.Version,
				Evidence: x.evidenceOf(t, det.Version, det.ExecutablePath, "already at requested version"),
			}, "", false
		}
		return TargetOutcome{}, fmt.Sprintf(
			"rediscovery found %s@%s before retry, fixed-version intent is %s; operator reconfirmation required",
			t.Package, det.Version, op.Version), true
	case domain.LifecycleOpUninstall:
		if !det.Installed {
			return TargetOutcome{TargetKey: key, State: domain.TargetSucceeded,
				Reason:   domain.ReasonVerifiedAbsent,
				Message:  "pre-execution rediscovery: instance already absent",
				Evidence: x.evidenceOf(t, "", det.ExecutablePath, "already absent"),
			}, "", false
		}
		return TargetOutcome{}, "", true // 在场：前提成立
	default:
		return refuse(key, fmt.Sprintf("op type %q never executes on a node", op.Type)), "", false
	}
}

// runEngineFn 统一 Install/Upgrade 的引擎入口形态（Uninstall 经闭包适配）。
type runEngineFn func(ctx context.Context, req Request) (Outcome, error)

// runEngine 执行引擎调用并映射结果。引擎内建"操作后重新发现并独立核验"，
// 这里只做状态/原因的如实映射：引擎错误 ≠ 成功，证据只取引擎核验后的实测值
//（Outcome.VerifiedVersion 来自独立重发现，不是上游返回值——KM-46 零值陷阱）。
func (x *NodeExecutor) runEngine(ctx context.Context, key string, t TargetSpec,
	successReason string, run runEngineFn) TargetOutcome {
	out, err := run(ctx, requestOf(t))
	if err != nil {
		return TargetOutcome{TargetKey: key, State: domain.TargetFailed,
			Reason: domain.ReasonOpFailed, Message: truncateStr(redact(err.Error()), 512)}
	}
	verdict := out.VerifiedVersion
	if successReason == domain.ReasonVerifiedAbsent {
		verdict = "" // 卸载成功 = 实例确已消失，版本为空
	}
	return TargetOutcome{TargetKey: key, State: domain.TargetSucceeded,
		Reason:   successReason,
		Evidence: x.evidenceOf(t, verdict, out.ExecutablePath, string(out.Op)),
	}
}

// targetCtx 给单目标执行设超时：整单 deadline 与单目标上限取更早者。
// 外部执行必须有超时（验收标准）；deadline 已过时 context 立即取消，
// 引擎调用以失败返回而不是无限等待。cancel 由调用方在执行后释放。
func (x *NodeExecutor) targetCtx(ctx context.Context, op OpRequest) (context.Context, context.CancelFunc) {
	deadline := x.now().Add(x.timeout)
	if !op.Deadline.IsZero() && op.Deadline.Before(deadline) {
		deadline = op.Deadline
	}
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		return ctx, func() {} // 沿用更早的父 deadline
	}
	return context.WithDeadline(ctx, deadline)
}

func requestOf(t TargetSpec) Request {
	return Request{
		AgentID: t.Agent, Package: t.Package, Version: t.Version,
		Executables: t.Executables,
		// 平台声明沿用 catalog 语义：npm 包跨平台，不另设限制；
		// 平台准入由 CapabilityPolicy 与环境/平台一致性检查承担。
	}
}

func normalizeEnv(env string) string {
	env = strings.TrimSpace(env)
	if env == "" {
		return NativeEnv
	}
	return env
}

// evidenceOf 是证据构造的统一入口（RecordedAt 取注入时钟，保证测试时间域
// 一致；消息经统一脱敏 + 限长）。
func (x *NodeExecutor) evidenceOf(t TargetSpec, version, execPath, note string) domain.LifecycleEvidence {
	return domain.LifecycleEvidence{
		ObservedVersion: version,
		ExecutablePath:  execPath,
		Method:          t.Method,
		Message:         truncateStr(redact(note), 256),
		RecordedAt:      x.now().UTC(),
	}
}

// refuse 是执行前拒绝：unsupported-combo（未验证/阻塞/环境或平台不符/无引擎/
// 来源未知）。禁止静默替换——拒绝必须带着原因走到操作者面前。
func refuse(key, why string) TargetOutcome {
	return TargetOutcome{TargetKey: key, State: domain.TargetFailed,
		Reason: domain.ReasonUnsupportedCombo, Message: truncateStr(redact(why), 512)}
}

func failDiscovery(key, why string) TargetOutcome {
	return TargetOutcome{TargetKey: key, State: domain.TargetFailed,
		Reason: domain.ReasonDiscoveryFailed, Message: why}
}

// redact 是上报侧脱敏（KM-47 契约：证据 message 限长脱敏，写入前由上报方执行）。
// 实现在 domain.RedactEvidence——与控制面入库兜底同一份规则，不会漂移。
func redact(s string) string { return domain.RedactEvidence(s) }

// truncateStr 限长并标注（超限截断不静默）。
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}
