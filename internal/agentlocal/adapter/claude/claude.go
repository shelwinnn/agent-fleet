// Package claude 是 Claude（Claude Code，Anthropic）家族适配器
// （架构 v1.1.2 §5.4/§15；批次二片 B，KM-34）。
//
// 运行时来源与契约证据见 docs/adapters-batch-2.md §1（0/1 片 KM-32 的接入前确认）
// 与 §9（本片验收记录）：官方 npm `@anthropic-ai/claude-code`，可执行名 `claude`，
// 本机实测 `claude --version` → `2.1.270 (Claude Code)`、
// `claude doctor` 输出中的 `Running: npm-global (2.1.270)` 行。
//
// 受管范围（其余键一律未托管，整树重序列化时逐键保留）：
//
//	~/.claude/settings.json   仅 model 与 env.ANTHROPIC_BASE_URL；**嵌套键级所有权**
//	                          （env 下 ANTHROPIC_AUTH_TOKEN 等键不读、不写、不进期望状态）
//	~/.claude/CLAUDE.md       rules 受管标记块（FR-5.1）；本机该路径是软链，
//	                          必须写穿（kit.AtomicWrite 的 resolveWritePath）
//	~/.claude/skills/<name>   Skill 软链（FR-6.3）；本机该目录是软链集合
//
// **MCP 声明 unsupported（KM-32 裁决 (d)）**：user/local 作用域 MCP 的唯一落点是
// `~/.claude.json`——官方原文 "Claude Code also keeps a fifth file, `~/.claude.json`,
// that it writes for itself; you don't need to edit it"，它是应用高频重写的活状态
// 文件，没有外部写入协议。Validate 在任何写入之前显式拒绝 MCP 请求（禁止静默跳过），
// 再次评估触发条件见 §1.4/§5.5。
//
// 版本探测**只用 CLI**：`claude --version` 的 `<semver> (Claude Code)`，形态不符即
// 显式报错；备用 `claude doctor` 中 `Running: <install-method> (<semver>)` 行。
// 健康检查用 `claude --version` + `claude doctor`（均只读）；`claude mcp list` 会连接
// MCP server，不得用作离线健康检查。安装/升级不在本片：`Apply(version)` 版本不匹配
// 显式失败。
//
// 更高配置层（§1.4/§6 缺口 3）：`managed-settings.json`（组织下发的文件层，Linux 为
// `/etc/claude-code/`）在层序上压过用户 settings.json 且不可覆盖。Inventory 读实际
// 生效值并给被覆盖的受管键打 `overriddenBy` 标记；只要任一受管键被覆盖，Plan 返回
// **空计划**（整片零写入）、HealthCheck 返回 *HigherLayerOverrideError——既不报
// Reconciled，也不进入"写成功→verify 失败→回滚"的循环。远程/服务端下发层
// （admin console / MDM plist / Windows registry）无法从适配器读取生效值，本片用
// `claude doctor` 的 "Managed settings (remote)" / "Organization policy" 行做保守信号，
// 只按文件层给出精确生效值（未验证项见 Capabilities）。
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/kit"
)

const (
	// ID 是家族标识（矩阵统一名称）。
	ID = "claude"
	// SchemaVersion 是本适配器受管 schema 版本（随 Hello 上报，FR-13.4）。
	SchemaVersion = "claude-config-v1"
	// binaryName 是可执行程序名。
	binaryName = "claude"
	// baseURLKey 是 Claude settings.json 中承载端点的环境变量名
	// （docs/settings `### env`；本机 settings.json 的 env 块实测含该键）。
	baseURLKey = "ANTHROPIC_BASE_URL"
	// overrideMarkerKey 是观测投影里记录"被更高配置层覆盖"的保留键。
	overrideMarkerKey = "overriddenBy"
	// remoteLayer 是远程/服务端下发层（生效值不可读）的层名。
	remoteLayer = "managed settings (remote/server-managed, value unreadable)"
)

// 受管 plan 键（Change.Key 与 overriddenBy 标记共用同一命名，便于 Plan 精确跳过）。
const (
	keyModel    = "config.model"
	keyProvider = "config.provider"
	keyRules    = "rules.global"
)

var (
	// safeName 约束 MCP/技能条目名（文件名安全，禁止路径穿越）。
	safeName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// versionLineRe 精确匹配 `claude --version` 的 `<semver> (Claude Code)`；
	// 形态不符即显式报错（矩阵验收 1）。
	versionLineRe = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+(?:[-.+][0-9A-Za-z.-]+)?) \(Claude Code\)$`)
	// doctorLineRe 匹配 `claude doctor` 输出中的 `Running: <install-method> (<semver>)` 行。
	doctorLineRe = regexp.MustCompile(`^Running: .+ \(([0-9]+\.[0-9]+\.[0-9]+(?:[-.+][0-9A-Za-z.-]+)?)\)$`)
	// managedSettingsNegatives 是 doctor 报告"没有远程托管设置"的措辞。
	managedSettingsNegatives = []string{"not fetched", "not available", "not configured", "none"}
)

// parseVersionText 解析 `claude --version` 输出；形态不符返回 ""。
func parseVersionText(out string) string {
	m := versionLineRe.FindStringSubmatch(kit.FirstLine(out))
	if m == nil {
		return ""
	}
	return m[1]
}

// parseDoctorVersion 解析 `claude doctor` 输出中的 `Running: <install-method>
// (<semver>)` 行（本机实测它不是第 1 行：实测输出是 `Claude Code doctor` + 空行 +
// `Running: ...`，故逐行扫描）；形态不符返回 ""。
func parseDoctorVersion(out string) string {
	for _, line := range strings.Split(out, "\n") {
		m := doctorLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m != nil {
			return m[1]
		}
	}
	return ""
}

