// Package hermes 是 Hermes（Nous Research `hermes-agent`，可执行 `hermes`）家族适配器
// （批次二片 C，KM-35）。运行时来源与契约证据见 docs/adapters-batch-2.md §4（0/1 片
// KM-32 的接入前确认）与 §10（本片验收记录）；本机实测版本 0.21.2、config 版本 44。
//
// 单一真源：`$HERMES_HOME/config.yaml`（默认 `~/.hermes/config.yaml`），YAML 带
// `_config_version`。受管范围只按叶子键路径声明，其余键（`agent`/`terminal`/`display`
// 等 60 余个顶层键）逐字节保留：
//
//	model.default、model.provider（custom）、model.base_url（= 归一化 provider.endpoint）
//	model.api_key（= `${<归一化 provider.apiKeyEnv>}` 环境变量间接引用；值永不落盘）
//	mcp_servers.<name>.command / .args（仅期望点名的条目）
//	skills/<name>（Skill 软链；`skills.external_dirs` 见下）
//
// 嵌套键级所有权（硬要求）：`delegation.api_key`、`auxiliary.*.api_key`、
// `secrets.bitwarden.*`、`dashboard.basic_auth.*`、顶层 `HTTP_PROXY`/`HTTPS_PROXY`
// 与受管键同处一个文档——适配器只按叶子路径读写，绝不整树 Decode，这些键既不进
// 期望状态也不进观测投影。`~/.hermes/.env` 与 `~/.hermes/auth.json` 完全不读。
//
// `SOUL.md` 是用户身份/人格，本片明确不纳入受管（Rules 能力声明 unsupported，
// 见 §4.2/§4.4 第 8 项）。
//
// 版本探测只用 `hermes --version` / `-V`：多行输出，首行严格匹配
// `Hermes Agent v<semver> (<date>) · upstream <sha>`；`hermes version` 不是子命令
// （invalid choice，退出码 2），`upstream <sha>` 是 checkout 提交号而非产品版本。
// 线上文档未记载该多行格式，形态不符即显式报错；`_config_version` 变化同样显式失败，
// 不静默降级。
package hermes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/kit"
)

const (
	// ID 是家族标识（矩阵统一名称）。
	ID = "hermes"
	// SchemaVersion 是本适配器受管 schema 版本。
	SchemaVersion = "hermes-config-v1"
	// binaryName 是可执行程序名。
	binaryName = "hermes"
	// customProvider 是 Hermes 承载任意 OpenAI 兼容端点的 provider 取值
	// （cli-config.yaml.example §Model Configuration："custom" - Any other
	// OpenAI-compatible endpoint. Set base_url below.）。
	customProvider = "custom"
	// supportedConfigVersion 是本适配器已对位的 config.yaml `_config_version`。
	supportedConfigVersion = 44
)

// versionArgs 是版本探测参数（`hermes version` 不是子命令，只能用长/短旗标）。
var versionArgs = []string{"--version"}

var (
	// mcpNameRe 限定 MCP 条目名：点号会与 YAML 点分路径冲突，故不允许。
	mcpNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	// skillNameRe 是 Skill 目录名（软链末端）。
	skillNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// envNameRe 是环境变量名（归一化 apiKeyEnv 与 envRefs 变量名）。
	envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// versionLineRe 严格匹配 `hermes --version` 首行；形态不符即显式报错。
	versionLineRe = regexp.MustCompile(`^Hermes Agent v([0-9]+\.[0-9]+\.[0-9]+(?:[-.+][0-9A-Za-z.-]+)?) \([0-9]{4}\.[0-9]+\.[0-9]+\) · upstream [0-9a-f]{7,40}$`)
	// envRefRe 从 `model.api_key` 取出环境变量名（支持 `${VAR}` 与 `${env:VAR}`）。
	envRefRe = regexp.MustCompile(`^\$\{(?:env:)?([A-Za-z_][A-Za-z0-9_]*)\}$`)
	// configVersionRe 从 `hermes config check` 输出取配置版本号。
	configVersionRe = regexp.MustCompile(`(?m)Config version:\s*([0-9]+)`)
)

