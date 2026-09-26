// Package grok 是 Grok 家族适配器（架构 v1.1.2 §5.4/§15.1；批次二片 A，KM-33）。
//
// 运行时来源与契约证据见 docs/adapters-batch-2.md §3（0/1 片 KM-32 的接入前确认）
// 与 §8（本片验收记录）：官方渠道是原生自更新二进制（https://x.ai/cli/install.sh），
// 本机实测 `grok --version` → `grok 1.0.30 (04b7ffed98c6)`、`grok version --json`
// → {"currentVersion":"1.0.30 (04b7ffed98c6)","channel":"unknown"}。
//
// 受管范围（其余键一律未托管，行级手术不触碰）：
//
//	~/.grok/config.toml            [models] default、[model.fleet]（model/base_url/
//	                               env_key/api_backend/name）、期望点名的
//	                               [mcp_servers.<name>]（含 [mcp_servers.<name>.env]）
//	~/.grok/rules/agent-fleet.md   rules 受管标记块（FR-5.1）
//	~/.grok/skills/<name>          Skill 软链（FR-6.3）
//
// 版本探测**只用 CLI**（§3.1）：优先 `grok version --json` 的 currentVersion，
// 回退 `grok --version` 的 `^grok <semver>( \(<hash>\))?$`；`~/.grok/version.json`
// 在本机落后实际二进制 5 个补丁版本，明确不作为版本来源。形态不符即显式报错。
//
// 受管键取自厂商随 CLI 发布的 26-config-reference.md 中 `Managed: user` 的行
// （该列含义是"厂商 fleet 层 vs 用户文件谁赢"）：Fleet 的受管层定位是**用户层**，
// `Managed: fleet` 的行（如 features.remote_fetch）Fleet 不写。
//
// 更高配置层（§3.4/§6 缺口 3）：`requirements.toml` 在层序上位于用户 config.toml
// 之后（26-config-reference.md「How to configure」第 3/6 层），其键会压过用户层。
// Inventory 读**实际生效值**并给被覆盖的受管键打 overriddenBy 标记；只要任一受管键
// 被覆盖，Plan 返回**空计划**（整片零写入）、HealthCheck 返回
// *HigherLayerOverrideError —— 既不报 Reconciled，也不进入"写成功→verify 失败→
// 回滚"的反复循环；代价是被 pin 期间同机 rules/skills 也一并停写（§8.6）。
// `managed_config.toml` 位于用户层
// **之前**（第 2 层），且其值只在 `Managed: fleet` 的键上胜出（厂商原文："Their value
// applies, except features.remote_fetch"），而 Fleet 只写 `Managed: user` 的键，
// 因此它不会覆盖 Fleet 的受管键（见 fixture TestManagedConfigLayerIsNotAnOverride）。
//
// envRefs 本家族原生支持（07-mcp-servers.md：`${VAR}` / `${VAR:-default}` 在
// `[mcp_servers.*]` 的 url/command/args/env 值/headers 上于加载时展开），这是批次一
// §6 缺口 3 的降级路径验证点：声明 supported 并按 `${VAR}` 渲染，值永不落盘。
//
// 归一化 MCP 契约（adapter.MCPEntry）只携带 command/args/envRefs，因此 §3.2 列出的
// url/headers/bearer_token_env_var 受管面在本片不可达 —— 已在评论里作为契约缺口上报。
package grok

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
	ID = "grok"
	// SchemaVersion 是本适配器受管 schema 版本（随 Hello 上报，FR-13.4）。
	SchemaVersion = "grok-config-v1"
	// binaryName 是可执行程序名。
	binaryName = "grok"
	// providerID 是 Fleet 在 Grok 里的模型命名空间（[model.fleet]）。
	providerID = "fleet"
	// providerName 是该模型在 picker 里的展示名（[model.fleet].name）。
	providerName = "agent-fleet"
	// apiBackend 是 Grok 的默认线协议（26-config-reference.md model.<id>.api_backend）。
	apiBackend = "chat_completions"
	// rulesFileName 是 Fleet 在 $GROK_HOME/rules/ 下自有的规则文件。
	rulesFileName = "agent-fleet.md"
	// overrideMarkerKey 是观测投影里记录"被更高配置层覆盖"的保留键。
	overrideMarkerKey = "overriddenBy"
)

// 受管 plan 键（Change.Key 与 overriddenBy 标记共用同一命名，便于 Plan 精确跳过）。
const (
	keyModel    = "config.model"
	keyProvider = "config.provider"
	keyRules    = "rules.global"
)

var (
	safeName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	envName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	semverRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$`)
	// versionLineRe 精确匹配 `grok <semver> (<hash>)`；形态不符即显式报错。
	versionLineRe = regexp.MustCompile(`^grok ([0-9]+\.[0-9]+\.[0-9]+(?:[-.+][0-9A-Za-z.-]+)?)(?: \([0-9A-Za-z]+\))?$`)
)

// parseVersionJSON 从 `grok version --json` 输出取 currentVersion 的 semver 部分
// （实测 currentVersion = "1.0.30 (04b7ffed98c6)"）。形态不符返回 ""。
func parseVersionJSON(out string) string {
	var doc struct {
		CurrentVersion string `json:"currentVersion"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		return ""
	}
	fields := strings.Fields(doc.CurrentVersion)
	if len(fields) == 0 || !semverRe.MatchString(fields[0]) {
		return ""
	}
	return fields[0]
}

// parseVersionText 解析 `grok --version` 的回退形态；形态不符返回 ""。
func parseVersionText(out string) string {
	m := versionLineRe.FindStringSubmatch(kit.FirstLine(out))
	if m == nil {
		return ""
	}
	return m[1]
}

// InspectProbe 执行 `grok inspect --json`（home 为解析根；实现负责把 GROK_HOME
// 指到 <home>/.grok）。可注入：fixture 测试不依赖真实二进制。
type InspectProbe func(ctx context.Context, home string) (string, error)

// Adapter 是 Grok 适配器。Probe/Inspect/GOOS 可注入以便 fixture 测试不依赖真实二进制。
type Adapter struct {
	Probe   kit.VersionProbe
	Inspect InspectProbe
	GOOS    string
}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) ID() string { return ID }

