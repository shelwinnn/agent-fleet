// Package omp 是 Oh-My-Pi (OMP) 家族适配器（架构 v1.1.2 §5.4/§15.2；批次一）。
//
// 运行时来源与证据见 docs/adapters-batch-1.md「OMP」一节：
//   - 发行包 `@oh-my-pi/pi-coding-agent`（bin `omp`），本次接入实测版本 17.4.0
//     （本机 `omp --version` → `omp/17.4.0`，npm 上该版本发布于 2026-08-20）；
//   - 全局设置 `~/.omp/agent/config.yml`（官方 docs/settings.md「Where settings live」
//     明确它是 canonical write target；已存在的 config.yaml 就地更新）；
//   - 模型角色 `modelRoles.default: <provider>/<model>`（docs/settings.md 示例，
//     与本机 `zai/glm-5.3` 一致）；
//   - 自定义 provider 与凭据在 `~/.omp/agent/models.yml`（docs/providers.md；
//     `apiKey` 写环境变量名即可，OMP 自行按名字解析，值永不入期望状态）；
//   - MCP 独立文件 `~/.omp/agent/mcp.json`（docs/mcp-config.md，`mcpServers` 顶层键）；
//   - Skills `~/.omp/agent/skills/<name>/SKILL.md`（docs/skills.md）；
//   - 指令 `~/.omp/agent/AGENTS.md`（docs/context-files.md，用户级上下文文件）。
//
// 受管范围（其余键/文件一律未托管）：
//
//	~/.omp/agent/config.yml      modelRoles.default
//	~/.omp/agent/models.yml      providers.fleet.{baseUrl,api,apiKey,models}
//	~/.omp/agent/mcp.json        mcpServers.<name>（仅期望点名的条目）
//	~/.omp/agent/AGENTS.md       rules 受管标记块
//	~/.omp/agent/skills/<name>   Skill 软链
package omp

import (
	"context"
	"encoding/json"
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
	// ID 是家族标识（矩阵统一名称 Oh-My-Pi（OMP））。
	ID = "omp"
	// SchemaVersion 是本适配器受管 schema 版本。
	SchemaVersion = "omp-config-v1"
	// binaryName 是可执行程序名。
	binaryName = "omp"
	// providerID 是 Fleet 在 OMP models.yml 中的 provider 命名空间。
	providerID = "fleet"
	// providerAPI 是自定义 OpenAI 兼容 provider 的 api 取值（docs/providers.md 示例）。
	providerAPI = "openai-completions"
)

// versionArgs 是版本探测参数（docs/cli-reference.md：`--version` 打印已装版本）。
var versionArgs = []string{"--version"}

var (
	versionRe = regexp.MustCompile(`^omp/([0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?)$`)
	safeName  = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
)

// parseVersion 解析 `omp/<semver>`；形态不符返回 ""（版本不可解析必须可区分）。
func parseVersion(out string) string {
	line := kit.FirstLine(out)
	m := versionRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return m[1]
}

// Adapter 是 OMP 适配器。Probe/GOOS 可注入以便 fixture 测试。
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
			VerifiedVersions: "omp 17.4.0 (linux/amd64)",
			Evidence:         "本机 `omp --version` → `omp/17.4.0`；npm `@oh-my-pi/pi-coding-agent` 17.4.0（published 2026-08-20）",
			Paths:            []string{".local/bin/omp"},
		},
		{
			Capability: adapter.CapabilityModelProvider, State: adapter.SupportSupported,
			VerifiedVersions: "omp 17.4.0（文档证据：settings.md / providers.md）",
			Evidence:         "docs/settings.md「Models」modelRoles.default: <provider>/<model>；docs/providers.md 自定义 provider models.yml（baseUrl/api/apiKey/models）。本机 config.yml 的 modelRoles.default 实测为该格式；models.yml 本机不存在，未实测",
			Paths:            []string{".omp/agent/config.yml", ".omp/agent/models.yml"},
		},
		{
			Capability: adapter.CapabilityMCP, State: adapter.SupportSupported,
			VerifiedVersions: "omp 17.4.0（文档证据：docs/mcp-config.md）",
			Evidence:         "docs/mcp-config.md：~/.omp/agent/mcp.json 顶层 mcpServers.<name>.{type,command,args,env}",
			Paths:            []string{".omp/agent/mcp.json"},
		},
		{
			Capability: adapter.CapabilitySkills, State: adapter.SupportSupported,
			VerifiedVersions: "omp 17.4.0（文档证据：docs/skills.md）",
			Evidence:         "docs/skills.md：~/.omp/agent/skills/<skill-name>/SKILL.md（非递归一层）",
			Paths:            []string{".omp/agent/skills"},
		},
		{
			Capability: adapter.CapabilityRules, State: adapter.SupportSupported,
			VerifiedVersions: "omp 17.4.0（文档证据：docs/context-files.md）",
			Evidence:         "docs/context-files.md：~/.omp/agent/AGENTS.md 为用户级上下文文件；~/.omp/agent/RULES.md 为 always-apply 规则",
			Paths:            []string{".omp/agent/AGENTS.md"},
		},
	})
}