// CommandProbe 执行家族 CLI 子命令（home 为注入的 HOME 根，护栏 #12）。
// 可注入：fixture 测试不依赖真实二进制，也绝不落到真实 ~/.claude。
type CommandProbe func(ctx context.Context, home string, args ...string) (string, error)

// Adapter 是 Claude 适配器。Probe/Doctor/GOOS/ManagedSettingsDir 可注入，
// 便于 fixture 测试不依赖真实二进制与真实 /etc。
type Adapter struct {
	// Probe 执行 `claude --version`（形态不符时回退 Doctor 解析版本）。
	Probe CommandProbe
	// Doctor 执行只读的 `claude doctor`（版本回退 + 远程托管设置信号 + 健康检查）。
	Doctor CommandProbe
	// GOOS 覆盖运行平台（默认 runtime.GOOS；非 linux 在 Validate 阶段拒绝）。
	GOOS string
	// ManagedSettingsDir 覆盖更高层 `managed-settings.json` 所在目录
	// （默认按平台取系统目录；测试注入临时目录，绝不读真实 /etc）。
	ManagedSettingsDir string
}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) ID() string { return ID }

func (a *Adapter) goos() string {
	if a.GOOS != "" {
		return a.GOOS
	}
	return runtime.GOOS
}

// runProbe 执行注入的探测；nil 时用默认实现（argv 直执行，HOME 指向注入根）。
func runProbe(probe CommandProbe, ctx context.Context, home string, args ...string) (string, error) {
	if probe != nil {
		return probe(ctx, home, args...)
	}
	cmd := exec.CommandContext(ctx, binaryName, args...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (a *Adapter) doctorOutput(ctx context.Context, home string) (string, error) {
	return runProbe(a.Doctor, ctx, home, "doctor")
}

// Capabilities 声明本家族逐能力支持状态与证据（矩阵「统一适配契约」）。
// §1.5 的未验证项逐条落进对应能力的 Reason/Evidence（见 docs/adapters-batch-2.md §9）。
func (a *Adapter) Capabilities() []adapter.CapabilityDecl {
	return adapter.SortDecls([]adapter.CapabilityDecl{
		{
			Capability: adapter.CapabilityVersion, State: adapter.SupportSupported,
			VerifiedVersions: "claude 2.1.270 (linux/amd64)",
			Reason: "版本**探测**已验证；安装/升级（claude install/update 已文档化）不在本片，Apply(version) 版本不匹配时显式失败。" +
				"探测只用 CLI：`claude --version` 的 `<semver> (Claude Code)`，回退 `claude doctor` 首行的 `Running: <install-method> (<semver>)`，形态不符即显式报错。" +
				"未验证：macOS / Windows / WSL 无本机实测（Validate 对非 linux 明确拒绝）；`claude doctor` 的完整输出结构官方未给字段表，解析只按首行与两个固定前缀行",
			Evidence: "本机实测 `claude --version` → `2.1.270 (Claude Code)`；`claude doctor` 首行 → `Running: npm-global (2.1.270)`；" +
				"https://code.claude.com/docs/en/setup（系统需求）",
			Paths: []string{".claude/settings.json"},
		},
		{
			Capability: adapter.CapabilityModelProvider, State: adapter.SupportSupported,
			VerifiedVersions: "claude 2.1.270",
			Reason: "`model` 顶层键 + `env.ANTHROPIC_BASE_URL`（endpoint 直配）。" +
				"**apiKeyEnv ↔ apiKeyHelper 映射未设计**：settings.json 没有承载\"环境变量名\"的键，官方对凭据的指引是 `apiKeyHelper`（执行命令取凭据），" +
				"Fleet 既不读也不写凭据，故请求携带非空 apiKeyEnv 时 Validate 显式拒绝（unverified），绝不静默丢字段。" +
				"更高层 `managed-settings.json` 会压过用户层且不可覆盖：观测侧读实际生效值并给可区分健康态，被覆盖期间整片停写（见 §9）。" +
				"未验证：macOS/Windows/WSL；`ANTHROPIC_MODEL` 环境变量层（官方 precedence: --model > ANTHROPIC_MODEL > settings.model）不在本片判定范围；" +
				"`env` 块与 shell 导出的优先级（文档已述 settings 覆盖 shell，但无本机交互验证）；" +
				"`availableModels` / `enforceAvailableModels` 等管理键存在但不纳入受管范围，与 managed-settings 的关系留待范围决策",
			Evidence: "https://code.claude.com/docs/en/settings（`model` 优先级、`env` 注入、apiKeyHelper）与 settings-reference（`### env` / `### model` / `### apiKeyHelper`）；" +
				"本机 `~/.claude/settings.json` 顶层键实测 `[env, model, statusLine, enabledPlugins]`（仅键名，值不入 fixture）",
			Paths: []string{".claude/settings.json"},
		},
		{
			Capability: adapter.CapabilityMCP, State: adapter.SupportUnsupported,
			Reason: "KM-32 裁决 (d)：本批次 Claude 不接 MCP。user/local 作用域 MCP 的唯一落点是**应用自持的活状态文件** `~/.claude.json`" +
				"（官方原文 \"Claude Code also keeps a fifth file, `~/.claude.json`, that it writes for itself; you don't need to edit it\"），" +
				"没有外部写入协议，直写即与 Claude Code 并发写同一文件；project 作用域 `.mcp.json` 需要 Adapter 契约里不存在的 project root 入参，" +
				"且未信任目录里会停在 `Pending approval`（写成功但不生效）；`claude mcp add -s user` 是唯一厂商背书写路径但属新形态（调用家族 CLI），单列设计片。" +
				"Validate 在**任何写入之前**拒绝 MCP 请求。未验证：`~/.claude.json` 的并发写行为与是否使用文件锁；再次评估触发条件见 §5.5",
			Evidence: "https://code.claude.com/docs/en/claude-directory（~/.claude.json 由应用自持）；https://code.claude.com/docs/en/mcp（作用域表）；" +
				"本机 `~/.claude.json` mtime 2026-09-23 14:49 晚于 settings.json 的 2026-09-19 22:31（值未读取）",
		},
		{
			Capability: adapter.CapabilitySkills, State: adapter.SupportSupported,
			VerifiedVersions: "claude 2.1.270",
			Reason: "用户级 `~/.claude/skills/<name>` 为目标（`<name>/SKILL.md` 约定）。本机该目录是**软链集合**" +
				"（全部指向 `~/.agents/skills/<name>`）：Fleet 只管理自己点名的条目，链接指向规范缓存。" +
				"工件物化（FetchArtifact/bundle）未实现，缓存缺失时显式失败而不静默跳过；" +
				"未验证：软链幂等性由 fixture 锁定后声明 supported（§9 验收 4），macOS/Windows/WSL 未实测",
			Evidence: "本机实测 `ls -la ~/.claude/skills/` 全部条目为 `-> ../../.agents/skills/<name>`；https://code.claude.com/docs/en/skills",
			Paths:    []string{".claude/skills"},
		},
		{
			Capability: adapter.CapabilityRules, State: adapter.SupportSupported,
			VerifiedVersions: "claude 2.1.270",
			Reason: "用户级 `~/.claude/CLAUDE.md`（本机为软链）受管标记块：只替换块内，块外逐字节保留，且**写穿软链**" +
				"（复用批次一 kit.resolveWritePath 修复，批次一复核轮 #3）。未验证：macOS/Windows/WSL 未实测",
			Evidence: "本机实测 `ls -la ~/.claude/CLAUDE.md` → `-> /home/shelwin/.config/plexus/personal/rules/global.md`；" +
				"https://code.claude.com/docs/en/memory",
			Paths: []string{".claude/CLAUDE.md"},
		},
	})
}

// Validate 是阶段 1 的适配器侧前置校验（任何写入之前）。
func (a *Adapter) Validate(_ context.Context, home string, desired adapter.AgentDesiredState) error {
	if err := a.checkOS(); err != nil {
		return err
	}
	// 未支持/未验证能力（含 MCP）在此显式拒绝，零写入产物（矩阵口径）。
	if err := adapter.CheckCapabilities(ID, a.Capabilities(), desired); err != nil {
		return err
	}
	cfg := adapter.ConfigMap(desired)
	if len(desired.Config) != 0 && len(cfg) == 0 {
		return fmt.Errorf("agent %q: config is not a JSON object: %s", ID, string(desired.Config))
	}
	if model, ok := cfg["model"]; ok {
		if _, isString := model.(string); !isString {
			return fmt.Errorf("agent %q: config.model must be a string, got %T", ID, model)
		}
	}
	if endpoint, keyEnv, ok := adapter.ProviderConfig(cfg); ok {
		if endpoint == "" {
			return fmt.Errorf("agent %q: provider.endpoint is required when providerRef is set", ID)
		}
		if keyEnv != "" {
			// Claude 没有承载"环境变量名"的 settings 键；apiKeyHelper 承接凭据的
			// 映射尚未设计。宁显式拒绝，也不静默丢字段或落明文。
			return &adapter.ValidateError{
				Family: ID, Capability: adapter.CapabilityModelProvider, State: adapter.SupportUnverified,
				Reason: fmt.Sprintf("provider.apiKeyEnv=%q cannot be mapped: Claude has no settings key carrying an "+
					"environment-variable name (credentials come from apiKeyHelper or the user's login session); the "+
					"apiKeyHelper ↔ apiKeyEnv mapping is not designed in this slice, so Fleet refuses to guess", keyEnv),
			}
		}
	}
	for name := range desired.Skills {
		if !safeName.MatchString(name) {
			return &adapter.ValidateError{
				Family: ID, Capability: adapter.CapabilitySkills, State: adapter.SupportUnsupported,
				Reason: fmt.Sprintf("skill name %q must match %s so it is a safe single-segment directory name", name, safeName),
			}
		}
	}
	if _, _, err := kit.ReadManagedBlock(a.rulesPath(home)); err != nil {
		return fmt.Errorf("agent %q: %w", ID, err)
	}
	raw, err := os.ReadFile(a.configPath(home))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("agent %q: read settings.json: %w", ID, err)
	}
	if len(raw) != 0 {
		doc, derr := decodeSettings(raw)
		if derr != nil {
			return fmt.Errorf("agent %q: existing settings.json is unparseable: %w", ID, derr)
		}
		// 嵌套键级所有权要求 env 是对象：否则无法只替换受管叶子键。
		if env, ok := doc["env"]; ok {
			if _, isObj := env.(map[string]any); !isObj {
				return fmt.Errorf("agent %q: settings.json env must be a JSON object to keep nested key ownership, got %T", ID, env)
			}
		}
	}
	// 更高层文件必须可解析：否则无法判定生效值，宁停在阶段 1。
	if _, err := a.readManagedSettings(); err != nil {
		return fmt.Errorf("agent %q: %w", ID, err)
	}
	return nil
}

func (a *Adapter) checkOS() error {
	switch a.goos() {
	case "linux":
		return nil
	default:
		return fmt.Errorf("agent %q: OS %q is unverified (only linux/amd64 verified on claude 2.1.270; "+
			"macOS/Windows/WSL support has no on-host evidence in this repo yet)", ID, a.goos())
	}
}

// probeVersion 探测版本：优先 `claude --version`，形态不符时回退 `claude doctor` 的
// `Running: ...` 行。
func (a *Adapter) probeVersion(ctx context.Context, home string) (string, error) {
	out, err := runProbe(a.Probe, ctx, home, "--version")
	switch {
	case err == nil:
		if v := parseVersionText(out); v != "" {
			return v, nil
		}
	case kit.IsNotInstalled(err):
		return "", fmt.Errorf("%w: %s", kit.ErrNotInstalled, binaryName)
	}
	doctorOut, derr := a.doctorOutput(ctx, home)
	if derr != nil {
		if kit.IsNotInstalled(derr) {
			return "", fmt.Errorf("%w: %s", kit.ErrNotInstalled, binaryName)
		}
		return "", fmt.Errorf("probe %s: %w", binaryName, derr)
	}
	if v := parseDoctorVersion(doctorOut); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("probe %s: cannot parse version from `--version` output %q nor `doctor` output %q",
		binaryName, strings.TrimSpace(out), strings.TrimSpace(kit.FirstLine(doctorOut)))
}

// Detect 探测已装/未装与版本（矩阵验收 1：程序缺失与版本不可解析必须可区分）。
func (a *Adapter) Detect(ctx context.Context, home string) (adapter.DetectedAgent, error) {
	d := adapter.DetectedAgent{Family: ID, ConfigPath: a.configPath(home), SchemaVer: SchemaVersion}
	v, err := a.probeVersion(ctx, home)
	switch {
	case errors.Is(err, kit.ErrNotInstalled):
		return d, nil // 未安装：Installed=false，不是错误
	case err != nil:
		return d, err // 版本不可解析：显式错误（不伪装成"未安装"）
	}
	d.Installed, d.Version = true, v
	return d, nil
}

// Inventory 产出 ADR-1 双侧投影。观测侧读**实际生效值**：managed-settings.json
// 在层序上压过用户 settings.json，故被覆盖的受管键用其生效值并打 overriddenBy 标记。
func (a *Adapter) Inventory(ctx context.Context, home string, desired adapter.AgentDesiredState) (adapter.AgentObservedState, error) {
	desiredProj, err := a.desiredProjection(desired)
	if err != nil {
		return adapter.AgentObservedState{}, err
	}
	doc, err := readSettingsFile(a.configPath(home))
	if err != nil {
		return adapter.AgentObservedState{}, err
	}
	observedProj, err := a.observedProjection(home, doc, desired)
	if err != nil {
		return adapter.AgentObservedState{}, err
	}
	if err := a.markHigherLayerOverrides(ctx, home, desired, observedProj); err != nil {
		return adapter.AgentObservedState{}, err
	}
	return adapter.AgentObservedState{
		Family:                   ID,
		Version:                  a.probeVersionQuiet(ctx, home),
		ManagedProjection:        observedProj,
		DesiredProjectionDigest:  kit.ProjectionDigest(desiredProj),
		ObservedProjectionDigest: kit.ProjectionDigest(observedProj),
		CanonicalizationVersion:  adapter.ProjectionCanonicalizationVersion,
		ConfigPath:               a.configPath(home),
	}, nil
}

// probeVersionQuiet 供 Inventory 使用：未安装或不可解析都返回 ""（此时版本步骤由
// HealthCheck/Apply 显式失败）。
func (a *Adapter) probeVersionQuiet(ctx context.Context, home string) string {
	v, err := a.probeVersion(ctx, home)
	if err != nil {
		return ""
	}
	return v
}

// ---- 投影 ----

func (a *Adapter) desiredProjection(desired adapter.AgentDesiredState) (map[string]any, error) {
	proj := map[string]any{}
	cfg := adapter.ConfigMap(desired)
	if len(desired.Config) != 0 && len(cfg) == 0 {
		return nil, fmt.Errorf("claude: desired config is not a JSON object")
	}
	if model, ok := cfg["model"]; ok {
		proj["model"] = fmt.Sprintf("%v", model)
	}
	if endpoint, _, ok := adapter.ProviderConfig(cfg); ok {
		// 受管面只有端点：凭据由用户的 apiKeyHelper / 登录态解决（Validate 拒绝
		// apiKeyEnv），故 provider 投影不携带 apiKeyEnv。
		proj["provider"] = map[string]any{"endpoint": endpoint}
	}
	if len(desired.Skills) != 0 {
		skills := map[string]any{}
		for name, s := range desired.Skills {
			skills[name] = s.ContentDigest
		}
		proj["skills"] = skills
	}
	if content := kit.RulesContent(desired.Rules); content != "" {
		proj["rules"] = content
	}
	return proj, nil
}

func (a *Adapter) observedProjection(home string, doc map[string]any, desired adapter.AgentDesiredState) (map[string]any, error) {
	proj := map[string]any{}
	dcfg := adapter.ConfigMap(desired)
	if _, ok := dcfg["model"]; ok {
		proj["model"] = stringOf(doc["model"])
	}
	if _, _, ok := adapter.ProviderConfig(dcfg); ok {
		env, _ := doc["env"].(map[string]any)
		proj["provider"] = map[string]any{"endpoint": stringOf(env[baseURLKey])}
	}
	if len(desired.Skills) != 0 {
		skills := map[string]any{}
		for name := range desired.Skills {
			skills[name] = kit.ReadSkillDigest(a.skillPath(home, name))
		}
		proj["skills"] = skills
	}
	if kit.RulesContent(desired.Rules) != "" {
		content, _, err := kit.ReadManagedBlock(a.rulesPath(home))
		if err != nil {
			return nil, err
		}
		proj["rules"] = content
	}
	return proj, nil
}

// Plan 比较两侧投影，逐受管键产出变更（阶段 3；空计划即幂等，FR-9.3）。
// 只要任一受管键被更高配置层覆盖，整片返回空计划（§9 口径）：写用户层是徒劳的，
// 且同机其它受管项的写入会被阶段 10 的健康失败整轮回滚，形成每轮写+回滚的循环。
func (a *Adapter) Plan(ctx context.Context, home string, desired adapter.AgentDesiredState, observed adapter.AgentObservedState) ([]adapter.Change, error) {
	desiredProj, err := a.desiredProjection(desired)
	if err != nil {
		return nil, err
	}
	observedProj := observed.ManagedProjection
	if observedProj == nil {
		observedProj = map[string]any{}
	}
	if len(overrideSet(observedProj)) != 0 {
		return nil, nil
	}
	var changes []adapter.Change
	if desired.Version != "" && observed.Version != desired.Version {
		changes = append(changes, adapter.Change{Family: ID, Step: "version", Key: "version",
			From: observed.Version, To: desired.Version})
	}
	if !projectionEqual(desiredProj["model"], observedProj["model"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "config", Key: keyModel,
			From: observedProj["model"], To: desiredProj["model"]})
	}
	if !projectionEqual(desiredProj["provider"], observedProj["provider"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "config", Key: keyProvider,
			From: observedProj["provider"], To: desiredProj["provider"]})
	}
	changes = append(changes, kit.DiffMapStep(ID, "skills", "skill:", desiredProj["skills"], observedProj["skills"])...)
	if !projectionEqual(desiredProj["rules"], observedProj["rules"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "rules", Key: keyRules,
			From: observedProj["rules"], To: desiredProj["rules"]})
	}
	return changes, nil
}