// parseVersion 严格解析 `hermes --version` 首行；形态不符返回 ""（可区分"未装"）。
func parseVersion(out string) string {
	m := versionLineRe.FindStringSubmatch(kit.FirstLine(out))
	if m == nil {
		return ""
	}
	return m[1]
}

// ConfigCheckProbe 执行 `hermes config check`（只读；注入以便 fixture 测试不依赖真实二进制）。
type ConfigCheckProbe func(ctx context.Context, home string) (string, error)

// Adapter 是 Hermes 适配器。Probe/ConfigCheck/GOOS 可注入以便测试。
type Adapter struct {
	Probe       kit.VersionProbe
	ConfigCheck ConfigCheckProbe
	GOOS        string
}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) ID() string { return ID }

func (a *Adapter) goos() string {
	if a.GOOS != "" {
		return a.GOOS
	}
	return runtime.GOOS
}

// Capabilities 声明逐能力支持状态与证据。§4.4 的未验证项逐条落进对应 Reason/Evidence。
func (a *Adapter) Capabilities() []adapter.CapabilityDecl {
	return adapter.SortDecls([]adapter.CapabilityDecl{
		{
			Capability: adapter.CapabilityVersion, State: adapter.SupportSupported,
			VerifiedVersions: "Hermes Agent 0.21.2 (linux/amd64, git checkout)",
			Reason: "版本**探测**已验证（`hermes --version` 多行输出首行 `Hermes Agent v<semver> (<date>) · upstream <sha>`，" +
				"按行严格匹配，形态不符显式报错）。`hermes version` 不是子命令（invalid choice/退出码 2），" +
				"`upstream <sha>` 是 checkout 提交号不是产品版本，均不作为版本判据。" +
				"安装/升级（`hermes update`）不在本片，Apply(version) 版本不匹配显式失败。" +
				"未验证：线上文档与本机 0.21.2 未逐页 diff；`hermes doctor` / `hermes status --deep` 完整输出未采集" +
				"（避免 `--live`/`--deep` 的真实网络探测）；Windows/macOS/Termux 无本机实测；PyPI 包官方性未定论",
			Evidence: "本机实测 `hermes --version` → 首行 `Hermes Agent v0.21.2 (2026.9.11) · upstream be2f7e9c`（退出 0）；" +
				"`hermes --help` choices 中无 `version`；官方 https://hermes-agent.nousresearch.com/docs/reference/cli-commands",
			Paths: []string{".hermes/config.yaml"},
		},
		{
			Capability: adapter.CapabilityModelProvider, State: adapter.SupportSupported,
			VerifiedVersions: "Hermes Agent 0.21.2（config version 44）",
			Reason: "`model.default` + `model.provider: custom` + `model.base_url`（任意 OpenAI 兼容端点），" +
				"`model.api_key` 写为 `${<归一化 apiKeyEnv>}` 环境变量间接引用（只写变量名，凭据值永不落盘）。" +
				"嵌套键级所有权：`model` 段内未点名键（如 `context_length`/`default_headers`）与同文档密钥面" +
				"（delegation/auxiliary/secrets/dashboard.basic_auth/HTTP(S)_PROXY）逐字节保留；绝不整树 Decode。" +
				"无机器可读 schema（规范 = cli-config.yaml.example 注释 + 文档）→ `_config_version` 变化时显式失败，不静默降级。" +
				"未验证：`.env` / `auth.json` 运行时优先级（按边界完全不读）未做运行验证；`_config_version` 逐版本迁移清单未展开；" +
				"`model.base_url` 与 shell 导出优先级无本机交互验证；环境变量层（HERMES_*）未判定",
			Evidence: "本机实测 `~/.hermes/config.yaml` 顶层 `model` 段（default/provider；base_url 缺省回落 OPENAI_API_KEY）；" +
				"官方 https://hermes-agent.nousresearch.com/docs/integrations/providers §Custom endpoints；" +
				"cli-config.yaml.example §Model Configuration（provider: custom + base_url）；源码 hermes_cli/config.py `_expand_env_vars`（`${VAR}`/`${env:VAR}` 递归展开）",
			Paths: []string{".hermes/config.yaml"},
		},
		{
			Capability: adapter.CapabilityMCP, State: adapter.SupportSupported,
			VerifiedVersions: "Hermes Agent 0.21.2",
			Reason: "command+args 支持（config.yaml 顶层 `mcp_servers.<name>`，单 YAML 真源，无独立 mcp.json）。" +
				"**envRefs 未验证**（本机 5 个条目的 `env` 均为字面值；`${VAR}` 在 MCP `env` 的语义未逐页确认）" +
				"→ Validate 在任何写入之前拒绝含 envRefs 的条目。" +
				"未验证：`hermes mcp list` / `hermes mcp test` 会连接 MCP server，本片不作离线健康检查，未做连通性验证；" +
				"归一化 MCP 契约只携带 command/args/envRefs，§4.2 列出的 url/headers/transport/timeout/enabled/tools.* 不可达（契约缺口）；" +
				"MCP 条目名限定 `[A-Za-z0-9_-]+`（点号与 YAML 点分路径冲突）",
			Evidence: "本机实测 `~/.hermes/config.yaml` 顶层 `mcp_servers` 5 项多形状（code-review-graph 仅 command+args；" +
				"hermes-studio-* 另有 enabled/env/timeout）；官方 https://hermes-agent.nousresearch.com/docs/reference/mcp-config-reference",
			Paths: []string{".hermes/config.yaml"},
		},
		{
			Capability: adapter.CapabilitySkills, State: adapter.SupportSupported,
			VerifiedVersions: "Hermes Agent 0.21.2",
			Reason: "Fleet 自有条目写为 `~/.hermes/skills/<name>` 软链（指向节点规范技能缓存），" +
				"与批次一/片 A/片 B 同范式；目录内既有条目（本机为指向 `~/.agents/skills/*` 的软链）不点名即不触碰。" +
				"缓存未物化时显式失败（不写坏链、不静默跳过）。" +
				"未验证：`skills.external_dirs` 在本片受管范围声明内，但归一化 skills 契约不携带外部技能根 → 本片无可写来源，" +
				"该列表（含既有 `~/.agents/skills`）逐字节保留（契约缺口）；Windows/macOS/Termux 未实测",
			Evidence: "本机实测 `ls ~/.hermes/skills/`（38 项，含 `-> ../../.agents/skills/<name>` 软链）；" +
				"官方 https://hermes-agent.nousresearch.com/docs/user-guide/features/skills；agentskills.io 开放标准",
			Paths: []string{".hermes/skills"},
		},
		{
			Capability: adapter.CapabilityRules, State: adapter.SupportUnsupported,
			VerifiedVersions: "",
			Reason: "本片明确不纳入受管范围：用户级人格/身份文件 `$HERMES_HOME/SOUL.md` 属用户身份，" +
				"§4.2 建议不纳入、§4.4 第 8 项仍属未决；项目级 `AGENTS.md` / `.hermes.md` 是仓库相对路径，" +
				"不在 home 受管面。要管必须做受管块且默认关闭（本片不做）。请求 rules 能力即由 Validate 显式拒绝。",
			Evidence: "官方 https://hermes-agent.nousresearch.com/docs/user-guide/features/personality 与 .../features/context-files；" +
				"本机实测 `~/.hermes/SOUL.md`（667 B，纯 Markdown，无 frontmatter）",
			Paths: []string{".hermes/SOUL.md"},
		},
	})
}