func (a *Adapter) goos() string {
	if a.GOOS != "" {
		return a.GOOS
	}
	return runtime.GOOS
}

// Capabilities 声明本家族逐能力支持状态与证据（矩阵「统一适配契约」）。
// §3.5 的未验证项逐条落进对应能力的 Reason/Evidence（见 docs/adapters-batch-2.md §8）。
func (a *Adapter) Capabilities() []adapter.CapabilityDecl {
	return adapter.SortDecls([]adapter.CapabilityDecl{
		{
			Capability: adapter.CapabilityVersion, State: adapter.SupportSupported,
			VerifiedVersions: "grok 1.0.30 (linux/amd64)",
			Reason: "版本**探测**已验证；安装/升级（grok update 已文档化）不在本片，Apply(version) 版本不匹配时显式失败。" +
				"版本来源只用 CLI（version --json 优先、--version 回退）；~/.grok/version.json 本机落后实际二进制，明确不用。" +
				"未验证：本机 config.toml 记 cli.installer=\"npm\" 而官方无 npm 包（来源不明）；x.ai 在本环境不可达，" +
				"安装渠道结论取自厂商随包文档与二进制内嵌 URL；macOS/Windows/WSL 无本机实测",
			Evidence: "本机实测 `grok version --json` → {\"currentVersion\":\"1.0.30 (04b7ffed98c6)\"}；" +
				"`grok --version` → `grok 1.0.30 (04b7ffed98c6)`；`cat ~/.grok/version.json` → 1.0.25（不作为来源）；" +
				"~/.grok/docs/user-guide/01-getting-started.md §Installation",
			Paths: []string{".grok/config.toml"},
		},
		{
			Capability: adapter.CapabilityModelProvider, State: adapter.SupportSupported,
			VerifiedVersions: "grok 1.0.30",
			Reason: "`[models] default` + `[model.fleet]`（model/base_url/env_key/api_backend/name），env_key 直配归一化 apiKeyEnv（只写变量名）。" +
				"`models.default` 在厂商表中 Requirements=pin，可被 requirements.toml 压过 → 观测侧读实际生效值并给可区分健康态；" +
				"被任一更高层覆盖期间本家族**整片停写**（含 rules/skills），见 docs/adapters-batch-2.md §8.6。" +
				"未验证：grok inspect --json（1.0.30）不暴露生效模型值，故本片用层序文件读生效值；" +
				"`GROK_*` 环境变量层（实测 GROK_DEFAULT_MODEL 会压过用户层）不在本片判定范围；" +
				"`managed_config.toml` / `requirements.toml` 是否纳入 Fleet 受管范围未决（本片把 Fleet 受管层定位为用户层；managed_config 只在 Managed: fleet 键上胜出，Fleet 不写该类键）；" +
				"macOS/Windows/WSL 未实测",
			Evidence: "~/.grok/docs/user-guide/11-custom-models.md（自定义端点在 [model.<name>]，env_key 优先于内联 api_key）；" +
				"26-config-reference.md `models.default`(pin/user)、`model.<id>.*`(yes/user)；" +
				"本机实测（临时 GROK_HOME，未触真实 ~/.grok）`grok models` → `Default model: fleet` / `* fleet (default)`",
			Paths: []string{".grok/config.toml"},
		},
		{
			Capability: adapter.CapabilityMCP, State: adapter.SupportSupported,
			VerifiedVersions: "grok 1.0.30",
			Reason: "command+args 支持；**envRefs 原生支持**（官方：`${VAR}`/`${VAR:-default}` 在 [mcp_servers.*] 的 url/command/args/env 值/headers 上于加载时展开），" +
				"本片按 `${VAR}` 渲染，另有 bearer_token_env_var。未验证：归一化 MCP 契约只携带 command/args/envRefs，" +
				"§3.2 列出的 url/headers/bearer_token_env_var 受管面不可达（契约缺口，已在评论上报）",
			Evidence: "~/.grok/docs/user-guide/07-mcp-servers.md（\"Grok expands string fields in [mcp_servers.*] … at load time\"）；" +
				"26-config-reference.md `mcp_servers.<name>.*`(yes/user)；本机实测 `grok mcp add --help`",
			Paths: []string{".grok/config.toml"},
		},
		{
			Capability: adapter.CapabilitySkills, State: adapter.SupportSupported,
			VerifiedVersions: "grok 1.0.30",
			Reason: "用户级 `$GROK_HOME/skills/<name>/SKILL.md` 为目标；工件物化（FetchArtifact/bundle）未实现，" +
				"缓存缺失时显式失败而不静默跳过",
			Evidence: "~/.grok/docs/user-guide/08-skills.md §Skill Locations；本机 `~/.grok/skills/` 既有条目形态",
			Paths:    []string{".grok/skills"},
		},
		{
			Capability: adapter.CapabilityRules, State: adapter.SupportSupported,
			VerifiedVersions: "grok 1.0.30",
			Reason: "用户级规则目录 `$GROK_HOME/rules/*.md`（官方：Always scanned; applies to all projects）。" +
				"Fleet 只写自有文件 `rules/agent-fleet.md` 的受管标记块，目录内其它规则文件不读不写。" +
				"未验证：`settings.json` 完整字段集未取全（本片不使用该文件）",
			Evidence: "~/.grok/docs/user-guide/12-project-rules.md（Rules Directories：`$GROK_HOME/rules/` Always scanned；每个 *.md 直接加载）",
			Paths:    []string{".grok/rules"},
		},
	})
}