// Apply 执行变更（阶段 5–9）：settings.json 整树重序列化（只改受管键，未托管键逐键
// 保留）、Skill 软链、rules 受管块（写穿软链）。空变更零写入（幂等）。
func (a *Adapter) Apply(ctx context.Context, home string, desired adapter.AgentDesiredState, changes []adapter.Change) error {
	byStep := map[string][]adapter.Change{}
	for _, c := range changes {
		byStep[c.Step] = append(byStep[c.Step], c)
	}
	if len(byStep["version"]) != 0 {
		// 版本安装由 command installer（§20.2）负责，本片未实现该路径：
		// 安装后再探测验证（FR-2.3），不匹配即显式失败，绝不谎报成功。
		v := a.probeVersionQuiet(ctx, home)
		if v != desired.Version {
			return fmt.Errorf("claude: version %s is required but %q is installed and the command installer "+
				"(§20.2) is not wired in this slice", desired.Version, v)
		}
	}
	if configChanges := byStep["config"]; len(configChanges) != 0 {
		// 防御：计划可能来自被更高层覆盖的键（陈旧 plan）——写前再过滤一次。
		over, err := a.higherLayerOverrides(ctx, home, desired)
		if err != nil {
			return fmt.Errorf("claude: %w", err)
		}
		if len(dropOverridden(configChanges, over)) != 0 {
			if err := a.applySettings(home, desired); err != nil {
				return err
			}
		}
	}
	for _, c := range byStep["skills"] {
		name := strings.TrimPrefix(c.Key, "skill:")
		digest := fmt.Sprintf("%v", c.To)
		if err := kit.EnsureSkillLink(a.skillPath(home, name), name, digest, kit.SkillCacheDir(home, name, digest)); err != nil {
			return fmt.Errorf("claude: %w", err)
		}
	}
	if len(byStep["rules"]) != 0 {
		if err := kit.WriteManagedBlock(a.rulesPath(home), kit.RulesContent(desired.Rules)); err != nil {
			return fmt.Errorf("claude: rules: %w", err)
		}
	}
	return nil
}

