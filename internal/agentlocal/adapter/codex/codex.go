// Package codex 是 Codex 家族适配器（架构 v1.1.2 §5.4/§15.1；批次一）。
//
// 运行时来源与证据见 docs/adapters-batch-1.md「Codex」一节：本机实测
// `codex --version` → `codex-cli 0.154.0`；配置路径由 CLI 自身 help 文本确认
// （"--config <key=value> Override a configuration value that would otherwise be
// loaded from `~/.codex/config.toml`"）；MCP 表结构由 `codex mcp add --help`
// 与本机 config.toml 的既有 `[mcp_servers.*]` 条目确认；Skill 目录由本机
// `~/.codex/skills/<name>/SKILL.md` 布局确认。
//
// 受管范围（其余键一律未托管，合并写不触碰）：
//
//	~/.codex/config.toml   顶层 model / model_provider、[model_providers.fleet]、
//	                       [mcp_servers.<name>]（仅期望中点名的条目）
//	~/.codex/AGENTS.md     rules 受管标记块（FR-5.1）
//	~/.codex/skills/<name> Skill 软链（FR-6.3）
package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	ID = "codex"
	// SchemaVersion 是本适配器受管 schema 版本（随 Hello 上报，FR-13.4）。
	SchemaVersion = "codex-config-v1"
	// binaryName 是可执行程序名。
	binaryName = "codex"
	// providerID 是 Fleet 在 Codex 里的 provider 命名空间（model_providers.fleet）。
	providerID = "fleet"
	// providerName 是该 provider 的展示名。
	providerName = "agent-fleet"
	// wireAPI 是 Codex 0.154.0 唯一受支持的 wire API（二进制内置提示：
	// `wire_api = "chat"` is no longer supported → 必须 "responses"）。
	wireAPI = "responses"
)

// versionArgs 是版本探测参数（本机实测 `codex --version`）。
var versionArgs = []string{"--version"}

// managedTopLevelKeys 是本适配器可能写入的顶层标量键。
var managedTopLevelKeys = []string{"model", "model_provider"}

