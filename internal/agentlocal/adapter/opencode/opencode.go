// Package opencode 是 OpenCode 家族适配器（架构 v1.1.2 §5.4/§15.3；批次一）。
//
// 运行时来源与证据见 docs/adapters-batch-1.md「OpenCode」一节（本机**未安装**
// 该家族，全部结论来自官方文档，逐项附 URL）：
//   - 发行包 npm `opencode-ai`（latest 1.18.32，bin `opencode`）；
//   - 全局配置 `~/.config/opencode/opencode.json`（JSON/JSONC，schema
//     https://opencode.ai/config.json）；
//   - 默认模型顶层 `model: "<provider>/<model-id>"`；
//   - 自定义 OpenAI 兼容 provider：`provider.<id>.{npm,name,options.baseURL,
//     options.apiKey,models}`，`apiKey` 支持 `{env:VAR}` 间接引用（不落明文）；
//   - MCP：顶层 `mcp.<name>.{type:"local",command:[...],environment,enabled}`；
//   - Skills：`~/.config/opencode/skills/<name>/SKILL.md`；
//   - 指令：全局 `~/.config/opencode/AGENTS.md`。
//
// 受管范围（其余键一律未托管）：
//
//	~/.config/opencode/opencode.json   model / provider.fleet / mcp.<name>
//	~/.config/opencode/AGENTS.md       rules 受管标记块（OpenCode 无原生标记语义，
//	                                   块内为纯文本；块外内容逐字节保留）
//	~/.config/opencode/skills/<name>   Skill 软链
package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/kit"
)

const (
	// ID 是家族标识。
	ID = "opencode"
	// SchemaVersion 是本适配器受管 schema 版本。
	SchemaVersion = "opencode-config-v1"
	// binaryName 是可执行程序名（npm bin 映射）。
	binaryName = "opencode"
	// providerID 是 Fleet 在 OpenCode 中的 provider 命名空间。
	providerID = "fleet"
	// providerNPM 是 OpenAI 兼容 provider 的 SDK 包（docs/providers）。
	providerNPM = "@ai-sdk/openai-compatible"
	// schemaURL 是官方配置 schema（写入新文件时带上，便于用户校验）。
	schemaURL = "https://opencode.ai/config.json"
)

// versionArgs 是版本探测参数（docs/cli：`--version/-v` 打印版本号）。
var versionArgs = []string{"--version"}

var (
	// versionRe 接受裸 semver；CLI 的确切输出格式官方未给示例，故
	// 解析失败必须显式报错而不是猜（矩阵验收 1）。
	versionRe = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$`)
	// skillNameRe 是 OpenCode 对 Skill 目录名的硬约束（docs/skills：
	// ^[a-z0-9]+(-[a-z0-9]+)*$，且必须与目录名一致）。
	skillNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	mcpNameRe   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// parseVersion 解析 `opencode --version` 输出（容错首行空白与可选 v 前缀）。
func parseVersion(out string) string {
	line := kit.FirstLine(out)
	line = strings.TrimSpace(line)
	if !versionRe.MatchString(line) {
		return ""
	}
	return strings.TrimPrefix(line, "v")
}

// Adapter 是 OpenCode 适配器。
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
	return runtime.GOOS
}

// Capabilities 声明逐能力支持状态与证据（矩阵「统一适配契约」）。
func (a *Adapter) Capabilities() []adapter.CapabilityDecl {
	return adapter.SortDecls([]adapter.CapabilityDecl{
		{
			Capability: adapter.CapabilityVersion, State: adapter.SupportSupported,
			VerifiedVersions: "docs 对应 1.18.x（本机未安装，未实测）",
			Reason: "`--version` flag 有文档，但输出格式未验证（官方无示例）→ 解析器只接受裸 semver，" +
				"形态不符即显式报错；安装/升级（§20.2 command installer）不在本片，" +
				"Apply(version) 在版本不匹配时显式失败",
			Evidence: "https://opencode.ai/docs/cli/（`--version, -v  Print version number`）；" +
				"npm opencode-ai latest 1.18.32 / bin `opencode`（https://registry.npmjs.org/opencode-ai）",
		},
		{
			Capability: adapter.CapabilityModelProvider, State: adapter.SupportSupported,
			VerifiedVersions: "文档对应 1.18.x（未在本机实测）",
			Evidence: "https://opencode.ai/docs/providers/（provider.<id>.npm/options.baseURL/options.apiKey/models）；" +
				"https://opencode.ai/docs/config/（顶层 model、{env:VAR} 间接引用）",
			Paths: []string{".config/opencode/opencode.json"},
		},
		{
			Capability: adapter.CapabilityMCP, State: adapter.SupportSupported,
			VerifiedVersions: "文档对应 1.18.x（未在本机实测）",
			Reason:           "local 类型 command/environment 支持；envRefs 按文档化的 {env:VAR} 间接引用渲染（不落明文）",
			Evidence:         "https://opencode.ai/docs/mcp-servers/（mcp.<name>.type=local/command/environment）",
			Paths:            []string{".config/opencode/opencode.json"},
		},
		{
			Capability: adapter.CapabilitySkills, State: adapter.SupportSupported,
			VerifiedVersions: "文档对应 1.18.x（未在本机实测）",
			Evidence:         "https://opencode.ai/docs/skills/（全局 ~/.config/opencode/skills/<name>/SKILL.md；目录名必须匹配 ^[a-z0-9]+(-[a-z0-9]+)*$）",
			Paths:            []string{".config/opencode/skills"},
		},
		{
			Capability: adapter.CapabilityRules, State: adapter.SupportSupported,
			VerifiedVersions: "文档对应 1.18.x（未在本机实测）",
			Evidence: "https://opencode.ai/docs/rules/（全局 ~/.config/opencode/AGENTS.md）。" +
				"注意：官方未定义任何标记块语义，本适配器的 `# BEGIN/END agent-fleet managed` 只是纯文本，块外内容逐字节保留",
			Paths: []string{".config/opencode/AGENTS.md"},
		},
	})
}