func dropOverridden(changes []adapter.Change, over map[string]layerOverride) []adapter.Change {
	out := make([]adapter.Change, 0, len(changes))
	for _, c := range changes {
		if _, ok := over[c.Key]; ok {
			continue
		}
		out = append(out, c)
	}
	return out
}

// applySettings 整树重序列化合并写（§16.1）：只写 `model` 与
// `env.ANTHROPIC_BASE_URL`，其余顶层键与 env 下未托管键逐键保留。
// JSON 无注释语义，故整树重新序列化：未知键与取值全部保留，但**用户注释不保留**
// （已知限制，同批次一 opencode 的口径）。
func (a *Adapter) applySettings(home string, desired adapter.AgentDesiredState) error {
	path := a.configPath(home)
	doc, err := readSettingsFile(path)
	if err != nil {
		return err
	}
	cfg := adapter.ConfigMap(desired)
	if model, ok := cfg["model"]; ok {
		doc["model"] = fmt.Sprintf("%v", model)
	}
	if endpoint, _, ok := adapter.ProviderConfig(cfg); ok {
		env, _ := doc["env"].(map[string]any)
		if env == nil {
			env = map[string]any{}
		}
		env[baseURLKey] = endpoint
		doc["env"] = env
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("claude: marshal settings: %w", err)
	}
	out = append(out, '\n')
	if _, err := decodeSettings(out); err != nil {
		return fmt.Errorf("claude: patched settings do not parse, aborting write: %w", err)
	}
	return kit.AtomicWrite(path, out, 0o600)
}