// Validate 是阶段 1 的适配器侧前置校验（任何写入之前）。
func (a *Adapter) Validate(_ context.Context, home string, desired adapter.AgentDesiredState) error {
	if a.goos() != "linux" {
		return fmt.Errorf("agent %q: OS %q is unverified (only linux/amd64 verified on omp 17.4.0)", ID, a.goos())
	}
	if err := adapter.CheckCapabilities(ID, a.Capabilities(), desired); err != nil {
		return err
	}
	cfg := adapter.ConfigMap(desired)
	if len(desired.Config) != 0 && len(cfg) == 0 {
		return fmt.Errorf("agent %q: config is not a JSON object: %s", ID, string(desired.Config))
	}
	_, modelSet := cfg["model"]
	endpoint, _, providerSet := adapter.ProviderConfig(cfg)
	if providerSet {
		// modelRoles.default 的选择器格式是 <provider>/<model>（docs/settings.md），
		// 缺一部分就无法渲染：写入前明确拒绝，而不是写出无效选择器。
		if endpoint == "" {
			return fmt.Errorf("agent %q: provider.endpoint is required when providerRef is set", ID)
		}
		if !modelSet {
			return fmt.Errorf("agent %q: config.model is required when providerRef is set "+
				"(OMP modelRoles selectors are <provider>/<model>)", ID)
		}
	}
	for name, entry := range desired.MCP {
		if !safeName.MatchString(name) || len(name) > 100 {
			return fmt.Errorf("agent %q: mcp entry name %q must match %s and be ≤100 chars", ID, name, safeName)
		}
		if entry.Command == "" {
			return fmt.Errorf("agent %q: mcp entry %q requires command", ID, name)
		}
		if len(entry.EnvRefs) != 0 {
			return &adapter.ValidateError{
				Family: ID, Capability: adapter.CapabilityMCP, State: adapter.SupportUnverified,
				Reason: fmt.Sprintf("mcp entry %q requests envRefs; docs/mcp-config.md documents only literal "+
					"env values for omp 17.4.0, so ${VAR} expansion is unverified", name),
			}
		}
	}
	if _, _, err := kit.ReadManagedBlock(a.rulesPath(home)); err != nil {
		return fmt.Errorf("agent %q: %w", ID, err)
	}
	if _, err := parseYAML(readFileOrEmpty(a.configPath(home))); err != nil {
		return fmt.Errorf("agent %q: config.yml unparseable: %w", ID, err)
	}
	if _, err := a.readMCPFile(home); err != nil {
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

// Inventory 产出 ADR-1 双侧投影（受管键集合 = 期望点名的键）。
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
		return nil, fmt.Errorf("omp: desired config is not a JSON object")
	}
	model, hasModel := cfg["model"]
	endpoint, keyEnv, hasProvider := adapter.ProviderConfig(cfg)
	if hasModel || hasProvider {
		if !hasModel || !hasProvider {
			return nil, fmt.Errorf("omp: model and provider must be managed together (modelRoles selector is <provider>/<model>)")
		}
		proj["selector"] = fmt.Sprintf("%s/%v", providerID, model)
		proj["provider"] = map[string]any{"endpoint": endpoint, "apiKeyEnv": keyEnv, "model": fmt.Sprintf("%v", model)}
	}
	if len(desired.MCP) != 0 {
		mcp := map[string]any{}
		for name, e := range desired.MCP {
			mcp[name] = map[string]any{"type": "stdio", "command": e.Command, "args": e.Args}
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

func (a *Adapter) observedProjection(home string, desired adapter.AgentDesiredState) (map[string]any, error) {
	proj := map[string]any{}
	dcfg := adapter.ConfigMap(desired)
	_, hasModel := dcfg["model"]
	_, _, hasProvider := adapter.ProviderConfig(dcfg)
	if hasModel || hasProvider {
		root, err := parseYAML(readFileOrEmpty(a.configPath(home)))
		if err != nil {
			return nil, fmt.Errorf("omp: config.yml unparseable: %w", err)
		}
		selector, _ := lookupPath(root, "modelRoles.default").(string)
		proj["selector"] = selector
		prov := map[string]any{"endpoint": "", "apiKeyEnv": "", "model": ""}
		modelsRoot, err := parseYAML(readFileOrEmpty(a.modelsPath(home)))
		if err != nil {
			return nil, fmt.Errorf("omp: models.yml unparseable: %w", err)
		}
		if block, ok := lookupPath(modelsRoot, "providers."+providerID).(map[string]any); ok {
			endpoint, _ := block["baseUrl"].(string)
			keyEnv, _ := block["apiKey"].(string)
			prov["endpoint"], prov["apiKeyEnv"] = endpoint, keyEnv
			if list, ok := block["models"].([]any); ok && len(list) > 0 {
				if first, ok := list[0].(map[string]any); ok {
					prov["model"], _ = first["id"].(string)
				}
			}
		}
		proj["provider"] = prov
	}
	if len(desired.MCP) != 0 {
		mcpCfg, err := a.readMCPFile(home)
		if err != nil {
			return nil, err
		}
		servers := map[string]any{}
		if raw, ok := mcpCfg["mcpServers"].(map[string]any); ok {
			servers = raw
		}
		mcp := map[string]any{}
		for name := range desired.MCP {
			entry, ok := servers[name].(map[string]any)
			if !ok {
				continue
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
			typ, _ := entry["type"].(string)
			if typ == "" {
				typ = "stdio"
			}
			mcp[name] = map[string]any{"type": typ, "command": command, "args": args}
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
	if !equal(desiredProj["selector"], observedProj["selector"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "config", Key: "config.modelRoles.default",
			From: observedProj["selector"], To: desiredProj["selector"]})
	}
	if !equal(desiredProj["provider"], observedProj["provider"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "config", Key: "config.provider",
			From: observedProj["provider"], To: desiredProj["provider"]})
	}
	changes = append(changes, diffMapStep("mcp", "mcp.", desiredProj["mcp"], observedProj["mcp"])...)
	changes = append(changes, diffMapStep("skills", "skill:", desiredProj["skills"], observedProj["skills"])...)
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
			return fmt.Errorf("omp: version %s is required but %q is installed and the command installer "+
				"(§20.2) is not wired in this slice", desired.Version, v)
		}
	}
	if len(byStep["config"]) != 0 {
		if err := a.applyConfig(home, desired); err != nil {
			return err
		}
	}
	if len(byStep["mcp"]) != 0 {
		if err := a.applyMCP(home, desired, byStep["mcp"]); err != nil {
			return err
		}
	}
	for _, c := range byStep["skills"] {
		name := strings.TrimPrefix(c.Key, "skill:")
		digest := fmt.Sprintf("%v", c.To)
		if err := kit.EnsureSkillLink(a.skillPath(home, name), name, digest, kit.SkillCacheDir(home, name, digest)); err != nil {
			return fmt.Errorf("omp: %w", err)
		}
	}
	if len(byStep["rules"]) != 0 {
		if err := kit.WriteManagedBlock(a.rulesPath(home), kit.RulesContent(desired.Rules)); err != nil {
			return fmt.Errorf("omp: rules: %w", err)
		}
	}
	return nil
}

func (a *Adapter) applyConfig(home string, desired adapter.AgentDesiredState) error {
	cfg := adapter.ConfigMap(desired)
	model, hasModel := cfg["model"]
	endpoint, keyEnv, hasProvider := adapter.ProviderConfig(cfg)
	if !hasModel || !hasProvider {
		return fmt.Errorf("omp: model and provider must be managed together")
	}
	// config.yml：模型角色选择器（注释与未托管键经 Node 保留）。
	configPath := a.configPath(home)
	out, err := patchYAML(readFileOrEmpty(configPath), map[string]any{
		"modelRoles.default": fmt.Sprintf("%s/%v", providerID, model),
	})
	if err != nil {
		return fmt.Errorf("omp: patch config.yml: %w", err)
	}
	if _, err := parseYAML(out); err != nil {
		return fmt.Errorf("omp: patched config.yml does not parse, aborting: %w", err)
	}
	if err := kit.AtomicWrite(configPath, []byte(out), 0o600); err != nil {
		return err
	}
	// models.yml：自定义 provider 定义（apiKey 只写环境变量名，值不入期望状态）。
	modelsPath := a.modelsPath(home)
	modelID := fmt.Sprintf("%v", model)
	prov := map[string]any{
		"baseUrl": endpoint,
		"api":     providerAPI,
		"models":  []any{map[string]any{"id": modelID, "name": modelID}},
	}
	if keyEnv != "" {
		prov["apiKey"] = keyEnv
	}
	out, err = patchYAML(readFileOrEmpty(modelsPath), map[string]any{
		"providers." + providerID: prov,
	})
	if err != nil {
		return fmt.Errorf("omp: patch models.yml: %w", err)
	}
	if _, err := parseYAML(out); err != nil {
		return fmt.Errorf("omp: patched models.yml does not parse, aborting: %w", err)
	}
	return kit.AtomicWrite(modelsPath, []byte(out), 0o600)
}

func (a *Adapter) applyMCP(home string, desired adapter.AgentDesiredState, mcpChanges []adapter.Change) error {
	path := a.mcpPath(home)
	doc, err := a.readMCPFile(home)
	if err != nil {
		return err
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	for _, c := range mcpChanges {
		name := strings.TrimPrefix(c.Key, "mcp.")
		entry, ok := desired.MCP[name]
		if !ok {
			continue
		}
		server := map[string]any{"type": "stdio", "command": entry.Command}
		if len(entry.Args) != 0 {
			server["args"] = entry.Args
		}
		servers[name] = server
	}
	doc["mcpServers"] = servers
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("omp: marshal mcp.json: %w", err)
	}
	out = append(out, '\n')
	// 写后解析验证（§16.1）。
	if _, err := readMCPBytes(out); err != nil {
		return fmt.Errorf("omp: patched mcp.json does not parse, aborting: %w", err)
	}
	return kit.AtomicWrite(path, out, 0o600)
}

// HealthCheck（阶段 10）：受管键到位、文件可解析、版本一致。
func (a *Adapter) HealthCheck(ctx context.Context, home string, desired adapter.AgentDesiredState) error {
	dcfg := adapter.ConfigMap(desired)
	_, hasModel := dcfg["model"]
	_, _, hasProvider := adapter.ProviderConfig(dcfg)
	if hasModel || hasProvider {
		root, err := parseYAML(readFileOrEmpty(a.configPath(home)))
		if err != nil {
			return fmt.Errorf("omp: health: config.yml unparseable: %w", err)
		}
		want := fmt.Sprintf("%s/%v", providerID, dcfg["model"])
		if got, _ := lookupPath(root, "modelRoles.default").(string); got != want {
			return fmt.Errorf("omp: health: modelRoles.default is %q, want %q", got, want)
		}
		modelsRoot, err := parseYAML(readFileOrEmpty(a.modelsPath(home)))
		if err != nil {
			return fmt.Errorf("omp: health: models.yml unparseable: %w", err)
		}
		block, ok := lookupPath(modelsRoot, "providers."+providerID).(map[string]any)
		if !ok {
			return fmt.Errorf("omp: health: providers.%s is missing", providerID)
		}
		if endpoint, _, _ := adapter.ProviderConfig(dcfg); fmt.Sprintf("%v", block["baseUrl"]) != endpoint {
			return fmt.Errorf("omp: health: providers.%s.baseUrl is not in place", providerID)
		}
	}
	if len(desired.MCP) != 0 {
		doc, err := a.readMCPFile(home)
		if err != nil {
			return fmt.Errorf("omp: health: %w", err)
		}
		servers, _ := doc["mcpServers"].(map[string]any)
		for name := range desired.MCP {
			if _, ok := servers[name].(map[string]any); !ok {
				return fmt.Errorf("omp: health: mcp server %q is not registered after apply", name)
			}
		}
	}
	if desired.Version != "" {
		v, err := kit.ProbeVersion(ctx, a.Probe, binaryName, versionArgs, parseVersion)
		if err != nil {
			return fmt.Errorf("omp: health: version probe failed: %w", err)
		}
		if v != desired.Version {
			return fmt.Errorf("omp: health: installed version %s != desired %s", v, desired.Version)
		}
	}
	for name, s := range desired.Skills {
		if got := kit.ReadSkillDigest(a.skillPath(home, name)); got != s.ContentDigest {
			return fmt.Errorf("omp: health: skill %q link digest is %q, want %q", name, got, s.ContentDigest)
		}
	}
	if kit.RulesContent(desired.Rules) != "" {
		content, ok, err := kit.ReadManagedBlock(a.rulesPath(home))
		if err != nil {
			return fmt.Errorf("omp: health: %w", err)
		}
		if !ok || content != kit.RulesContent(desired.Rules) {
			return fmt.Errorf("omp: health: rules managed block is missing or differs")
		}
	}
	return nil
}

// ---- 路径（相对注入的 home 根，护栏 #12）----

func (a *Adapter) agentDir(home string) string { return filepath.Join(home, ".omp", "agent") }
func (a *Adapter) configPath(home string) string {
	return filepath.Join(a.agentDir(home), "config.yml")
}
func (a *Adapter) modelsPath(home string) string {
	return filepath.Join(a.agentDir(home), "models.yml")
}
func (a *Adapter) mcpPath(home string) string   { return filepath.Join(a.agentDir(home), "mcp.json") }
func (a *Adapter) rulesPath(home string) string { return filepath.Join(a.agentDir(home), "AGENTS.md") }
func (a *Adapter) skillPath(home, name string) string {
	return filepath.Join(a.agentDir(home), "skills", name)
}

// ---- 受管文件声明（reconciler 备份/恢复契约，§5.3）----

// ManagedFiles 声明本适配器写入的文件（相对 home）。
func (a *Adapter) ManagedFiles(home string) ([]string, error) {
	return []string{
		filepath.Join(".omp", "agent", "config.yml"),
		filepath.Join(".omp", "agent", "models.yml"),
		filepath.Join(".omp", "agent", "mcp.json"),
		filepath.Join(".omp", "agent", "AGENTS.md"),
	}, nil
}

// ExtractManaged 提取受管键值。
func (a *Adapter) ExtractManaged(content []byte) (map[string]any, error) {
	out := map[string]any{}
	root, err := parseYAML(string(content))
	if err == nil {
		if v := lookupPath(root, "modelRoles.default"); v != nil {
			out["modelRoles.default"] = v
		}
		if v := lookupPath(root, "providers."+providerID); v != nil {
			out["providers."+providerID] = v
		}
		var doc map[string]any
		if json.Unmarshal(content, &doc) == nil {
			if servers, ok := doc["mcpServers"].(map[string]any); ok {
				for name, v := range servers {
					out["mcpServers."+name] = v
				}
			}
		}
	}
	if block, ok, blockErr := kit.ManagedBlockIn(string(content)); blockErr == nil && ok {
		out["agentFleetRules"] = block
	}
	return out, nil
}

// MergeManaged 受管字段级回退（§5.3 契约 3）。
func (a *Adapter) MergeManaged(home, relPath string, managed map[string]any) error {
	switch relPath {
	case filepath.Join(".omp", "agent", "config.yml"), filepath.Join(".omp", "agent", "models.yml"):
		path := a.modelsPath(home)
		if relPath == filepath.Join(".omp", "agent", "config.yml") {
			path = a.configPath(home)
		}
		sets := map[string]any{}
		for k, v := range managed {
			if k == "agentFleetRules" {
				continue
			}
			sets[k] = v
		}
		out, err := patchYAML(readFileOrEmpty(path), sets)
		if err != nil {
			return err
		}
		if _, err := parseYAML(out); err != nil {
			return fmt.Errorf("omp: managed rollback produced unparseable yaml: %w", err)
		}
		return kit.AtomicWrite(path, []byte(out), 0o600)
	case filepath.Join(".omp", "agent", "mcp.json"):
		cur, err := a.readMCPFile(home)
		if err != nil {
			return err
		}
		servers, _ := cur["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		for k, v := range managed {
			if name, ok := strings.CutPrefix(k, "mcpServers."); ok {
				servers[name] = v
			}
		}
		cur["mcpServers"] = servers
		out, err := json.MarshalIndent(cur, "", "  ")
		if err != nil {
			return err
		}
		return kit.AtomicWrite(a.mcpPath(home), append(out, '\n'), 0o600)
	case filepath.Join(".omp", "agent", "AGENTS.md"):
		content, _ := managed["agentFleetRules"].(string)
		return kit.WriteManagedBlock(a.rulesPath(home), content)
	default:
		return fmt.Errorf("omp: %s is not a managed-key-restorable file", relPath)
	}
}

// ---- 小工具 ----

func (a *Adapter) readMCPFile(home string) (map[string]any, error) {
	b, err := os.ReadFile(a.mcpPath(home))
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("omp: read mcp.json: %w", err)
	}
	return readMCPBytes(b)
}

func readMCPBytes(raw []byte) (map[string]any, error) {
	out := map[string]any{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcp.json unparseable: %w", err)
	}
	return out, nil
}

func readFileOrEmpty(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func equal(a, b any) bool { return fmt.Sprintf("%#v", a) == fmt.Sprintf("%#v", b) }

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
		if equal(desired[n], observed[n]) {
			continue
		}
		out = append(out, adapter.Change{Family: ID, Step: step, Key: keyPrefix + n,
			From: observed[n], To: desired[n]})
	}
	return out
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