// Validate 是阶段 1 的适配器侧前置校验（任何写入之前）。
func (a *Adapter) Validate(_ context.Context, home string, desired adapter.AgentDesiredState) error {
	if a.goos() != "linux" {
		return fmt.Errorf("agent %q: OS %q is unverified (only linux/amd64 verified on Hermes Agent 0.21.2)", ID, a.goos())
	}
	if err := adapter.CheckCapabilities(ID, a.Capabilities(), desired); err != nil {
		return err
	}
	cfg := adapter.ConfigMap(desired)
	if len(desired.Config) != 0 && len(cfg) == 0 {
		return fmt.Errorf("agent %q: config is not a JSON object: %s", ID, string(desired.Config))
	}
	_, hasModel := cfg["model"]
	endpoint, keyEnv, hasProvider := adapter.ProviderConfig(cfg)
	if hasModel || hasProvider {
		if !hasModel {
			return fmt.Errorf("agent %q: config.model is required when provider is set (Hermes custom endpoints name the API model)", ID)
		}
		if _, ok := cfg["model"].(string); !ok {
			return fmt.Errorf("agent %q: config.model must be a string, got %T", ID, cfg["model"])
		}
		if !hasProvider || endpoint == "" {
			return fmt.Errorf("agent %q: provider.endpoint is required when provider is set", ID)
		}
		// model.api_key 以 `${VAR}` 间接引用承载 apiKeyEnv；字面量密钥永不由本适配器写入。
		if keyEnv == "" {
			return fmt.Errorf("agent %q: provider.apiKeyEnv is required (Hermes custom endpoints resolve the key "+
				"through model.api_key; Fleet writes only the `${<VAR>}` reference, never a literal value)", ID)
		}
		if !envNameRe.MatchString(keyEnv) {
			return fmt.Errorf("agent %q: provider.apiKeyEnv %q must be an environment variable name", ID, keyEnv)
		}
	}
	for name, entry := range desired.MCP {
		if !mcpNameRe.MatchString(name) {
			return fmt.Errorf("agent %q: mcp entry name %q must match %s (dots would collide with the YAML dotted path)", ID, name, mcpNameRe)
		}
		if entry.Command == "" {
			return fmt.Errorf("agent %q: mcp entry %q requires command", ID, name)
		}
		if len(entry.EnvRefs) != 0 {
			return &adapter.ValidateError{
				Family: ID, Capability: adapter.CapabilityMCP, State: adapter.SupportUnverified,
				Reason: fmt.Sprintf("mcp entry %q requests envRefs; ${VAR} expansion inside Hermes mcp_servers env "+
					"is unverified on 0.21.2, so the adapter refuses the write instead of persisting a wrong shape", name),
			}
		}
	}
	for name := range desired.Skills {
		if !validSkillName(name) {
			return fmt.Errorf("agent %q: skill name %q must match %s and not be %q/%q", ID, name, skillNameRe, ".", "..")
		}
	}
	// 既有配置必须存在且版本对位（无 schema → 版本变化显式失败，不静默降级）。
	if _, err := a.requireSupportedConfig(home); err != nil {
		return err
	}
	return nil
}