// HealthCheck（阶段 10）：先给"被更高层覆盖"这一可区分健康态，再验证
// `claude --version` 与只读 `claude doctor`、settings.json 可解析且受管键到位。
func (a *Adapter) HealthCheck(ctx context.Context, home string, desired adapter.AgentDesiredState) error {
	// 官方只读健康命令；`claude mcp list` 会连接 MCP server，明确不用（§1.1）。
	doctorOut, err := a.doctorOutput(ctx, home)
	if err != nil {
		return fmt.Errorf("claude: health: `claude doctor` failed: %w", err)
	}
	over, err := a.overridesFrom(home, desired, doctorOut)
	if err != nil {
		return fmt.Errorf("claude: health: %w", err)
	}
	if len(over) != 0 {
		key := sortedOverrideKeys(over)[0]
		o := over[key]
		return &HigherLayerOverrideError{Family: ID, Key: key, Layer: o.Layer, Effective: o.Effective, Wanted: o.Wanted}
	}
	doc, err := readSettingsFile(a.configPath(home))
	if err != nil {
		return fmt.Errorf("claude: health: %w", err)
	}
	dcfg := adapter.ConfigMap(desired)
	if model, ok := dcfg["model"]; ok {
		want := fmt.Sprintf("%v", model)
		if got := stringOf(doc["model"]); got != want {
			return fmt.Errorf("claude: health: settings.json model is %q, want %q", got, want)
		}
	}
	if endpoint, _, ok := adapter.ProviderConfig(dcfg); ok {
		env, _ := doc["env"].(map[string]any)
		if got := stringOf(env[baseURLKey]); got != endpoint {
			return fmt.Errorf("claude: health: env.%s is %q, want %q", baseURLKey, got, endpoint)
		}
	}
	if desired.Version != "" {
		v, err := a.probeVersion(ctx, home)
		if err != nil {
			return fmt.Errorf("claude: health: version probe failed: %w", err)
		}
		if v != desired.Version {
			return fmt.Errorf("claude: health: installed version %s != desired %s", v, desired.Version)
		}
	}
	for name, s := range desired.Skills {
		if got := kit.ReadSkillDigest(a.skillPath(home, name)); got != s.ContentDigest {
			return fmt.Errorf("claude: health: skill %q link digest is %q, want %q", name, got, s.ContentDigest)
		}
	}
	if kit.RulesContent(desired.Rules) != "" {
		content, ok, err := kit.ReadManagedBlock(a.rulesPath(home))
		if err != nil {
			return fmt.Errorf("claude: health: %w", err)
		}
		if !ok || content != kit.RulesContent(desired.Rules) {
			return fmt.Errorf("claude: health: rules managed block is missing or differs")
		}
	}
	return nil
}