var (
	safeName  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$`)
)

// parseVersion 解析 `codex-cli <semver>`；前缀或版本形态不符即返回 ""（矩阵
// 验收 1：版本可解析/不可解析必须可区分，不得把输出整体当版本号）。
func parseVersion(out string) string {
	rest, ok := strings.CutPrefix(kit.FirstLine(out), "codex-cli ")
	if !ok {
		return ""
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 || !versionRe.MatchString(fields[0]) {
		return ""
	}
	return fields[0]
}

// Adapter 是 Codex 适配器。Probe/GOOS 可注入以便 fixture 测试不依赖真实二进制。
type Adapter struct {
	Probe kit.VersionProbe
	GOOS  string
}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) ID() string { return ID }

func (a *Adapter) goos() string {
	if a.GOOS != "" {
		return a.GOOS
	}
	return runtimeGOOS()
}

// Capabilities 声明本家族逐能力支持状态与证据（矩阵「统一适配契约」）。
func (a *Adapter) Capabilities() []adapter.CapabilityDecl {
	return adapter.SortDecls([]adapter.CapabilityDecl{
		{
			Capability: adapter.CapabilityVersion, State: adapter.SupportSupported,
			VerifiedVersions: "codex-cli 0.154.0 (linux/amd64)",
			Evidence:         "本机 `codex --version` → `codex-cli 0.154.0`；`~/.codex/packages/standalone/releases/0.154.0-x86_64-unknown-linux-musl`",
			Paths:            []string{".codex/packages/standalone/releases"},
		},
		{
			Capability: adapter.CapabilityModelProvider, State: adapter.SupportSupported,
			VerifiedVersions: "codex-cli 0.154.0",
			Evidence:         "二进制内 ModelProviderInfo 字段表（name/base_url/env_key/wire_api）；本机 config.toml 顶层 model 键实测",
			Paths:            []string{".codex/config.toml"},
		},
		{
			Capability: adapter.CapabilityMCP, State: adapter.SupportSupported,
			VerifiedVersions: "codex-cli 0.154.0",
			Evidence:         "`codex mcp add --help`（--env/--url/--bearer-token-env-var）+ 本机 `[mcp_servers.*]` 既有条目",
			Paths:            []string{".codex/config.toml"},
		},
		{
			Capability: adapter.CapabilitySkills, State: adapter.SupportSupported,
			VerifiedVersions: "codex-cli 0.154.0",
			Evidence:         "本机 `~/.codex/skills/<name>/SKILL.md` 布局（.system/imagegen 等）",
			Paths:            []string{".codex/skills"},
		},
		{
			Capability: adapter.CapabilityRules, State: adapter.SupportSupported,
			VerifiedVersions: "codex-cli 0.154.0",
			Evidence:         "本机 `~/.codex/AGENTS.md`（现为软链，指向用户全局规则文件）",
			Paths:            []string{".codex/AGENTS.md"},
		},
	})
}

// Validate 是阶段 1 的适配器侧前置校验（任何写入之前）。
func (a *Adapter) Validate(_ context.Context, home string, desired adapter.AgentDesiredState) error {
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
	if endpoint, _, ok := adapter.ProviderConfig(cfg); ok && endpoint == "" {
		return fmt.Errorf("agent %q: provider.endpoint is required when providerRef is set", ID)
	}
	for name, entry := range desired.MCP {
		if !safeName.MatchString(name) {
			return fmt.Errorf("agent %q: mcp entry name %q must match %s", ID, name, safeName)
		}
		if len(entry.EnvRefs) != 0 {
			// 未验证：没有证据表明 Codex 会展开 MCP env 中的 ${VAR}，
			// 直接写入会把字面量交给服务进程。明确拒绝，不静默跳过。
			return &adapter.ValidateError{
				Family: ID, Capability: adapter.CapabilityMCP, State: adapter.SupportUnverified,
				Reason: fmt.Sprintf("mcp entry %q requests envRefs; codex-cli 0.154.0 has no evidence that ${VAR} "+
					"is expanded in [mcp_servers.*].env, so the value would be passed literally; "+
					"remove envRefs or unmanage this entry", name),
			}
		}
		if entry.Command == "" {
			return fmt.Errorf("agent %q: mcp entry %q requires command", ID, name)
		}
	}
	if _, _, err := kit.ReadManagedBlock(a.rulesPath(home)); err != nil {
		return fmt.Errorf("agent %q: %w", ID, err)
	}
	raw, err := os.ReadFile(a.configPath(home))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("agent %q: read config: %w", ID, err)
	}
	if _, err := parseTOML(string(raw)); err != nil {
		return fmt.Errorf("agent %q: existing config is unparseable: %w", ID, err)
	}
	return nil
}

func (a *Adapter) checkOS() error {
	switch a.goos() {
	case "linux":
		return nil
	default:
		return fmt.Errorf("agent %q: OS %q is unverified (only linux/amd64 verified on codex-cli 0.154.0; "+
			"macOS/Windows support has no evidence in this repo yet)", ID, a.goos())
	}
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

// Inventory 产出 ADR-1 双侧投影：受管键集合 = 期望点名的键（未托管字段既不
// 比较也不备份，§7.1 约束 1）。
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
	version := a.probeVersionQuiet(ctx)
	return adapter.AgentObservedState{
		Family:                   ID,
		Version:                  version,
		ManagedProjection:        observedProj,
		DesiredProjectionDigest:  kit.ProjectionDigest(desiredProj),
		ObservedProjectionDigest: kit.ProjectionDigest(observedProj),
		CanonicalizationVersion:  adapter.ProjectionCanonicalizationVersion,
		ConfigPath:               a.configPath(home),
	}, nil
}

// probeVersionQuiet 供 Inventory 使用：未安装返回 ""，不可解析也返回 ""
// （此时 drift/plan 会走版本步骤并由 HealthCheck/Apply 显式失败）。
func (a *Adapter) probeVersionQuiet(ctx context.Context) string {
	v, err := kit.ProbeVersion(ctx, a.Probe, binaryName, versionArgs, parseVersion)
	if err != nil {
		return ""
	}
	return v
}

func (a *Adapter) desiredProjection(desired adapter.AgentDesiredState) (map[string]any, error) {
	proj := map[string]any{}
	cfg := adapter.ConfigMap(desired)
	if len(desired.Config) != 0 && len(cfg) == 0 {
		return nil, fmt.Errorf("codex: desired config is not a JSON object")
	}
	if model, ok := cfg["model"]; ok {
		proj["model"] = model
	}
	if endpoint, keyEnv, ok := adapter.ProviderConfig(cfg); ok {
		proj["provider"] = map[string]any{"endpoint": endpoint, "apiKeyEnv": keyEnv}
	}
	if len(desired.MCP) != 0 {
		mcp := map[string]any{}
		for name, e := range desired.MCP {
			mcp[name] = map[string]any{"command": e.Command, "args": e.Args}
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
	if _, ok := dcfg["model"]; ok {
		if v, ok := cfg["model"]; ok {
			proj["model"] = v
		}
	}
	if _, _, ok := adapter.ProviderConfig(dcfg); ok {
		prov := map[string]any{"endpoint": "", "apiKeyEnv": ""}
		if id, _ := cfg["model_provider"].(string); id == providerID {
			if table, ok := tableOf(cfg, "model_providers", providerID); ok {
				endpoint, _ := table["base_url"].(string)
				keyEnv, _ := table["env_key"].(string)
				prov["endpoint"], prov["apiKeyEnv"] = endpoint, keyEnv
			}
		}
		proj["provider"] = prov
	}
	if len(desired.MCP) != 0 {
		mcp := map[string]any{}
		servers, _ := cfg["mcp_servers"].(map[string]any)
		for name := range desired.MCP {
			entry, ok := servers[name].(map[string]any)
			if !ok {
				continue // 未注册的受管条目：投影缺席 → 摘要不等 → drift
			}
			command, _ := entry["command"].(string)
			args := []string{}
			if raw, ok := entry["args"].([]any); ok {
				for _, v := range raw {
					if s, ok := v.(string); ok {
						args = append(args, s)
					}
				}
			}
			mcp[name] = map[string]any{"command": command, "args": args}
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
func (a *Adapter) Plan(ctx context.Context, home string, desired adapter.AgentDesiredState, observed adapter.AgentObservedState) ([]adapter.Change, error) {
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
	if !projectionEqual(desiredProj["model"], observedProj["model"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "config", Key: "config.model",
			From: observedProj["model"], To: desiredProj["model"]})
	}
	if !projectionEqual(desiredProj["provider"], observedProj["provider"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "config", Key: "config.provider",
			From: observedProj["provider"], To: desiredProj["provider"]})
	}
	changes = append(changes, diffMapStep("mcp", "mcp.", desiredProj["mcp"], observedProj["mcp"])...)
	changes = append(changes, diffMapStep("skills", "skill:", desiredProj["skills"], observedProj["skills"])...)
	if !projectionEqual(desiredProj["rules"], observedProj["rules"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "rules", Key: "rules.global",
			From: observedProj["rules"], To: desiredProj["rules"]})
	}
	return changes, nil
}

// Apply 执行变更（阶段 5–9）：合并写（只写受管键/受管条目，其余原样保留）、
// 原子写、Skill 软链。空变更零写入（幂等）。
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
			return fmt.Errorf("codex: version %s is required but %q is installed and the command installer "+
				"(§20.2) is not wired in this slice", desired.Version, v)
		}
	}
	if len(byStep["config"]) != 0 || len(byStep["mcp"]) != 0 {
		if err := a.applyConfig(home, desired, byStep["config"], byStep["mcp"]); err != nil {
			return err
		}
	}
	for _, c := range byStep["skills"] {
		name := strings.TrimPrefix(c.Key, "skill:")
		digest := fmt.Sprintf("%v", c.To)
		if err := kit.EnsureSkillLink(a.skillPath(home, name), name, digest, kit.SkillCacheDir(home, name, digest)); err != nil {
			return fmt.Errorf("codex: %w", err)
		}
	}
	if len(byStep["rules"]) != 0 {
		if err := kit.WriteManagedBlock(a.rulesPath(home), kit.RulesContent(desired.Rules)); err != nil {
			return fmt.Errorf("codex: rules: %w", err)
		}
	}
	return nil
}

// applyConfig 用行级手术写入受管键与受管 MCP 表：保留注释与未托管内容。
// 写后重新解析验证（§5.5）；解析失败即失败，不留半写文件（原子写 + 校验）。
func (a *Adapter) applyConfig(home string, desired adapter.AgentDesiredState, configChanges, mcpChanges []adapter.Change) error {
	path := a.configPath(home)
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("codex: read config: %w", err)
	}
	doc := string(raw)
	if _, err := parseTOML(doc); err != nil {
		return fmt.Errorf("codex: existing config is unparseable, refusing to merge: %w", err)
	}
	scalars := map[string]any{}
	tables := map[string]map[string]any{}
	cfg := adapter.ConfigMap(desired)
	if _, ok := cfg["model"]; ok {
		if v, present := cfg["model"]; present {
			scalars["model"] = v
		}
	}
	if endpoint, keyEnv, ok := adapter.ProviderConfig(cfg); ok {
		scalars["model_provider"] = providerID
		table := map[string]any{"name": providerName, "base_url": endpoint, "wire_api": wireAPI}
		if keyEnv != "" {
			table["env_key"] = keyEnv
		}
		tables["model_providers."+providerID] = table
	}
	for _, c := range mcpChanges {
		name := strings.TrimPrefix(c.Key, "mcp.")
		entry, ok := desired.MCP[name]
		if !ok {
			continue
		}
		table := map[string]any{"command": entry.Command}
		if len(entry.Args) != 0 {
			table["args"] = entry.Args
		}
		tables["mcp_servers."+name] = table
	}
	out, err := patchTOML(doc, scalars, tables)
	if err != nil {
		return fmt.Errorf("codex: patch config: %w", err)
	}
	if _, err := parseTOML(out); err != nil {
		return fmt.Errorf("codex: patched config does not parse, aborting write: %w", err)
	}
	return kit.AtomicWrite(path, []byte(out), 0o600)
}

// HealthCheck（阶段 10）：配置可解析、受管键到位、版本可解析且与期望一致。
// 只验证语法/注册，不验证外部服务可用性（FR-4.3）。
func (a *Adapter) HealthCheck(ctx context.Context, home string, desired adapter.AgentDesiredState) error {
	path := a.configPath(home)
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("codex: health: read config: %w", err)
	}
	cfg, err := parseTOML(string(raw))
	if err != nil {
		return fmt.Errorf("codex: health: config unparseable: %w", err)
	}
	dcfg := adapter.ConfigMap(desired)
	if model, ok := dcfg["model"]; ok {
		if got, present := cfg["model"]; !present || fmt.Sprintf("%v", got) != fmt.Sprintf("%v", model) {
			return fmt.Errorf("codex: health: managed key model is %v, want %v", got, model)
		}
	}
	if endpoint, keyEnv, ok := adapter.ProviderConfig(dcfg); ok {
		table, present := tableOf(cfg, "model_providers", providerID)
		if !present || fmt.Sprintf("%v", table["base_url"]) != endpoint {
			return fmt.Errorf("codex: health: model_providers.%s.base_url is not in place", providerID)
		}
		if keyEnv != "" && fmt.Sprintf("%v", table["env_key"]) != keyEnv {
			return fmt.Errorf("codex: health: model_providers.%s.env_key is not in place", providerID)
		}
	}
	for name := range desired.MCP {
		servers, _ := cfg["mcp_servers"].(map[string]any)
		if _, ok := servers[name].(map[string]any); !ok {
			return fmt.Errorf("codex: health: mcp server %q is not registered after apply", name)
		}
	}
	if desired.Version != "" {
		v, err := kit.ProbeVersion(ctx, a.Probe, binaryName, versionArgs, parseVersion)
		if err != nil {
			return fmt.Errorf("codex: health: version probe failed: %w", err)
		}
		if v != desired.Version {
			return fmt.Errorf("codex: health: installed version %s != desired %s", v, desired.Version)
		}
	}
	for name, s := range desired.Skills {
		if got := kit.ReadSkillDigest(a.skillPath(home, name)); got != s.ContentDigest {
			return fmt.Errorf("codex: health: skill %q link digest is %q, want %q", name, got, s.ContentDigest)
		}
	}
	if kit.RulesContent(desired.Rules) != "" {
		content, ok, err := kit.ReadManagedBlock(a.rulesPath(home))
		if err != nil {
			return fmt.Errorf("codex: health: %w", err)
		}
		if !ok || content != kit.RulesContent(desired.Rules) {
			return fmt.Errorf("codex: health: rules managed block is missing or differs")
		}
	}
	return nil
}

// ---- 路径（相对注入的 home 根，护栏 #12）----

func (a *Adapter) configPath(home string) string { return filepath.Join(home, ".codex", "config.toml") }
func (a *Adapter) rulesPath(home string) string  { return filepath.Join(home, ".codex", "AGENTS.md") }
func (a *Adapter) skillPath(home, name string) string {
	return filepath.Join(home, ".codex", "skills", name)
}

// ---- 受管文件声明（reconciler 的备份/恢复契约，§5.3）----

// ManagedFiles 声明本适配器写入的文件（相对 home）。
func (a *Adapter) ManagedFiles(home string) ([]string, error) {
	return []string{
		filepath.Join(".codex", "config.toml"),
		filepath.Join(".codex", "AGENTS.md"),
	}, nil
}

// ExtractManaged 提取受管键值（TOML 键 + rules 受管块；软链由路径体现）。
func (a *Adapter) ExtractManaged(content []byte) (map[string]any, error) {
	cfg, err := parseTOML(string(content))
	if err != nil {
		// 非 TOML（如 AGENTS.md）：受管对象是 rules 受管块内容。
		if block, ok, blockErr := kit.ManagedBlockIn(string(content)); blockErr == nil && ok {
			return map[string]any{"agentFleetRules": block}, nil
		}
		return map[string]any{}, nil
	}
	out := kit.ManagedProjection(cfg, managedTopLevelKeys...)
	if table, ok := tableOf(cfg, "model_providers", providerID); ok {
		out["model_providers."+providerID] = table
	}
	return out, nil
}

// MergeManaged 受管字段级回退（§5.3 契约 3）：存在外部编辑时只还原受管键，
// 保留未托管当前值。rules 文件按受管块回退。
func (a *Adapter) MergeManaged(home, relPath string, managed map[string]any) error {
	switch relPath {
	case filepath.Join(".codex", "config.toml"):
		scalars := map[string]any{}
		tables := map[string]map[string]any{}
		for k, v := range managed {
			if strings.Contains(k, ".") {
				if table, ok := v.(map[string]any); ok {
					tables[k] = table
				}
				continue
			}
			scalars[k] = v
		}
		raw, err := os.ReadFile(a.configPath(home))
		if err != nil {
			return fmt.Errorf("codex: read config for managed rollback: %w", err)
		}
		out, err := patchTOML(string(raw), scalars, tables)
		if err != nil {
			return err
		}
		if _, err := parseTOML(out); err != nil {
			return fmt.Errorf("codex: managed rollback produced unparseable config: %w", err)
		}
		return kit.AtomicWrite(a.configPath(home), []byte(out), 0o600)
	case filepath.Join(".codex", "AGENTS.md"):
		content, _ := managed["agentFleetRules"].(string)
		return kit.WriteManagedBlock(a.rulesPath(home), content)
	default:
		return fmt.Errorf("codex: %s is not a managed-key-restorable file", relPath)
	}
}

// ---- 小工具 ----

func runtimeGOOS() string { return runtime.GOOS }

// readConfig 读并解析 config.toml；文件不存在视为空文档（首次纳管）。
func (a *Adapter) readConfig(home string) (map[string]any, error) {
	raw, err := os.ReadFile(a.configPath(home))
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("codex: read config: %w", err)
	}
	cfg, err := parseTOML(string(raw))
	if err != nil {
		return nil, fmt.Errorf("codex: config unparseable: %w", err)
	}
	return cfg, nil
}

func tableOf(cfg map[string]any, path ...string) (map[string]any, bool) {
	cur := cfg
	for i, p := range path {
		v, ok := cur[p]
		if !ok {
			return nil, false
		}
		table, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if i == len(path)-1 {
			return table, true
		}
		cur = table
	}
	return nil, false
}

func projectionEqual(a, b any) bool {
	return fmt.Sprintf("%#v", a) == fmt.Sprintf("%#v", b)
}

func diffMapStep(step, keyPrefix string, desiredRaw, observedRaw any) []adapter.Change {
	desired, _ := desiredRaw.(map[string]any)
	if len(desired) == 0 {
		return nil
	}
	observed, _ := observedRaw.(map[string]any)
	names := make([]string, 0, len(desired))
	for n := range desired {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []adapter.Change
	for _, n := range names {
		if projectionEqual(desired[n], observed[n]) {
			continue
		}
		out = append(out, adapter.Change{Family: ID, Step: step, Key: keyPrefix + n,
			From: observed[n], To: desired[n]})
	}
	return out
}