// Validate 是阶段 1 的适配器侧前置校验（任何写入之前）。
func (a *Adapter) Validate(ctx context.Context, home string, desired adapter.AgentDesiredState) error {
	if err := a.checkOS(); err != nil {
		return err
	}
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
	if endpoint, _, ok := adapter.ProviderConfig(cfg); ok {
		if endpoint == "" {
			return fmt.Errorf("agent %q: provider.endpoint is required when providerRef is set", ID)
		}
		if _, hasModel := cfg["model"]; !hasModel {
			// Grok 的 BYOK 条目 [model.fleet] 必须给出送到 API 的模型 id，
			// 否则自定义端点没有可路由的模型。
			return fmt.Errorf("agent %q: config.model is required when provider is set "+
				"(grok's BYOK entry [model.%s] must name the API model id)", ID, providerID)
		}
	}
	for name, entry := range desired.MCP {
		if !safeName.MatchString(name) {
			return fmt.Errorf("agent %q: mcp entry name %q must match %s", ID, name, safeName)
		}
		if entry.Command == "" {
			return fmt.Errorf("agent %q: mcp entry %q requires command (grok's url/headers MCP entries "+
				"are not expressible by the normalized mcp contract)", ID, name)
		}
		for key, envVar := range entry.EnvRefs {
			if !envName.MatchString(key) {
				return fmt.Errorf("agent %q: mcp entry %q envRefs key %q must be an environment variable name", ID, name, key)
			}
			if !envName.MatchString(envVar) {
				return &adapter.ValidateError{
					Family: ID, Capability: adapter.CapabilityMCP, State: adapter.SupportUnverified,
					Reason: fmt.Sprintf("mcp entry %q envRefs[%s]=%q must name an environment variable "+
						"so grok can expand ${%s} at load time", name, key, envVar, envVar),
				}
			}
		}
	}
	if _, _, err := kit.ReadManagedBlock(a.rulesPath(home)); err != nil {
		return fmt.Errorf("agent %q: %w", ID, err)
	}
	raw, err := os.ReadFile(a.configPath(home))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("agent %q: read config: %w", ID, err)
	}
	if _, err := kit.ParseTOML(string(raw)); err != nil {
		return fmt.Errorf("agent %q: existing config is unparseable: %w", ID, err)
	}
	// 更高层文件必须可解析：否则无法判定生效值，宁停在阶段 1。
	if _, _, err := a.readRequirements(home); err != nil {
		return fmt.Errorf("agent %q: %w", ID, err)
	}
	return nil
}

func (a *Adapter) checkOS() error {
	switch a.goos() {
	case "linux":
		return nil
	default:
		return fmt.Errorf("agent %q: OS %q is unverified (only linux/amd64 verified on grok 1.0.30; "+
			"macOS/Windows/WSL support has no on-host evidence in this repo yet)", ID, a.goos())
	}
}