// ---- 更高配置层（managed-settings.json，§1.4/§6 缺口 3）----

// HigherLayerOverrideError 表示受管键被更高的配置层覆盖：Fleet 的用户层写入无法
// 生效（managed-settings.json 在层序上位于用户 settings.json 之后且不可覆盖）。
// 这是**可区分的健康态**——既不是 Reconciled，也不是"写成功→verify 失败→回滚"的循环。
type HigherLayerOverrideError struct {
	Family    string
	Key       string
	Layer     string
	Effective any
	Wanted    any
}

func (e *HigherLayerOverrideError) Error() string {
	return fmt.Sprintf("agent %q managed key %s is overridden by higher config layer %s "+
		"(effective=%v, wanted=%v); Fleet's managed layer is the user layer and cannot override an "+
		"organization-managed setting — resolve on the policy layer or stop managing this key",
		e.Family, e.Key, e.Layer, e.Effective, e.Wanted)
}

// layerOverride 是单个受管键的覆盖记录。
type layerOverride struct {
	Effective any
	Wanted    any
	Layer     string
}

// managedSettings 是合并后的更高层受管键生效值（primary + managed-settings.d 片段）。
type managedSettings struct {
	model        string
	modelLayer   string
	modelSet     bool
	baseURL      string
	baseURLLayer string
	baseURLSet   bool
}