// validSkillName 防目录穿越：`filepath.Join(root, "..")` 会把目标 Clean 到 root 之外。
func validSkillName(name string) bool {
	return skillNameRe.MatchString(name) && name != "." && name != ".."
}

// requireSupportedConfig 读 config.yaml 并校验 `_config_version` 与本适配器对位。
func (a *Adapter) requireSupportedConfig(home string) (*yaml.Node, error) {
	root, present, err := a.parseConfig(home)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("agent %q: %s is missing or empty; the Hermes home is not initialized "+
			"(run Hermes once so config.yaml with _config_version %d exists) — refusing to create a version-less config",
			ID, a.configPath(home), supportedConfigVersion)
	}
	v, ok := configVersion(root)
	if !ok {
		return nil, fmt.Errorf("agent %q: config.yaml has no integer _config_version; refusing to write "+
			"(adapter verified against %d)", ID, supportedConfigVersion)
	}
	if v != supportedConfigVersion {
		return nil, fmt.Errorf("agent %q: config.yaml _config_version=%d but the adapter is verified against %d; "+
			"refusing to write rather than silently degrade (upgrade the adapter or pin the config version)",
			ID, v, supportedConfigVersion)
	}
	return root, nil
}

// Detect 探测已装/未装与版本（矩阵验收 1：程序缺失与版本不可解析必须可区分）。
func (a *Adapter) Detect(ctx context.Context, home string) (adapter.DetectedAgent, error) {
	d := adapter.DetectedAgent{Family: ID, ConfigPath: a.configPath(home), SchemaVer: SchemaVersion}
	v, err := kit.ProbeVersion(ctx, a.Probe, binaryName, versionArgs, parseVersion)
	switch {
	case errors.Is(err, kit.ErrNotInstalled):
		return d, nil // 未安装：Installed=false，不是错误
	case err != nil:
		return d, err // 版本不可解析：显式错误（不伪装成"未安装"）
	}
	d.Installed, d.Version = true, v
	return d, nil
}