// Validate 是阶段 1 的适配器侧前置校验（任何写入之前）。
func (a *Adapter) Validate(_ context.Context, home string, desired adapter.AgentDesiredState) error {
	if a.goos() != "linux" {
		return fmt.Errorf("agent %q: OS %q is unverified in this slice (official Windows guidance is WSL; "+
			"only linux is exercised by fixtures)", ID, a.goos())
	}
	if err := adapter.CheckCapabilities(ID, a.Capabilities(), desired); err != nil {
		return err
	}
	cfg := adapter.ConfigMap(desired)
	if len(desired.Config) != 0 && len(cfg) == 0 {
		return fmt.Errorf("agent %q: config is not a JSON object: %s", ID, string(desired.Config))
	}
	_, hasModel := cfg["model"]
	endpoint, _, hasProvider := adapter.ProviderConfig(cfg)
	if hasProvider {
		if endpoint == "" {
			return fmt.Errorf("agent %q: provider.endpoint is required when providerRef is set", ID)
		}
		if !hasModel {
			return fmt.Errorf("agent %q: config.model is required when providerRef is set "+
				"(OpenCode provider entries are provider/model-id)", ID)
		}
	}
	for name, entry := range desired.MCP {
		if !mcpNameRe.MatchString(name) {
			return fmt.Errorf("agent %q: mcp entry name %q must match %s", ID, name, mcpNameRe)
		}
		if entry.Command == "" {
			return fmt.Errorf("agent %q: mcp entry %q requires command", ID, name)
		}
	}
	for name := range desired.Skills {
		if !skillNameRe.MatchString(name) {
			return &adapter.ValidateError{
				Family: ID, Capability: adapter.CapabilitySkills, State: adapter.SupportUnsupported,
				Reason: fmt.Sprintf("skill name %q violates OpenCode's directory-name rule ^[a-z0-9]+(-[a-z0-9]+)*$ "+
					"(docs/skills); OpenCode would ignore the skill", name),
			}
		}
	}
	if _, _, err := kit.ReadManagedBlock(a.rulesPath(home)); err != nil {
		return fmt.Errorf("agent %q: %w", ID, err)
	}
	if _, err := readConfigFile(a.configPath(home)); err != nil {
		return fmt.Errorf("agent %q: %w", ID, err)
	}
	return nil
}