// managedSettingsDir 返回更高层目录：默认按平台取官方系统目录（Linux 为
// `/etc/claude-code/`，见 docs/settings 的 Managed 作用域）。
func (a *Adapter) managedSettingsDir() string {
	if a.ManagedSettingsDir != "" {
		return a.ManagedSettingsDir
	}
	switch a.goos() {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	default:
		return "/etc/claude-code"
	}
}

// readManagedSettings 合并读取文件层更高配置：`managed-settings.json` 以及
// 字母序的 `managed-settings.d/*.json`（官方支持 drop-in 片段）。后者覆盖前者。
// 文件不存在即跳过；不可解析或受管键类型不符则显式报错（不猜生效值）。
func (a *Adapter) readManagedSettings() (managedSettings, error) {
	var ms managedSettings
	dir := a.managedSettingsDir()
	files := []string{filepath.Join(dir, "managed-settings.json")}
	fragments, err := filepath.Glob(filepath.Join(dir, "managed-settings.d", "*.json"))
	if err != nil {
		return ms, fmt.Errorf("glob managed-settings.d: %w", err)
	}
	sort.Strings(fragments)
	files = append(files, fragments...)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return ms, fmt.Errorf("read %s: %w", f, err)
		}
		doc, err := decodeSettings(raw)
		if err != nil {
			return ms, fmt.Errorf("%s is unparseable: %w", f, err)
		}
		if v, ok := doc["model"]; ok {
			s, isStr := v.(string)
			if !isStr {
				return ms, fmt.Errorf("%s: model must be a string, got %T", f, v)
			}
			ms.model, ms.modelLayer, ms.modelSet = s, f, true
		}
		if rawEnv, ok := doc["env"]; ok {
			env, isObj := rawEnv.(map[string]any)
			if !isObj {
				return ms, fmt.Errorf("%s: env must be a JSON object, got %T", f, rawEnv)
			}
			if v, ok := env[baseURLKey]; ok {
				s, isStr := v.(string)
				if !isStr {
					return ms, fmt.Errorf("%s: env.%s must be a string, got %T", f, baseURLKey, v)
				}
				ms.baseURL, ms.baseURLLayer, ms.baseURLSet = s, f, true
			}
		}
	}
	return ms, nil
}

// higherLayerOverrides 计算"哪些受管键的实际生效值来自更高层"。`claude doctor`
// 不可用只意味着无法给出远程层信号，不影响文件层判定（观测不因健康命令失败而整体失败）。
func (a *Adapter) higherLayerOverrides(ctx context.Context, home string, desired adapter.AgentDesiredState) (map[string]layerOverride, error) {
	doctorOut, _ := a.doctorOutput(ctx, home)
	return a.overridesFrom(home, desired, doctorOut)
}

// overridesFrom：文件层给出精确生效值；doctor 报告的远程/服务端层只能报"被覆盖、
// 值不可读"。只有"该层确实设了受管键、且值不同于期望"才计为覆盖。
func (a *Adapter) overridesFrom(home string, desired adapter.AgentDesiredState, doctorOut string) (map[string]layerOverride, error) {
	dcfg := adapter.ConfigMap(desired)
	if len(dcfg) == 0 {
		return nil, nil
	}
	ms, err := a.readManagedSettings()
	if err != nil {
		return nil, err
	}
	out := map[string]layerOverride{}
	if wantModel, ok := dcfg["model"]; ok {
		want := fmt.Sprintf("%v", wantModel)
		if ms.modelSet && !kit.ValuesEqual(ms.model, want) {
			out[keyModel] = layerOverride{Effective: ms.model, Wanted: want, Layer: ms.modelLayer}
		}
	}
	if endpoint, _, ok := adapter.ProviderConfig(dcfg); ok {
		if ms.baseURLSet && !kit.ValuesEqual(ms.baseURL, endpoint) {
			out[keyProvider] = layerOverride{
				Effective: map[string]any{"endpoint": ms.baseURL},
				Wanted:    map[string]any{"endpoint": endpoint},
				Layer:     ms.baseURLLayer,
			}
		}
	}
	if remoteManagedSignal(doctorOut) {
		const unknown = "unknown (layer value not readable outside the home)"
		if wantModel, ok := dcfg["model"]; ok {
			if _, exists := out[keyModel]; !exists {
				out[keyModel] = layerOverride{Effective: unknown, Wanted: fmt.Sprintf("%v", wantModel), Layer: remoteLayer}
			}
		}
		if endpoint, _, ok := adapter.ProviderConfig(dcfg); ok {
			if _, exists := out[keyProvider]; !exists {
				out[keyProvider] = layerOverride{
					Effective: unknown,
					Wanted:    map[string]any{"endpoint": endpoint},
					Layer:     remoteLayer,
				}
			}
		}
	}
	return out, nil
}

// remoteManagedSignal 是 `claude doctor` 报告的远程/服务端托管设置信号：官方输出
// 有 `Managed settings (remote):` 与 `Organization policy:` 两个固定前缀行；本机
// 实测两者都是 "not fetched ..."（无远程层）。措辞解析未经多版本验证 → 未验证。
func remoteManagedSignal(doctorOut string) bool {
	for _, line := range strings.Split(doctorOut, "\n") {
		t := strings.TrimSpace(line)
		for _, prefix := range []string{"Managed settings (remote):", "Organization policy:"} {
			rest, ok := strings.CutPrefix(t, prefix)
			if !ok {
				continue
			}
			r := strings.ToLower(strings.TrimSpace(rest))
			if r == "" {
				continue
			}
			negative := false
			for _, n := range managedSettingsNegatives {
				if strings.Contains(r, n) {
					negative = true
					break
				}
			}
			if !negative {
				return true
			}
		}
	}
	return false
}