// Inventory 产出 ADR-1 双侧投影（只读受管叶子键，绝不整树 Decode）。
func (a *Adapter) Inventory(ctx context.Context, home string, desired adapter.AgentDesiredState) (adapter.AgentObservedState, error) {
	desiredProj, err := a.desiredProjection(desired)
	if err != nil {
		return adapter.AgentObservedState{}, err
	}
	root, err := a.requireSupportedConfig(home)
	if err != nil {
		return adapter.AgentObservedState{}, err
	}
	observedProj, err := a.observedProjection(home, root, desired)
	if err != nil {
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

func (a *Adapter) probeVersionQuiet(ctx context.Context) string {
	v, err := kit.ProbeVersion(ctx, a.Probe, binaryName, versionArgs, parseVersion)
	if err != nil {
		return ""
	}
	return v
}

// modelProjection 是受管 model 单元：default/provider/base_url/apiKeyEnv 一起写、一起观测
// （Hermes custom provider 缺任一都不可路由）。apiKeyEnv 只承载环境变量名；provider 由期望侧
// 固定为 custom，观测侧读实际值（用户改掉即触发 drift）。
func modelProjection(model, provider, endpoint, keyEnv string) map[string]any {
	return map[string]any{
		"default":   model,
		"provider":  provider,
		"base_url":  endpoint,
		"apiKeyEnv": keyEnv,
	}
}

func (a *Adapter) desiredProjection(desired adapter.AgentDesiredState) (map[string]any, error) {
	proj := map[string]any{}
	cfg := adapter.ConfigMap(desired)
	if len(desired.Config) != 0 && len(cfg) == 0 {
		return nil, fmt.Errorf("hermes: desired config is not a JSON object")
	}
	model, hasModel := cfg["model"]
	endpoint, keyEnv, hasProvider := adapter.ProviderConfig(cfg)
	if hasModel || hasProvider {
		if !hasModel || !hasProvider {
			return nil, fmt.Errorf("hermes: model and provider must be managed together")
		}
		proj["model"] = modelProjection(fmt.Sprintf("%v", model), customProvider, endpoint, keyEnv)
	}
	if len(desired.MCP) != 0 {
		mcp := map[string]any{}
		for name, e := range desired.MCP {
			mcp[name] = map[string]any{"command": e.Command, "args": normalizeArgs(e.Args)}
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
	return proj, nil
}

func (a *Adapter) observedProjection(home string, root *yaml.Node, desired adapter.AgentDesiredState) (map[string]any, error) {
	proj := map[string]any{}
	dcfg := adapter.ConfigMap(desired)
	_, hasModel := dcfg["model"]
	_, _, hasProvider := adapter.ProviderConfig(dcfg)
	if hasModel || hasProvider {
		proj["model"] = modelProjection(
			stringOf(kit.LookupYAML(root, "model.default")),
			stringOf(kit.LookupYAML(root, "model.provider")),
			stringOf(kit.LookupYAML(root, "model.base_url")),
			envRefName(kit.LookupYAML(root, "model.api_key")),
		)
	}
	if len(desired.MCP) != 0 {
		mcp := map[string]any{}
		for name := range desired.MCP {
			command := stringOf(kit.LookupYAML(root, "mcp_servers."+name+".command"))
			if command == "" && kit.LookupYAML(root, "mcp_servers."+name) == nil {
				continue // 未注册的受管条目：投影缺席 → 摘要不等 → drift
			}
			mcp[name] = map[string]any{"command": command,
				"args": stringSlice(kit.LookupYAML(root, "mcp_servers."+name+".args"))}
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
	return proj, nil
}

// Plan 比较两侧投影产出变更（阶段 3；空计划即幂等）。
func (a *Adapter) Plan(_ context.Context, _ string, desired adapter.AgentDesiredState, observed adapter.AgentObservedState) ([]adapter.Change, error) {
	desiredProj, err := a.desiredProjection(desired)
	if err != nil {
		return nil, err
	}
	observedProj := observed.ManagedProjection
	if observedProj == nil {
		observedProj = map[string]any{}
	}
	var changes []adapter.Change
	if desired.Version != "" && observed.Version != desired.Version {
		changes = append(changes, adapter.Change{Family: ID, Step: "version", Key: "version",
			From: observed.Version, To: desired.Version})
	}
	if !equal(desiredProj["model"], observedProj["model"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "config", Key: keyModel,
			From: observedProj["model"], To: desiredProj["model"]})
	}
	changes = append(changes, kit.DiffMapStep(ID, "mcp", "mcp.", desiredProj["mcp"], observedProj["mcp"])...)
	changes = append(changes, kit.DiffMapStep(ID, "skills", "skill:", desiredProj["skills"], observedProj["skills"])...)
	return changes, nil
}

// keyModel 是受管 model 单元的 plan 键（default/provider/base_url/apiKeyEnv 一起收敛）。
const keyModel = "config.model"

// Apply 执行变更（阶段 5–9）：YAML Node 树合并写（只写受管叶子键，注释与未托管键保留）、
// 原子写、Skill 软链。空变更零写入（幂等）。
func (a *Adapter) Apply(ctx context.Context, home string, desired adapter.AgentDesiredState, changes []adapter.Change) error {
	byStep := map[string][]adapter.Change{}
	for _, c := range changes {
		byStep[c.Step] = append(byStep[c.Step], c)
	}
	if len(byStep["version"]) != 0 {
		v := a.probeVersionQuiet(ctx)
		if v != desired.Version {
			return fmt.Errorf("hermes: version %s is required but %q is installed and the command installer "+
				"(§20.2) is not wired in this slice", desired.Version, v)
		}
	}
	if len(byStep["config"]) != 0 || len(byStep["mcp"]) != 0 {
		if err := a.applyConfig(home, desired, byStep["mcp"]); err != nil {
			return err
		}
	}
	for _, c := range byStep["skills"] {
		name := strings.TrimPrefix(c.Key, "skill:")
		if !validSkillName(name) {
			return fmt.Errorf("hermes: refusing skill name %q (traversal guard)", name)
		}
		digest := fmt.Sprintf("%v", c.To)
		if err := kit.EnsureSkillLink(a.skillPath(home, name), name, digest, kit.SkillCacheDir(home, name, digest)); err != nil {
			return fmt.Errorf("hermes: %w", err)
		}
	}
	return nil
}

// applyConfig 用 YAML Node 合并写受管叶子键：只改受管键，注释、键序与未托管键（含同文档
// 密钥面）逐字节保留。写后重新解析验证（§5.5）。
func (a *Adapter) applyConfig(home string, desired adapter.AgentDesiredState, mcpChanges []adapter.Change) error {
	raw, err := a.readConfigRaw(home)
	if err != nil {
		return err
	}
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("hermes: %s is missing or empty; refusing to create a version-less config", a.configPath(home))
	}
	cfg := adapter.ConfigMap(desired)
	sets := map[string]any{}
	if model, ok := cfg["model"]; ok {
		endpoint, keyEnv, _ := adapter.ProviderConfig(cfg)
		sets["model.default"] = fmt.Sprintf("%v", model)
		sets["model.provider"] = customProvider
		sets["model.base_url"] = endpoint
		sets["model.api_key"] = "${" + keyEnv + "}"
	}
	for _, c := range mcpChanges {
		name := strings.TrimPrefix(c.Key, "mcp.")
		entry, ok := desired.MCP[name]
		if !ok {
			continue
		}
		sets["mcp_servers."+name+".command"] = entry.Command
		sets["mcp_servers."+name+".args"] = normalizeArgs(entry.Args)
	}
	if len(sets) == 0 {
		return nil
	}
	out, err := kit.PatchYAML(raw, sets)
	if err != nil {
		return fmt.Errorf("hermes: patch config.yaml: %w", err)
	}
	if _, err := kit.ParseYAML(out); err != nil {
		return fmt.Errorf("hermes: patched config.yaml does not parse, aborting write: %w", err)
	}
	return kit.AtomicWrite(a.configPath(home), []byte(out), 0o600)
}

// HealthCheck（阶段 10）：配置可解析且 `_config_version` 对位、`hermes config check` 通过、
// 受管键到位、版本一致、Skill 软链 digest 一致。只读用法，不使用 --live/--deep。
func (a *Adapter) HealthCheck(ctx context.Context, home string, desired adapter.AgentDesiredState) error {
	root, err := a.requireSupportedConfig(home)
	if err != nil {
		return fmt.Errorf("hermes: health: %w", err)
	}
	out, err := a.runConfigCheck(ctx, home)
	if err != nil {
		return fmt.Errorf("hermes: health: `hermes config check` failed: %w: %s", err, strings.TrimSpace(out))
	}
	if m := configVersionRe.FindStringSubmatch(out); m != nil && m[1] != fmt.Sprintf("%d", supportedConfigVersion) {
		return fmt.Errorf("hermes: health: `hermes config check` reports config version %s, want %d", m[1], supportedConfigVersion)
	}
	dcfg := adapter.ConfigMap(desired)
	_, hasModel := dcfg["model"]
	_, _, hasProvider := adapter.ProviderConfig(dcfg)
	if hasModel || hasProvider {
		got := modelProjection(
			stringOf(kit.LookupYAML(root, "model.default")),
			stringOf(kit.LookupYAML(root, "model.provider")),
			stringOf(kit.LookupYAML(root, "model.base_url")),
			envRefName(kit.LookupYAML(root, "model.api_key")),
		)
		want, err := a.desiredProjection(desired)
		if err != nil {
			return fmt.Errorf("hermes: health: %w", err)
		}
		if !equal(want["model"], got) {
			return fmt.Errorf("hermes: health: managed model is %v, want %v", got, want["model"])
		}
	}
	for name, entry := range desired.MCP {
		if stringOf(kit.LookupYAML(root, "mcp_servers."+name+".command")) != entry.Command {
			return fmt.Errorf("hermes: health: mcp server %q command is not in place", name)
		}
	}
	if desired.Version != "" {
		v, err := kit.ProbeVersion(ctx, a.Probe, binaryName, versionArgs, parseVersion)
		if err != nil {
			return fmt.Errorf("hermes: health: version probe failed: %w", err)
		}
		if v != desired.Version {
			return fmt.Errorf("hermes: health: installed version %s != desired %s", v, desired.Version)
		}
	}
	for name, s := range desired.Skills {
		if got := kit.ReadSkillDigest(a.skillPath(home, name)); got != s.ContentDigest {
			return fmt.Errorf("hermes: health: skill %q link digest is %q, want %q", name, got, s.ContentDigest)
		}
	}
	return nil
}

func (a *Adapter) runConfigCheck(ctx context.Context, home string) (string, error) {
	run := a.ConfigCheck
	if run == nil {
		run = defaultConfigCheck
	}
	return run(ctx, home)
}

// defaultConfigCheck 执行只读的 `hermes config check`，把 HERMES_HOME 指到 <home>/.hermes
// （护栏 #12：绝不落到真实 ~/.hermes）。绝不使用 --live/--deep。
func defaultConfigCheck(ctx context.Context, home string) (string, error) {
	cmd := exec.CommandContext(ctx, binaryName, "config", "check")
	cmd.Env = envWith("HERMES_HOME", configRoot(home))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// envWith 返回 os.Environ() 但把 key 替换为 value（避免继承真实 HERMES_HOME）。
func envWith(key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, prefix+value)
}

// ---- 路径（相对注入的 home 根，护栏 #12）----

func configRoot(home string) string { return filepath.Join(home, ".hermes") }
func (a *Adapter) configPath(home string) string {
	return filepath.Join(configRoot(home), "config.yaml")
}
func (a *Adapter) skillPath(home, name string) string {
	return filepath.Join(configRoot(home), "skills", name)
}

const configRel = ".hermes/config.yaml"

// readConfigRaw 读 config.yaml；文件不存在返回 ""。
func (a *Adapter) readConfigRaw(home string) (string, error) {
	b, err := os.ReadFile(a.configPath(home))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("hermes: read config.yaml: %w", err)
	}
	return string(b), nil
}

// parseConfig 解析 config.yaml；第二个返回值表示文件是否存在且非空。
func (a *Adapter) parseConfig(home string) (*yaml.Node, bool, error) {
	raw, err := a.readConfigRaw(home)
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, false, nil
	}
	root, err := kit.ParseYAML(raw)
	if err != nil {
		return nil, false, fmt.Errorf("agent %q: config.yaml unparseable: %w", ID, err)
	}
	return root, true, nil
}

// configVersion 取 `_config_version` 的整数值。
func configVersion(root *yaml.Node) (int, bool) {
	switch v := kit.LookupYAML(root, "_config_version").(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

// ---- 受管文件声明（reconciler 的备份/恢复契约，§5.3）----

// ManagedFiles 声明本适配器写入的文件（相对 home）。
func (a *Adapter) ManagedFiles(home string) ([]string, error) {
	return []string{configRel}, nil
}

// extractableModelKeys 是进备份的受管键。model.api_key 仅当其为 `${VAR}` 间接引用时提取
// （字面量密钥绝不进 manifest，避免把秘密值持久化）。
var extractableModelKeys = []string{"model.default", "model.provider", "model.base_url"}

// ExtractManaged 提取受管键值（只提取 model 单元；MCP 条目名只有期望侧知道，与片 A 同口径）。
func (a *Adapter) ExtractManaged(content []byte) (map[string]any, error) {
	root, err := kit.ParseYAML(string(content))
	if err != nil {
		return map[string]any{}, nil
	}
	out := map[string]any{}
	for _, p := range extractableModelKeys {
		if v := kit.LookupYAML(root, p); v != nil {
			out[p] = v
		}
	}
	if v, ok := kit.LookupYAML(root, "model.api_key").(string); ok && envRefRe.MatchString(v) {
		out["model.api_key"] = v
	}
	return out, nil
}

// MergeManaged 受管键级回退（§5.3 契约 3）：只还原受管叶子键，保留文件中其余（未托管）
// 内容。未知受管键显式失败，不静默跳过。
func (a *Adapter) MergeManaged(home, relPath string, managed map[string]any) error {
	if relPath != configRel {
		return fmt.Errorf("hermes: %s is not a managed-key-restorable file", relPath)
	}
	allowed := map[string]bool{"model.default": true, "model.provider": true, "model.base_url": true, "model.api_key": true}
	sets := map[string]any{}
	for k, v := range managed {
		if !allowed[k] {
			return fmt.Errorf("hermes: unknown managed key %q in backup for %s", k, relPath)
		}
		sets[k] = v
	}
	raw, err := a.readConfigRaw(home)
	if err != nil {
		return err
	}
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("hermes: config.yaml is missing; cannot merge managed keys back")
	}
	out, err := kit.PatchYAML(raw, sets)
	if err != nil {
		return fmt.Errorf("hermes: managed rollback: %w", err)
	}
	if _, err := kit.ParseYAML(out); err != nil {
		return fmt.Errorf("hermes: managed rollback produced unparseable config.yaml: %w", err)
	}
	return kit.AtomicWrite(a.configPath(home), []byte(out), 0o600)
}

// ---- 小工具 ----

func normalizeArgs(args []string) []string { return append([]string{}, args...) }

// envRefName 从 `model.api_key` 取环境变量名；字面量（非 `${VAR}` 引用）返回 ""（漂移信号）。
func envRefName(v any) string {
	s, _ := v.(string)
	m := envRefRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
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

func equal(a, b any) bool { return kit.ValuesEqual(a, b) }