// Detect 探测已装/未装与版本（矩阵验收 1）。
func (a *Adapter) Detect(ctx context.Context, home string) (adapter.DetectedAgent, error) {
	d := adapter.DetectedAgent{Family: ID, ConfigPath: a.configPath(home), SchemaVer: SchemaVersion}
	v, err := kit.ProbeVersion(ctx, a.Probe, binaryName, versionArgs, parseVersion)
	switch {
	case errors.Is(err, kit.ErrNotInstalled):
		return d, nil
	case err != nil:
		return d, err
	}
	d.Installed, d.Version = true, v
	return d, nil
}

// Inventory 产出 ADR-1 双侧投影。
func (a *Adapter) Inventory(ctx context.Context, home string, desired adapter.AgentDesiredState) (adapter.AgentObservedState, error) {
	desiredProj, err := a.desiredProjection(desired)
	if err != nil {
		return adapter.AgentObservedState{}, err
	}
	observedProj, err := a.observedProjection(home, desired)
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

func (a *Adapter) desiredProjection(desired adapter.AgentDesiredState) (map[string]any, error) {
	proj := map[string]any{}
	cfg := adapter.ConfigMap(desired)
	if len(desired.Config) != 0 && len(cfg) == 0 {
		return nil, fmt.Errorf("opencode: desired config is not a JSON object")
	}
	model, hasModel := cfg["model"]
	endpoint, keyEnv, hasProvider := adapter.ProviderConfig(cfg)
	if hasModel {
		proj["model"] = fmt.Sprintf("%v", model)
		if hasProvider {
			proj["model"] = fmt.Sprintf("%s/%v", providerID, model)
			proj["provider"] = map[string]any{"endpoint": endpoint, "apiKeyEnv": keyEnv, "model": fmt.Sprintf("%v", model)}
		}
	}
	if len(desired.MCP) != 0 {
		mcp := map[string]any{}
		for name, e := range desired.MCP {
			command := append([]string{e.Command}, e.Args...)
			entry := map[string]any{"type": "local", "command": command}
			if len(e.EnvRefs) != 0 {
				entry["environment"] = envRefsOf(e.EnvRefs)
			}
			mcp[name] = entry
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

// envRefsOf 把 envRefs 渲染为 OpenCode 的 {env:VAR} 间接引用（docs/config
// 「Environment variable substitution」）——只写环境变量名，值永不入期望状态。
func envRefsOf(refs map[string]string) map[string]any {
	out := map[string]any{}
	for key, env := range refs {
		out[key] = "{env:" + env + "}"
	}
	return out
}

func (a *Adapter) observedProjection(home string, desired adapter.AgentDesiredState) (map[string]any, error) {
	proj := map[string]any{}
	dcfg := adapter.ConfigMap(desired)
	_, hasModel := dcfg["model"]
	_, _, hasProvider := adapter.ProviderConfig(dcfg)
	if hasModel || hasProvider {
		cfg, err := readConfigFile(a.configPath(home))
		if err != nil {
			return nil, err
		}
		if hasModel {
			proj["model"] = cfg["model"]
		}
		if hasProvider {
			prov := map[string]any{"endpoint": "", "apiKeyEnv": "", "model": ""}
			if providers, ok := cfg["provider"].(map[string]any); ok {
				if block, ok := providers[providerID].(map[string]any); ok {
					if opts, ok := block["options"].(map[string]any); ok {
						endpoint, _ := opts["baseURL"].(string)
						keyEnv := envRefName(opts["apiKey"])
						prov["endpoint"], prov["apiKeyEnv"] = endpoint, keyEnv
					}
					if models, ok := block["models"].(map[string]any); ok {
						for id := range models {
							prov["model"] = id
							break
						}
					}
				}
			}
			proj["provider"] = prov
		}
	}
	if len(desired.MCP) != 0 {
		cfg, err := readConfigFile(a.configPath(home))
		if err != nil {
			return nil, err
		}
		servers, _ := cfg["mcp"].(map[string]any)
		mcp := map[string]any{}
		for name, d := range desired.MCP {
			entry, ok := servers[name].(map[string]any)
			if !ok {
				continue
			}
			typ, _ := entry["type"].(string)
			if typ == "" {
				typ = "local"
			}
			command := []string{}
			if raw, ok := entry["command"].([]any); ok {
				for _, v := range raw {
					if s, ok := v.(string); ok {
						command = append(command, s)
					}
				}
			}
			observed := map[string]any{"type": typ, "command": command}
			if len(d.EnvRefs) != 0 {
				// 必须读**文件里的** environment：用期望值回填会让外部改动
				// 受管 env 不产生 drift（FR-8 判据失效）。
				observed["environment"] = observedEnvRefs(entry["environment"], d.EnvRefs)
			}
			mcp[name] = observed
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

// observedEnvRefs 从原生配置反向读取受管 env 键：只比较期望点名的键，
// 未托管的 env 键不参与判据（§7.1 约束 1）。缺席或形态不再是 `{env:VAR}`
// 都如实回填，从而产生 drift。
func observedEnvRefs(raw any, desired map[string]string) map[string]any {
	env, _ := raw.(map[string]any)
	out := map[string]any{}
	for key := range desired {
		v, ok := env[key]
		if !ok {
			out[key] = ""
			continue
		}
		out[key] = v
	}
	return out
}

// envRefName 反向解析 `{env:VAR}` → VAR（其它形态返回空，表示无 env 间接引用）。
func envRefName(v any) string {
	s, _ := v.(string)
	inner, ok := strings.CutPrefix(s, "{env:")
	if !ok {
		return ""
	}
	return strings.TrimSuffix(inner, "}")
}

// Plan 比较两侧投影产出变更。
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
		changes = append(changes, adapter.Change{Family: ID, Step: "config", Key: "config.model",
			From: observedProj["model"], To: desiredProj["model"]})
	}
	if !equal(desiredProj["provider"], observedProj["provider"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "config", Key: "config.provider",
			From: observedProj["provider"], To: desiredProj["provider"]})
	}
	changes = append(changes, kit.DiffMapStep(ID, "mcp", "mcp.", desiredProj["mcp"], observedProj["mcp"])...)
	changes = append(changes, kit.DiffMapStep(ID, "skills", "skill:", desiredProj["skills"], observedProj["skills"])...)
	if !equal(desiredProj["rules"], observedProj["rules"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "rules", Key: "rules.global",
			From: observedProj["rules"], To: desiredProj["rules"]})
	}
	return changes, nil
}

// Apply 执行变更（阶段 5–9）。
func (a *Adapter) Apply(ctx context.Context, home string, desired adapter.AgentDesiredState, changes []adapter.Change) error {
	byStep := map[string][]adapter.Change{}
	for _, c := range changes {
		byStep[c.Step] = append(byStep[c.Step], c)
	}
	if len(byStep["version"]) != 0 {
		v := a.probeVersionQuiet(ctx)
		if v != desired.Version {
			return fmt.Errorf("opencode: version %s is required but %q is installed and the command installer "+
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
		digest := fmt.Sprintf("%v", c.To)
		if err := kit.EnsureSkillLink(a.skillPath(home, name), name, digest, kit.SkillCacheDir(home, name, digest)); err != nil {
			return fmt.Errorf("opencode: %w", err)
		}
	}
	if len(byStep["rules"]) != 0 {
		if err := kit.WriteManagedBlock(a.rulesPath(home), kit.RulesContent(desired.Rules)); err != nil {
			return fmt.Errorf("opencode: rules: %w", err)
		}
	}
	return nil
}

// applyConfig 合并写（只写受管键，其余键原样保留，§16.1）。JSON 无注释语义，
// 故整树重新序列化：未知键与取值全部保留，但**用户注释不保留**（已知限制，
// 见 docs/adapters-batch-1.md 的未验证/限制清单）。
func (a *Adapter) applyConfig(home string, desired adapter.AgentDesiredState, mcpChanges []adapter.Change) error {
	path := a.configPath(home)
	doc, err := readConfigFile(path)
	if err != nil {
		return err
	}
	cfg := adapter.ConfigMap(desired)
	if model, ok := cfg["model"]; ok {
		if _, _, hasProvider := adapter.ProviderConfig(cfg); hasProvider {
			doc["model"] = fmt.Sprintf("%s/%v", providerID, model)
			endpoint, keyEnv, _ := adapter.ProviderConfig(cfg)
			models := map[string]any{fmt.Sprintf("%v", model): map[string]any{"name": fmt.Sprintf("%v", model)}}
			block := map[string]any{
				"npm":     providerNPM,
				"name":    "agent-fleet",
				"options": map[string]any{"baseURL": endpoint},
				"models":  models,
			}
			if keyEnv != "" {
				block["options"].(map[string]any)["apiKey"] = "{env:" + keyEnv + "}"
			}
			providers, _ := doc["provider"].(map[string]any)
			if providers == nil {
				providers = map[string]any{}
			}
			providers[providerID] = block
			doc["provider"] = providers
		} else {
			doc["model"] = fmt.Sprintf("%v", model)
		}
	}
	if len(mcpChanges) != 0 {
		servers, _ := doc["mcp"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		for _, c := range mcpChanges {
			name := strings.TrimPrefix(c.Key, "mcp.")
			entry, ok := desired.MCP[name]
			if !ok {
				continue
			}
			server := map[string]any{"type": "local", "enabled": true,
				"command": append([]string{entry.Command}, entry.Args...)}
			if len(entry.EnvRefs) != 0 {
				server["environment"] = envRefsOf(entry.EnvRefs)
			}
			servers[name] = server
		}
		doc["mcp"] = servers
	}
	if _, ok := doc["$schema"]; !ok {
		doc["$schema"] = schemaURL
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("opencode: marshal config: %w", err)
	}
	out = append(out, '\n')
	if _, err := decodeJSONC(out); err != nil {
		return fmt.Errorf("opencode: patched config does not parse, aborting: %w", err)
	}
	return kit.AtomicWrite(path, out, 0o644)
}

// HealthCheck（阶段 10）：受管键到位、配置可解析、版本一致。
func (a *Adapter) HealthCheck(ctx context.Context, home string, desired adapter.AgentDesiredState) error {
	cfg, err := readConfigFile(a.configPath(home))
	if err != nil {
		return fmt.Errorf("opencode: health: %w", err)
	}
	dcfg := adapter.ConfigMap(desired)
	if model, ok := dcfg["model"]; ok {
		want := fmt.Sprintf("%v", model)
		if _, _, hasProvider := adapter.ProviderConfig(dcfg); hasProvider {
			want = fmt.Sprintf("%s/%v", providerID, model)
		}
		if got := fmt.Sprintf("%v", cfg["model"]); got != want {
			return fmt.Errorf("opencode: health: model is %q, want %q", got, want)
		}
	}
	if endpoint, keyEnv, ok := adapter.ProviderConfig(dcfg); ok {
		providers, _ := cfg["provider"].(map[string]any)
		block, _ := providers[providerID].(map[string]any)
		opts, _ := block["options"].(map[string]any)
		if fmt.Sprintf("%v", opts["baseURL"]) != endpoint {
			return fmt.Errorf("opencode: health: provider.%s.options.baseURL is not in place", providerID)
		}
		if keyEnv != "" && envRefName(opts["apiKey"]) != keyEnv {
			return fmt.Errorf("opencode: health: provider.%s.options.apiKey is not the {env:%s} reference", providerID, keyEnv)
		}
	}
	if len(desired.MCP) != 0 {
		servers, _ := cfg["mcp"].(map[string]any)
		for name := range desired.MCP {
			if _, ok := servers[name].(map[string]any); !ok {
				return fmt.Errorf("opencode: health: mcp server %q is not registered after apply", name)
			}
		}
	}
	if desired.Version != "" {
		v, err := kit.ProbeVersion(ctx, a.Probe, binaryName, versionArgs, parseVersion)
		if err != nil {
			return fmt.Errorf("opencode: health: version probe failed: %w", err)
		}
		if v != desired.Version {
			return fmt.Errorf("opencode: health: installed version %s != desired %s", v, desired.Version)
		}
	}
	for name, s := range desired.Skills {
		if got := kit.ReadSkillDigest(a.skillPath(home, name)); got != s.ContentDigest {
			return fmt.Errorf("opencode: health: skill %q link digest is %q, want %q", name, got, s.ContentDigest)
		}
	}
	if kit.RulesContent(desired.Rules) != "" {
		content, ok, err := kit.ReadManagedBlock(a.rulesPath(home))
		if err != nil {
			return fmt.Errorf("opencode: health: %w", err)
		}
		if !ok || content != kit.RulesContent(desired.Rules) {
			return fmt.Errorf("opencode: health: rules managed block is missing or differs")
		}
	}
	return nil
}

// ---- 路径（相对注入的 home 根；遵循官方 `~/.config/opencode/` 约定）----

func (a *Adapter) dir(home string) string { return filepath.Join(home, ".config", "opencode") }
func (a *Adapter) configPath(home string) string {
	return filepath.Join(a.dir(home), "opencode.json")
}
func (a *Adapter) rulesPath(home string) string { return filepath.Join(a.dir(home), "AGENTS.md") }
func (a *Adapter) skillPath(home, name string) string {
	return filepath.Join(a.dir(home), "skills", name)
}

// ---- 受管文件声明（reconciler 备份/恢复契约，§5.3）----

// ManagedFiles 声明本适配器写入的文件（相对 home）。
func (a *Adapter) ManagedFiles(home string) ([]string, error) {
	return []string{
		filepath.Join(".config", "opencode", "opencode.json"),
		filepath.Join(".config", "opencode", "AGENTS.md"),
	}, nil
}

// ExtractManaged 提取受管键值。
func (a *Adapter) ExtractManaged(content []byte) (map[string]any, error) {
	out := map[string]any{}
	if doc, err := decodeJSONC(content); err == nil {
		if v, ok := doc["model"]; ok {
			out["model"] = v
		}
		if providers, ok := doc["provider"].(map[string]any); ok {
			if block, ok := providers[providerID]; ok {
				out["provider."+providerID] = block
			}
		}
		if servers, ok := doc["mcp"].(map[string]any); ok {
			for name, v := range servers {
				out["mcp."+name] = v
			}
		}
	}
	if block, ok, err := kit.ManagedBlockIn(string(content)); err == nil && ok {
		out["agentFleetRules"] = block
	}
	return out, nil
}

// MergeManaged 受管字段级回退（§5.3 契约 3）。
func (a *Adapter) MergeManaged(home, relPath string, managed map[string]any) error {
	switch relPath {
	case filepath.Join(".config", "opencode", "opencode.json"):
		path := a.configPath(home)
		doc, err := readConfigFile(path)
		if err != nil {
			return err
		}
		for k, v := range managed {
			if k == "agentFleetRules" {
				continue
			}
			if key, ok := strings.CutPrefix(k, "provider."); ok {
				providers, _ := doc["provider"].(map[string]any)
				if providers == nil {
					providers = map[string]any{}
				}
				providers[key] = v
				doc["provider"] = providers
				continue
			}
			if key, ok := strings.CutPrefix(k, "mcp."); ok {
				servers, _ := doc["mcp"].(map[string]any)
				if servers == nil {
					servers = map[string]any{}
				}
				servers[key] = v
				doc["mcp"] = servers
				continue
			}
			doc[k] = v
		}
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		return kit.AtomicWrite(path, append(out, '\n'), 0o644)
	case filepath.Join(".config", "opencode", "AGENTS.md"):
		content, _ := managed["agentFleetRules"].(string)
		return kit.WriteManagedBlock(a.rulesPath(home), content)
	default:
		return fmt.Errorf("opencode: %s is not a managed-key-restorable file", relPath)
	}
}

// ---- JSON/JSONC ----

// readConfigFile 读并解析配置（JSON 或 JSONC；文件不存在视为空对象）。
func readConfigFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opencode: read config: %w", err)
	}
	doc, err := decodeJSONC(raw)
	if err != nil {
		return nil, fmt.Errorf("opencode: config unparseable: %w", err)
	}
	return doc, nil
}

// decodeJSONC 解析 JSON/JSONC：先去掉字符串外的 // 与 /* */ 注释再走标准库。
func decodeJSONC(raw []byte) (map[string]any, error) {
	clean := stripJSONComments(raw)
	out := map[string]any{}
	if len(strings.TrimSpace(string(clean))) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(clean, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func stripJSONComments(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	inString, escaped := false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case inString:
			out = append(out, c)
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(raw) && raw[i+1] == '/':
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			if i < len(raw) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(raw) && raw[i+1] == '*':
			i += 2
			for i+1 < len(raw) && !(raw[i] == '*' && raw[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	return out
}

func equal(a, b any) bool { return kit.ValuesEqual(a, b) }