// markHigherLayerOverrides 把生效值写进观测投影，并给被覆盖的键打 overriddenBy 标记。
func (a *Adapter) markHigherLayerOverrides(ctx context.Context, home string, desired adapter.AgentDesiredState, proj map[string]any) error {
	over, err := a.higherLayerOverrides(ctx, home, desired)
	if err != nil {
		return err
	}
	if len(over) == 0 {
		return nil
	}
	markers := map[string]any{}
	for key, o := range over {
		markers[key] = o.Layer
		switch key {
		case keyModel:
			proj["model"] = o.Effective
		case keyProvider:
			proj["provider"] = o.Effective
		}
	}
	proj[overrideMarkerKey] = markers
	return nil
}

// overrideSet 从观测投影里取出被覆盖的 plan 键集合。
func overrideSet(proj map[string]any) map[string]bool {
	out := map[string]bool{}
	markers, _ := proj[overrideMarkerKey].(map[string]any)
	for key := range markers {
		out[key] = true
	}
	return out
}

func sortedOverrideKeys(over map[string]layerOverride) []string {
	out := make([]string, 0, len(over))
	for k := range over {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- 路径（相对注入的 home 根，护栏 #12）----

func configRoot(home string) string { return filepath.Join(home, ".claude") }

func (a *Adapter) configPath(home string) string {
	return filepath.Join(configRoot(home), "settings.json")
}
func (a *Adapter) rulesPath(home string) string {
	return filepath.Join(configRoot(home), "CLAUDE.md")
}
func (a *Adapter) skillPath(home, name string) string {
	return filepath.Join(configRoot(home), "skills", name)
}

// ---- 受管文件声明（reconciler 的备份/恢复契约，§5.3）----

// ManagedFiles 声明本适配器写入的文件（相对 home）。Skill 软链由路径体现，
// 不列入整文件备份（同批次一口径）。
func (a *Adapter) ManagedFiles(_ string) ([]string, error) {
	return []string{
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".claude", "CLAUDE.md"),
	}, nil
}

// ExtractManaged 提取受管键值（settings.json 的 `model` 与 `env.ANTHROPIC_BASE_URL`、
// CLAUDE.md 的受管块）。**未托管的 env 键（如 ANTHROPIC_AUTH_TOKEN）绝不进备份。**
func (a *Adapter) ExtractManaged(content []byte) (map[string]any, error) {
	out := map[string]any{}
	if doc, err := decodeSettings(content); err == nil && len(doc) != 0 {
		if v, ok := doc["model"]; ok {
			out["model"] = v
		}
		if env, ok := doc["env"].(map[string]any); ok {
			if v, ok := env[baseURLKey]; ok {
				out["env."+baseURLKey] = v
			}
		}
		if len(out) != 0 {
			return out, nil
		}
	}
	if block, ok, err := kit.ManagedBlockIn(string(content)); err == nil && ok {
		out["agentFleetRules"] = block
	}
	return out, nil
}

// MergeManaged 受管字段级回退（§5.3 契约 3）：只还原受管键，保留文件中其余
// （未托管）内容。未知受管键显式失败，不静默跳过。
func (a *Adapter) MergeManaged(home, relPath string, managed map[string]any) error {
	switch relPath {
	case filepath.Join(".claude", "settings.json"):
		path := a.configPath(home)
		doc, err := readSettingsFile(path)
		if err != nil {
			return err
		}
		for k, v := range managed {
			switch k {
			case "model":
				doc["model"] = v
			case "env." + baseURLKey:
				env, _ := doc["env"].(map[string]any)
				if env == nil {
					env = map[string]any{}
				}
				env[baseURLKey] = v
				doc["env"] = env
			default:
				return fmt.Errorf("claude: managed key %q does not belong to settings.json", k)
			}
		}
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		out = append(out, '\n')
		if _, err := decodeSettings(out); err != nil {
			return fmt.Errorf("claude: managed rollback produced unparseable settings: %w", err)
		}
		return kit.AtomicWrite(path, out, 0o600)
	case filepath.Join(".claude", "CLAUDE.md"):
		content, _ := managed["agentFleetRules"].(string)
		return kit.WriteManagedBlock(a.rulesPath(home), content)
	default:
		return fmt.Errorf("claude: %s is not a managed-key-restorable file", relPath)
	}
}

// ---- JSON ----

// readSettingsFile 读并解析 settings.json；文件不存在视为空对象（首次纳管）。
func readSettingsFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claude: read settings.json: %w", err)
	}
	doc, err := decodeSettings(raw)
	if err != nil {
		return nil, fmt.Errorf("claude: settings.json unparseable: %w", err)
	}
	return doc, nil
}

// decodeSettings 解析 settings.json（官方为无注释的标准 JSON）。
func decodeSettings(raw []byte) (map[string]any, error) {
	out := map[string]any{}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- 小工具 ----

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

func projectionEqual(a, b any) bool { return kit.ValuesEqual(a, b) }