// probeVersion 探测版本：优先 `grok version --json`，回退 `grok --version`。
func (a *Adapter) probeVersion(ctx context.Context) (string, error) {
	probe := a.Probe
	if probe == nil {
		probe = kit.ExecVersionProbe
	}
	out, err := probe(ctx, binaryName, "version", "--json")
	switch {
	case err == nil:
		if v := parseVersionJSON(out); v != "" {
			return v, nil
		}
	case kit.IsNotInstalled(err):
		return "", fmt.Errorf("%w: %s", kit.ErrNotInstalled, binaryName)
	}
	out, err = probe(ctx, binaryName, "--version")
	if err != nil {
		if kit.IsNotInstalled(err) {
			return "", fmt.Errorf("%w: %s", kit.ErrNotInstalled, binaryName)
		}
		return "", fmt.Errorf("probe %s: %w", binaryName, err)
	}
	if v := parseVersionText(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("probe %s: cannot parse version from output %q", binaryName, strings.TrimSpace(out))
}

// Detect 探测已装/未装与版本（矩阵验收 1：程序缺失与版本不可解析必须可区分）。
func (a *Adapter) Detect(ctx context.Context, home string) (adapter.DetectedAgent, error) {
	d := adapter.DetectedAgent{Family: ID, ConfigPath: a.configPath(home), SchemaVer: SchemaVersion}
	v, err := a.probeVersion(ctx)
	switch {
	case errors.Is(err, kit.ErrNotInstalled):
		return d, nil // 未安装：Installed=false，不是错误
	case err != nil:
		return d, err // 版本不可解析：显式错误（不伪装成"未安装"）
	}
	d.Installed, d.Version = true, v
	return d, nil
}

// Inventory 产出 ADR-1 双侧投影。观测侧读**实际生效值**：requirements.toml 在层序上
// 压过用户 config.toml，故被覆盖的受管键用其生效值并打 overriddenBy 标记。
func (a *Adapter) Inventory(ctx context.Context, home string, desired adapter.AgentDesiredState) (adapter.AgentObservedState, error) {
	desiredProj, err := a.desiredProjection(desired)
	if err != nil {
		return adapter.AgentObservedState{}, err
	}
	cfg, err := a.readConfig(home)
	if err != nil {
		return adapter.AgentObservedState{}, err
	}
	observedProj, err := a.observedProjection(home, cfg, desired)
	if err != nil {
		return adapter.AgentObservedState{}, err
	}
	if err := a.markHigherLayerOverrides(ctx, home, desired, observedProj); err != nil {
		return adapter.AgentObservedState{}, err
	}
	return adapter.AgentObservedState{
		Family:                   ID,
		Version:                  a.probeVersionQuiet(ctx),
		ManagedProjection:        observedProj,
		DesiredProjectionDigest:  kit.ProjectionDigest(desiredProj),
		ObservedProjectionDigest: kit.ProjectionDigest(observedProj),
		CanonicalizationVersion:  adapter.ProjectionCanonicalizationVersion,
		ConfigPath:               a.configPath(home),
	}, nil
}

// probeVersionQuiet 供 Inventory 使用：未安装或不可解析都返回 ""（此时 drift/plan
// 会走版本步骤并由 HealthCheck/Apply 显式失败）。
func (a *Adapter) probeVersionQuiet(ctx context.Context) string {
	v, err := a.probeVersion(ctx)
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
		return nil, fmt.Errorf("grok: desired config is not a JSON object")
	}
	if model, ok := cfg["model"]; ok {
		// 模型侧受管两件事：默认选择器指向 Fleet 命名空间，且该命名空间的
		// `model` 字段等于归一化模型 id（26-config-reference.md）。
		proj["model"] = map[string]any{"default": providerID, "model": model}
	}
	if endpoint, keyEnv, ok := adapter.ProviderConfig(cfg); ok {
		// api_backend/name 也是受管键：必须进投影，否则写而不可观测（用户改掉不触发 drift）。
		proj["provider"] = map[string]any{"endpoint": endpoint, "apiKeyEnv": keyEnv,
			"apiBackend": apiBackend, "name": providerName}
	}
	if len(desired.MCP) != 0 {
		mcp := map[string]any{}
		for name, e := range desired.MCP {
			// env 始终参与投影（可为空表）：Fleet 对点名条目的 env 子表拥有所有权，
			// 期望不再点名 env 时必须能把"陈旧 env 子表"判成 drift 并清理。
			mcp[name] = map[string]any{"command": e.Command, "args": normalizeArgs(e.Args),
				"env": renderEnvRefs(e.EnvRefs)}
		}
		proj["mcp"] = mcp
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

func (a *Adapter) observedProjection(home string, cfg map[string]any, desired adapter.AgentDesiredState) (map[string]any, error) {
	proj := map[string]any{}
	dcfg := adapter.ConfigMap(desired)
	models, _ := cfg["models"].(map[string]any)
	if _, ok := dcfg["model"]; ok {
		defaultID, _ := models["default"].(string)
		proj["model"] = map[string]any{"default": defaultID, "model": stringOf(tableAt(cfg, "model", providerID)["model"])}
	}
	if _, _, ok := adapter.ProviderConfig(dcfg); ok {
		table := tableAt(cfg, "model", providerID)
		proj["provider"] = map[string]any{
			"endpoint":   stringOf(table["base_url"]),
			"apiKeyEnv":  envKeyOf(table["env_key"]),
			"apiBackend": stringOf(table["api_backend"]),
			"name":       stringOf(table["name"]),
		}
	}
	if len(desired.MCP) != 0 {
		mcp := map[string]any{}
		servers, _ := cfg["mcp_servers"].(map[string]any)
		for name := range desired.MCP {
			entry, ok := servers[name].(map[string]any)
			if !ok {
				continue // 未注册的受管条目：投影缺席 → 摘要不等 → drift
			}
			// env 子表整表归 Fleet 所有（点名条目）：观测侧读全部 env 键，
			// 与期望侧对称，才能发现"期望不再点名 env"这一收敛步骤。
			mcp[name] = map[string]any{"command": stringOf(entry["command"]),
				"args": stringSlice(entry["args"]), "env": envMapOf(entry["env"])}
		}
		proj["mcp"] = mcp
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
// 只要任一受管键被更高配置层覆盖，整片返回空计划（复核 B3）：写用户层是徒劳的，
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
		// 复核 B3：任一受管键被更高层覆盖即整片停写。只跳过被覆盖的键是不够的——
		// 同机 rules/skills 的 drift 仍会触发写入，而阶段 10 的 HealthCheck 必然返回
		// 覆盖错误 → reconciler 回滚本轮全部写入 → 下一轮重现（写→失败→回滚循环）。
		// 空计划 + 可区分健康失败 = 一次干净、零写入、不产生备份/回滚的停机。
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
	changes = append(changes, kit.DiffMapStep(ID, "mcp", "mcp.", desiredProj["mcp"], observedProj["mcp"])...)
	changes = append(changes, kit.DiffMapStep(ID, "skills", "skill:", desiredProj["skills"], observedProj["skills"])...)
	if !projectionEqual(desiredProj["rules"], observedProj["rules"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "rules", Key: keyRules,
			From: observedProj["rules"], To: desiredProj["rules"]})
	}
	return changes, nil
}

// Apply 执行变更（阶段 5–9）：行级手术合并写（只写受管键/受管条目，其余原样保留）、
// 原子写、Skill 软链、rules 受管块。空变更零写入（幂等）。
func (a *Adapter) Apply(ctx context.Context, home string, desired adapter.AgentDesiredState, changes []adapter.Change) error {
	byStep := map[string][]adapter.Change{}
	for _, c := range changes {
		byStep[c.Step] = append(byStep[c.Step], c)
	}
	if cs := byStep["version"]; len(cs) != 0 {
		// 版本安装由 command installer（§20.2）负责，本片未实现该路径：
		// 安装后再探测验证（FR-2.3），不匹配即显式失败，绝不谎报成功。
		v := a.probeVersionQuiet(ctx)
		if v != desired.Version {
			return fmt.Errorf("grok: version %s is required but %q is installed and the command installer "+
				"(§20.2) is not wired in this slice", desired.Version, v)
		}
	}
	configChanges, mcpChanges := byStep["config"], byStep["mcp"]
	if len(configChanges) != 0 || len(mcpChanges) != 0 {
		// 防御：计划可能来自被更高层覆盖的键（陈旧 plan）——写前再过滤一次。
		over, err := a.higherLayerOverrides(ctx, home, desired)
		if err != nil {
			return fmt.Errorf("grok: %w", err)
		}
		configChanges = dropOverridden(configChanges, over)
		mcpChanges = dropOverridden(mcpChanges, over)
		if len(configChanges) != 0 || len(mcpChanges) != 0 {
			if err := a.applyConfig(home, desired, configChanges, mcpChanges); err != nil {
				return err
			}
		}
	}
	for _, c := range byStep["skills"] {
		name := strings.TrimPrefix(c.Key, "skill:")
		digest := fmt.Sprintf("%v", c.To)
		if err := kit.EnsureSkillLink(a.skillPath(home, name), name, digest, kit.SkillCacheDir(home, name, digest)); err != nil {
			return fmt.Errorf("grok: %w", err)
		}
	}
	if len(byStep["rules"]) != 0 {
		if err := kit.WriteManagedBlock(a.rulesPath(home), kit.RulesContent(desired.Rules)); err != nil {
			return fmt.Errorf("grok: rules: %w", err)
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

// applyConfig 用行级手术写入受管键与受管 MCP 表：保留注释、键序与未托管内容。
// 写后重新解析验证（§5.5）；解析失败即失败，不留半写文件（原子写 + 校验）。
func (a *Adapter) applyConfig(home string, desired adapter.AgentDesiredState, configChanges, mcpChanges []adapter.Change) error {
	path := a.configPath(home)
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("grok: read config: %w", err)
	}
	doc := string(raw)
	if _, err := kit.ParseTOML(doc); err != nil {
		return fmt.Errorf("grok: existing config is unparseable, refusing to merge: %w", err)
	}
	cfg := adapter.ConfigMap(desired)
	keys := map[string]map[string]any{}
	removeKeys := map[string][]string{}
	var remove []string
	if model, ok := cfg["model"]; ok {
		// 默认选择器指向 Fleet 命名空间（本机实测 `grok models` 的
		// "Default model: fleet" / "* fleet (default)"）；[model.fleet].model 是
		// 送到 API 的模型 id，期望侧受管键必须成对写入才能收敛。
		keys["models"] = map[string]any{"default": providerID}
		table := map[string]any{"name": providerName, "api_backend": apiBackend, "model": model}
		if endpoint, keyEnv, ok := adapter.ProviderConfig(cfg); ok {
			if endpoint != "" {
				table["base_url"] = endpoint
			}
			if keyEnv != "" {
				table["env_key"] = keyEnv
			}
		}
		keys["model."+providerID] = table
	} else if endpoint, keyEnv, ok := adapter.ProviderConfig(cfg); ok {
		// provider 无 model 由 Validate 拒绝；此处保留防御性写入路径。
		table := map[string]any{"name": providerName, "api_backend": apiBackend}
		if endpoint != "" {
			table["base_url"] = endpoint
		}
		if keyEnv != "" {
			table["env_key"] = keyEnv
		}
		keys["model."+providerID] = table
	}
	for _, c := range mcpChanges {
		name := strings.TrimPrefix(c.Key, "mcp.")
		entry, ok := desired.MCP[name]
		if !ok {
			continue
		}
		keys["mcp_servers."+name] = map[string]any{"command": entry.Command, "args": normalizeArgs(entry.Args)}
		// env 可能写成子表头 [mcp_servers.x.env]，也可能写成表内点号键
		// `env.TOKEN = "..."`（grok mcp add 一类工具如此落盘）。两种形态都要清掉，
		// 否则期望不再点名 env 时观测侧永远非空、每轮重排同一条变更（复核 B2）。
		removeKeys["mcp_servers."+name] = []string{"env"}
		if len(entry.EnvRefs) != 0 {
			keys["mcp_servers."+name+".env"] = renderEnvRefs(entry.EnvRefs)
		} else {
			// 之前写过 env、现在期望不再点名：清理该受管子表，不留陈旧受管内容。
			remove = append(remove, "mcp_servers."+name+".env")
		}
	}
	out, err := kit.PatchTOML(doc, kit.TOMLPatch{Keys: keys, Remove: remove, RemoveKeys: removeKeys})
	if err != nil {
		return fmt.Errorf("grok: patch config: %w", err)
	}
	patched, err := kit.ParseTOML(out)
	if err != nil {
		return fmt.Errorf("grok: patched config does not parse, aborting write: %w", err)
	}
	// 写后断言受管 mcp env 已收敛（复核 B2 的显式失败安全网）：点号键/子表两种形态
	// 任一残留都会让随后每轮 reconcile 重排同一条变更，故宁可在此显式失败。
	for _, c := range mcpChanges {
		name := strings.TrimPrefix(c.Key, "mcp.")
		entry, ok := desired.MCP[name]
		if !ok {
			continue
		}
		got := envMapOf(tableAt(patched, "mcp_servers", name)["env"])
		want := renderEnvRefs(entry.EnvRefs)
		if !kit.ValuesEqual(got, want) {
			return fmt.Errorf("grok: patched mcp server %q env is %v, want %v; refusing a write that will not converge",
				name, got, want)
		}
	}
	return kit.AtomicWrite(path, []byte(out), 0o600)
}

// HealthCheck（阶段 10）：先给"被更高层覆盖"这一可区分健康态，再验证配置可解析、
// 受管键到位、版本可解析且与期望一致。只验证语法/注册，不验证外部服务可用性（FR-4.3）。
func (a *Adapter) HealthCheck(ctx context.Context, home string, desired adapter.AgentDesiredState) error {
	over, err := a.higherLayerOverrides(ctx, home, desired)
	if err != nil {
		return fmt.Errorf("grok: health: %w", err)
	}
	if len(over) != 0 {
		key := sortedOverrideKeys(over)[0]
		o := over[key]
		return &HigherLayerOverrideError{Family: ID, Key: key, Layer: o.Layer, Effective: o.Effective, Wanted: o.Wanted}
	}
	path := a.configPath(home)
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("grok: health: read config: %w", err)
	}
	cfg, err := kit.ParseTOML(string(raw))
	if err != nil {
		return fmt.Errorf("grok: health: config unparseable: %w", err)
	}
	dcfg := adapter.ConfigMap(desired)
	if model, ok := dcfg["model"]; ok {
		models, _ := cfg["models"].(map[string]any)
		if got, present := models["default"]; !present || fmt.Sprintf("%v", got) != providerID {
			return fmt.Errorf("grok: health: [models] default is %v, want %q", got, providerID)
		}
		table := tableAt(cfg, "model", providerID)
		if got := stringOf(table["model"]); got != fmt.Sprintf("%v", model) {
			return fmt.Errorf("grok: health: [model.%s] model is %q, want %v", providerID, got, model)
		}
	}
	if endpoint, keyEnv, ok := adapter.ProviderConfig(dcfg); ok {
		table := tableAt(cfg, "model", providerID)
		if got := stringOf(table["base_url"]); got != endpoint {
			return fmt.Errorf("grok: health: [model.%s] base_url is %q, want %s", providerID, got, endpoint)
		}
		if keyEnv != "" && envKeyOf(table["env_key"]) != keyEnv {
			return fmt.Errorf("grok: health: [model.%s] env_key is not in place", providerID)
		}
		if got := stringOf(table["api_backend"]); got != apiBackend {
			return fmt.Errorf("grok: health: [model.%s] api_backend is %q, want %q", providerID, got, apiBackend)
		}
		if got := stringOf(table["name"]); got != providerName {
			return fmt.Errorf("grok: health: [model.%s] name is %q, want %q", providerID, got, providerName)
		}
	}
	for name, entry := range desired.MCP {
		servers, _ := cfg["mcp_servers"].(map[string]any)
		got, ok := servers[name].(map[string]any)
		if !ok {
			return fmt.Errorf("grok: health: mcp server %q is not registered after apply", name)
		}
		if stringOf(got["command"]) != entry.Command {
			return fmt.Errorf("grok: health: mcp server %q command is not in place", name)
		}
	}
	if desired.Version != "" {
		v, err := a.probeVersion(ctx)
		if err != nil {
			return fmt.Errorf("grok: health: version probe failed: %w", err)
		}
		if v != desired.Version {
			return fmt.Errorf("grok: health: installed version %s != desired %s", v, desired.Version)
		}
	}
	for name, s := range desired.Skills {
		if got := kit.ReadSkillDigest(a.skillPath(home, name)); got != s.ContentDigest {
			return fmt.Errorf("grok: health: skill %q link digest is %q, want %q", name, got, s.ContentDigest)
		}
	}
	if kit.RulesContent(desired.Rules) != "" {
		content, ok, err := kit.ReadManagedBlock(a.rulesPath(home))
		if err != nil {
			return fmt.Errorf("grok: health: %w", err)
		}
		if !ok || content != kit.RulesContent(desired.Rules) {
			return fmt.Errorf("grok: health: rules managed block is missing or differs")
		}
	}
	return nil
}

// ---- 更高配置层（§3.4/§6 缺口 3）----

// HigherLayerOverrideError 表示受管键被更高的配置层覆盖：Fleet 的用户层写入
// 无法生效（requirements.toml 在层序上位于用户 config.toml 之后）。这是**可区分的
// 健康态**——既不是 Reconciled，也不是"写成功→verify 失败→回滚"的反复循环。
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
		"organization pin — resolve on the policy layer or stop managing this key",
		e.Family, e.Key, e.Layer, e.Effective, e.Wanted)
}

// layerOverride 是单个受管键的覆盖记录。
type layerOverride struct {
	Effective any
	Wanted    any
	Layer     string
}

// inspectLayer 是 `grok inspect --json` 的 configSources.layers 条目。
type inspectLayer struct {
	Role string `json:"role"`
	Path string `json:"path"`
}

// inspectLayers 尽力读取生效层的角色列表。失败（未安装/超时/结构变化）返回 nil：
// 观测不得因健康检查命令不可用而整体失败，层判定随后回落到 home 内文件。
func (a *Adapter) inspectLayers(ctx context.Context, home string) []inspectLayer {
	run := a.Inspect
	if run == nil {
		run = defaultInspect
	}
	out, err := run(ctx, home)
	if err != nil {
		return nil
	}
	var doc struct {
		ConfigSources struct {
			Layers []inspectLayer `json:"layers"`
		} `json:"configSources"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return nil
	}
	return doc.ConfigSources.Layers
}

// defaultInspect 执行 `grok inspect --json`，把 GROK_HOME 指到 <home>/.grok
// （护栏 #12：绝不落到真实 ~/.grok）。
func defaultInspect(ctx context.Context, home string) (string, error) {
	cmd := exec.CommandContext(ctx, binaryName, "inspect", "--json")
	cmd.Env = append(os.Environ(), "GROK_HOME="+filepath.Join(home, ".grok"))
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// higherLayerOverrides 计算"哪些受管键的实际生效值来自更高层"。
//
// 层序（26-config-reference.md「How to configure」）：2. managed_config.toml →
// 3. 用户 config.toml → 6. requirements.toml。因此 requirements.toml 是唯一能压过
// Fleet 用户层的**文件**层；managed_config.toml 在用户层之前，且其值只在
// `Managed: fleet` 的键上胜出（Fleet 只写 `Managed: user` 的键），故不构成覆盖。
//
// 只有"requirements 层确实设了该键、且其值不同于期望"才算覆盖；用户层自己的
// 值不一致属普通 drift，由 Plan/Apply 正常收敛。
//
// 生效值从 home 内的 requirements.toml 读取；若 inspect 报告存在 requirements 层
// 但该层路径不在 $GROK_HOME 内（/etc/grok/requirements.toml 或 MDM 下发），值不可读
// ——仍如实报"被覆盖"。
//
// 【复核 B1 修正】"文件存在"与"文件里有键"必须分开判定：
//   - 空文件/仅注释：真实 `grok inspect --json` 会报一个 role=requirements、
//     path 就在 $GROK_HOME **内**、note=empty 的层。旧判定 `len(req)==0` 会落到
//     outsideHomeOverrides，把一个空占位文件报成"$GROK_HOME 外的高层覆盖"，
//     从此整片永不写、错误文案还把层指错。
//   - 因此：home 内文件存在 → 只用它实际设置的键判定（空层 = 无覆盖）；
//     home 内文件不存在、且 inspect 报出的 requirements 层路径**不等于** home 路径
//     时，才按"home 外高层的值不可读"处理。
func (a *Adapter) higherLayerOverrides(ctx context.Context, home string, desired adapter.AgentDesiredState) (map[string]layerOverride, error) {
	dcfg := adapter.ConfigMap(desired)
	if len(dcfg) == 0 && len(desired.MCP) == 0 {
		return nil, nil
	}
	cfg, err := a.readConfig(home)
	if err != nil {
		// 可解析性由调用方另报；此处只做生效值判定。
		cfg = map[string]any{}
	}
	req, reqExists, err := a.readRequirements(home)
	if err != nil {
		return nil, err
	}
	if reqExists {
		// 文件在 $GROK_HOME 内：即使只有注释/为空，也只看它设了哪些键。
		return requirementOverrides(cfg, req, desired), nil
	}
	if outside := outsideRequirementsLayer(a.inspectLayers(ctx, home), requirementsPath(home)); outside != "" {
		return outsideHomeOverrides(dcfg, desired), nil
	}
	return nil, nil
}

// requirementOverrides 把 requirements.toml 里"确实设了且与期望不同"的受管键记为覆盖，
// 生效值取 requirements 层（层序更高）。
func requirementOverrides(cfg, req map[string]any, desired adapter.AgentDesiredState) map[string]layerOverride {
	const layer = "requirements.toml"
	out := map[string]layerOverride{}
	dcfg := adapter.ConfigMap(desired)
	reqModels, _ := req["models"].(map[string]any)
	reqFleet := tableAt(req, "model", providerID)
	cfgFleet := tableAt(cfg, "model", providerID)
	cfgModels, _ := cfg["models"].(map[string]any)

	if wantModel, ok := dcfg["model"]; ok {
		eff := map[string]any{"default": stringOf(cfgModels["default"]), "model": stringOf(cfgFleet["model"])}
		want := map[string]any{"default": providerID, "model": wantModel}
		changed := false
		if reqModels != nil {
			if v, present := reqModels["default"]; present {
				eff["default"] = stringOf(v)
				if !kit.ValuesEqual(stringOf(v), providerID) {
					changed = true
				}
			}
		}
		if reqFleet != nil {
			if v, present := reqFleet["model"]; present {
				eff["model"] = stringOf(v)
				if !kit.ValuesEqual(stringOf(v), wantModel) {
					changed = true
				}
			}
		}
		if changed {
			out[keyModel] = layerOverride{Effective: eff, Wanted: want, Layer: layer}
		}
	}
	if endpoint, keyEnv, ok := adapter.ProviderConfig(dcfg); ok {
		eff := map[string]any{"endpoint": stringOf(cfgFleet["base_url"]), "apiKeyEnv": envKeyOf(cfgFleet["env_key"]),
			"apiBackend": stringOf(cfgFleet["api_backend"]), "name": stringOf(cfgFleet["name"])}
		want := map[string]any{"endpoint": endpoint, "apiKeyEnv": keyEnv,
			"apiBackend": apiBackend, "name": providerName}
		changed := false
		if reqFleet != nil {
			if v, present := reqFleet["base_url"]; present {
				eff["endpoint"] = stringOf(v)
				if !kit.ValuesEqual(stringOf(v), endpoint) {
					changed = true
				}
			}
			if v, present := reqFleet["env_key"]; present {
				eff["apiKeyEnv"] = envKeyOf(v)
				if !kit.ValuesEqual(envKeyOf(v), keyEnv) {
					changed = true
				}
			}
			if v, present := reqFleet["api_backend"]; present {
				eff["apiBackend"] = stringOf(v)
				if !kit.ValuesEqual(stringOf(v), apiBackend) {
					changed = true
				}
			}
			if v, present := reqFleet["name"]; present {
				eff["name"] = stringOf(v)
				if !kit.ValuesEqual(stringOf(v), providerName) {
					changed = true
				}
			}
		}
		if changed {
			out[keyProvider] = layerOverride{Effective: eff, Wanted: want, Layer: layer}
		}
	}
	for name, e := range desired.MCP {
		reqEntry := tableAt(req, "mcp_servers", name)
		if reqEntry == nil {
			continue
		}
		cfgEntry := tableAt(cfg, "mcp_servers", name)
		eff := map[string]any{"command": stringOf(cfgEntry["command"]), "args": stringSlice(cfgEntry["args"]),
			"env": envMapOf(cfgEntry["env"])}
		want := map[string]any{"command": e.Command, "args": normalizeArgs(e.Args), "env": renderEnvRefs(e.EnvRefs)}
		changed := false
		if v, present := reqEntry["command"]; present {
			eff["command"] = stringOf(v)
			if !kit.ValuesEqual(stringOf(v), e.Command) {
				changed = true
			}
		}
		if v, present := reqEntry["args"]; present {
			eff["args"] = stringSlice(v)
			if !kit.ValuesEqual(stringSlice(v), normalizeArgs(e.Args)) {
				changed = true
			}
		}
		if v, present := reqEntry["env"]; present {
			eff["env"] = envMapOf(v)
			if !kit.ValuesEqual(envMapOf(v), want["env"]) {
				changed = true
			}
		}
		if changed {
			out["mcp."+name] = layerOverride{Effective: eff, Wanted: want, Layer: layer}
		}
	}
	return out
}

// outsideHomeOverrides：inspect 报告有 requirements 层，但 $GROK_HOME 内没有该文件
// （/etc/grok/requirements.toml 或 macOS MDM）。生效值不可读 → 合法地报"被覆盖、
// 值未知"，让上层看到可区分状态而不是无休止的 drift 循环。
func outsideHomeOverrides(dcfg map[string]any, desired adapter.AgentDesiredState) map[string]layerOverride {
	const layer = "requirements.toml (outside $GROK_HOME, e.g. /etc/grok or MDM)"
	const unknown = "unknown (layer outside $GROK_HOME)"
	out := map[string]layerOverride{}
	if wantModel, ok := dcfg["model"]; ok {
		out[keyModel] = layerOverride{Effective: unknown,
			Wanted: map[string]any{"default": providerID, "model": wantModel}, Layer: layer}
	}
	if endpoint, keyEnv, ok := adapter.ProviderConfig(dcfg); ok {
		out[keyProvider] = layerOverride{Effective: unknown,
			Wanted: map[string]any{"endpoint": endpoint, "apiKeyEnv": keyEnv,
				"apiBackend": apiBackend, "name": providerName}, Layer: layer}
	}
	for name, e := range desired.MCP {
		out["mcp."+name] = layerOverride{Effective: unknown,
			Wanted: map[string]any{"command": e.Command, "args": normalizeArgs(e.Args),
				"env": renderEnvRefs(e.EnvRefs)}, Layer: layer}
	}
	return out
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
		switch {
		case key == keyModel:
			proj["model"] = o.Effective
		case key == keyProvider:
			proj["provider"] = o.Effective
		default:
			if name, ok := strings.CutPrefix(key, "mcp."); ok {
				if mcp, ok := proj["mcp"].(map[string]any); ok {
					mcp[name] = o.Effective
				}
			}
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

// outsideRequirementsLayer 返回 inspect 报告里 role=requirements 且路径**不等于**
// home 内 requirements.toml 的那一层的路径；没有则返回 ""（含 inspect 不可用）。
// 用于区分"$GROK_HOME 内的高层文件"与"$GROK_HOME 外（/etc/grok、MDM）的层"（复核 B1）。
func outsideRequirementsLayer(layers []inspectLayer, homePath string) string {
	for _, l := range layers {
		if l.Role != "requirements" {
			continue
		}
		if l.Path == "" || filepath.Clean(l.Path) == filepath.Clean(homePath) {
			continue
		}
		return l.Path
	}
	return ""
}

// ---- 路径（相对注入的 home 根，护栏 #12）----

func configRoot(home string) string { return filepath.Join(home, ".grok") }
func (a *Adapter) configPath(home string) string {
	return filepath.Join(configRoot(home), "config.toml")
}
func (a *Adapter) rulesPath(home string) string {
	return filepath.Join(configRoot(home), "rules", rulesFileName)
}
func (a *Adapter) skillPath(home, name string) string {
	return filepath.Join(configRoot(home), "skills", name)
}
func requirementsPath(home string) string {
	return filepath.Join(configRoot(home), "requirements.toml")
}

// readRequirements 读 home 内的 requirements.toml；第二个返回值表示**文件是否存在**
// （与"文件存在但无键/仅注释"必须区分，复核 B1）。
func (a *Adapter) readRequirements(home string) (map[string]any, bool, error) {
	raw, err := os.ReadFile(requirementsPath(home))
	if os.IsNotExist(err) {
		return map[string]any{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read requirements.toml: %w", err)
	}
	cfg, err := kit.ParseTOML(string(raw))
	if err != nil {
		return nil, true, fmt.Errorf("requirements.toml is unparseable: %w", err)
	}
	return cfg, true, nil
}

// ---- 受管文件声明（reconciler 的备份/恢复契约，§5.3）----

// ManagedFiles 声明本适配器写入的文件（相对 home）。
func (a *Adapter) ManagedFiles(home string) ([]string, error) {
	return []string{
		filepath.Join(".grok", "config.toml"),
		filepath.Join(".grok", "rules", rulesFileName),
	}, nil
}

// ExtractManaged 提取受管键值（[models] 的受管键、[model.fleet] 表、rules 受管块；
// 软链由路径体现）。MCP 条目名只有期望侧知道，故不在此提取（避免回退时误动
// 用户自有的 [mcp_servers.*]）。
func (a *Adapter) ExtractManaged(content []byte) (map[string]any, error) {
	cfg, err := kit.ParseTOML(string(content))
	if err != nil {
		// 非 TOML（如 rules 文件）：受管对象是受管块内容。
		if block, ok, blockErr := kit.ManagedBlockIn(string(content)); blockErr == nil && ok {
			return map[string]any{"agentFleetRules": block}, nil
		}
		return map[string]any{}, nil
	}
	out := map[string]any{}
	if table, ok := cfg["models"].(map[string]any); ok {
		if v, present := table["default"]; present {
			out["models"] = map[string]any{"default": v}
		}
	}
	if table := tableAt(cfg, "model", providerID); table != nil {
		out["model."+providerID] = table
	}
	return out, nil
}

// MergeManaged 受管字段级回退（§5.3 契约 3）：只还原受管键，保留文件中其余
// （未托管）内容与未托管表内键。rules 文件按受管块回退。
func (a *Adapter) MergeManaged(home, relPath string, managed map[string]any) error {
	switch relPath {
	case filepath.Join(".grok", "config.toml"):
		keys := map[string]map[string]any{}
		for k, v := range managed {
			table, ok := v.(map[string]any)
			if !ok {
				// 受管值一律是表（[models]/[model.fleet]）；形态不符宁可显式失败，
				// 不静默跳过（与 default 分支、矩阵"未验证即拒绝"的口径一致）。
				return fmt.Errorf("grok: managed config value for %q is %T, want a table", k, v)
			}
			keys[k] = table
		}
		raw, err := os.ReadFile(a.configPath(home))
		if err != nil {
			return fmt.Errorf("grok: read config for managed rollback: %w", err)
		}
		out, err := kit.PatchTOML(string(raw), kit.TOMLPatch{Keys: keys})
		if err != nil {
			return err
		}
		if _, err := kit.ParseTOML(out); err != nil {
			return fmt.Errorf("grok: managed rollback produced unparseable config: %w", err)
		}
		return kit.AtomicWrite(a.configPath(home), []byte(out), 0o600)
	case filepath.Join(".grok", "rules", rulesFileName):
		content, _ := managed["agentFleetRules"].(string)
		return kit.WriteManagedBlock(a.rulesPath(home), content)
	default:
		return fmt.Errorf("grok: %s is not a managed-key-restorable file", relPath)
	}
}

// ---- 小工具 ----

func normalizeArgs(args []string) []string { return append([]string{}, args...) }

// renderEnvRefs 把归一化 envRefs 渲染为 Grok 原生 `${VAR}` 间接引用
// （07-mcp-servers.md：Grok 在加载时展开 [mcp_servers.*] 的 env 值）。
// 只写变量名，值永不入期望状态（FR-3.3）。
func renderEnvRefs(refs map[string]string) map[string]any {
	out := map[string]any{}
	for key, name := range refs {
		out[key] = "${" + name + "}"
	}
	return out
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

func envKeyOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		if len(t) > 0 {
			return stringOf(t[0])
		}
	case []string:
		if len(t) > 0 {
			return t[0]
		}
	}
	return ""
}

// envMapOf 把解析出的 env 子表归一化为 map[string]any（缺席即空表）。
func envMapOf(v any) map[string]any {
	out := map[string]any{}
	if m, ok := v.(map[string]any); ok {
		for k, val := range m {
			out[k] = val
		}
	}
	return out
}

func stringSlice(v any) []string {
	out := []string{}
	if raw, ok := v.([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// tableAt 返回 cfg 的嵌套表；任一层缺失或不是表则返回 nil。
func tableAt(cfg map[string]any, path ...string) map[string]any {
	cur := cfg
	for i, p := range path {
		v, ok := cur[p]
		if !ok {
			return nil
		}
		table, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		if i == len(path)-1 {
			return table
		}
		cur = table
	}
	return nil
}

// readConfig 读并解析 config.toml；文件不存在视为空文档（首次纳管）。
func (a *Adapter) readConfig(home string) (map[string]any, error) {
	raw, err := os.ReadFile(a.configPath(home))
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("grok: read config: %w", err)
	}
	cfg, err := kit.ParseTOML(string(raw))
	if err != nil {
		return nil, fmt.Errorf("grok: config unparseable: %w", err)
	}
	return cfg, nil
}

func projectionEqual(a, b any) bool { return kit.ValuesEqual(a, b) }
