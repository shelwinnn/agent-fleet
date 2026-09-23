// Package fixture 是语义验证用 fixture 适配器（KM-23 边界：适配器实现留给第 4 片，
// 本片用 fixture 验证 reconcile/drift/回滚语义）。它以真实文件读写落实 §5.4/§5.5
// 契约：合并写保留未托管字段、原子写、投影只哈希受管键（FR-8.2）、双侧投影同源
// 同版本（ADR-1）。真实家族适配器接入后本包仅保留给测试使用。
package fixture

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/desiredstate"
)

// ID 是 fixture 家族名。
const ID = "fixture"

// CanonicalizationVersion 是 fixture 投影规范化规则版本（ADR-1：期望侧与观测侧
// 必须一致；与期望快照的 snapshot 规范化版本是两套独立规则，§7.1）。
// 与家族适配器共用同一节点侧规则版本，使多家族聚合摘要可比（§7.1 契约 1）。
const CanonicalizationVersion = adapter.ProjectionCanonicalizationVersion

// 受管键集合（§16 所有权声明的 fixture 具象）。其余键一律未托管：
// 未托管编辑不得改变投影摘要、不得触发 drift、不得被合并写覆盖。
var managedKeys = []string{"model", "providerEndpoint"}

// Adapter 是 fixture 家族适配器。文件布局（相对注入的 home 根，护栏 #12）：
//
//	<home>/fixture/config.json   受管+未托管混合的 JSON 配置
//	<home>/fixture/version       已装版本（非空即已安装）
//	<home>/fixture/skills/<name>.skill  内容为期望 contentDigest 的标记文件
type Adapter struct {
	mu sync.Mutex
	// FailNext 使下一次指定步骤的执行失败（故障注入，T7/T21 用）：step → 剩余次数。
	FailNext map[string]int
}

func New() *Adapter { return &Adapter{FailNext: map[string]int{}} }

func (a *Adapter) ID() string { return ID }

// Capabilities 是 fixture 的能力声明（测试用适配器：全部能力"支持"仅表示
// 语义可测，不代表任何真实家族）。
func (a *Adapter) Capabilities() []adapter.CapabilityDecl {
	return []adapter.CapabilityDecl{
		{Capability: adapter.CapabilityVersion, State: adapter.SupportSupported, Evidence: "fixture"},
		{Capability: adapter.CapabilityModelProvider, State: adapter.SupportSupported, Evidence: "fixture"},
		{Capability: adapter.CapabilityMCP, State: adapter.SupportSupported, Evidence: "fixture"},
		{Capability: adapter.CapabilitySkills, State: adapter.SupportSupported, Evidence: "fixture"},
		{Capability: adapter.CapabilityRules, State: adapter.SupportSupported, Evidence: "fixture"},
	}
}

// Validate 落实阶段 1 能力前置校验（真实家族据此在写入前拒绝未支持能力）。
func (a *Adapter) Validate(_ context.Context, _ string, desired adapter.AgentDesiredState) error {
	return adapter.CheckCapabilities(ID, a.Capabilities(), desired)
}

// fail consums 一次注入；返回是否命中注入。
func (a *Adapter) fail(step string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.FailNext[step] > 0 {
		a.FailNext[step]--
		return true
	}
	return false
}

func (a *Adapter) Detect(_ context.Context, home string) (adapter.DetectedAgent, error) {
	d := adapter.DetectedAgent{Family: ID, SchemaVer: CanonicalizationVersion}
	if v, err := os.ReadFile(a.versionPath(home)); err == nil && len(v) > 0 {
		d.Installed = true
		d.Version = string(v)
	}
	d.ConfigPath = a.configPath(home)
	return d, nil
}

// Inventory 落实 ADR-1 双侧投影：给定 desired 算期望侧投影摘要，读本机配置算
// 观测侧投影摘要，同一 canonicalizationVersion 一并返回（§5.4 适配器义务 1）。
func (a *Adapter) Inventory(_ context.Context, home string, desired adapter.AgentDesiredState) (adapter.AgentObservedState, error) {
	desiredKeys, err := managedProjection(desired.Config)
	if err != nil {
		return adapter.AgentObservedState{}, fmt.Errorf("fixture: desired projection: %w", err)
	}
	current, err := a.readConfig(home)
	if err != nil {
		return adapter.AgentObservedState{}, err
	}
	observedKeys := projectionOf(current)
	version := a.installedVersion(home)
	return adapter.AgentObservedState{
		Family:                   ID,
		Version:                  version,
		ManagedProjection:        observedKeys,
		DesiredProjectionDigest:  projectionDigest(desiredKeys),
		ObservedProjectionDigest: projectionDigest(observedKeys),
		CanonicalizationVersion:  CanonicalizationVersion,
	}, nil
}

// Plan 比较 desired 与 observed 产出变更清单（阶段 3）。observed 由 reconciler
// 从 Inventory 透传，计划的确定性由双方摘要与变更集共同保证（planDigest 输入）。
func (a *Adapter) Plan(_ context.Context, home string, desired adapter.AgentDesiredState, observed adapter.AgentObservedState) ([]adapter.Change, error) {
	var changes []adapter.Change
	desiredKeys, err := managedProjection(desired.Config)
	if err != nil {
		return nil, err
	}
	for _, k := range managedKeys {
		to, has := desiredKeys[k]
		from, cur := observed.ManagedProjection[k]
		switch {
		case cur && !has:
			// 期望不含该受管键：不删除（fixture 的受管键集固定，属"期望空缺"）。
			continue
		case !cur || fmt.Sprintf("%v", from) != fmt.Sprintf("%v", to):
			changes = append(changes, adapter.Change{Family: ID, Step: "config", Key: "config." + k, From: from, To: to})
		}
	}
	// 版本变更（阶段 5）。
	if desired.Version != "" && observed.Version != desired.Version {
		changes = append(changes, adapter.Change{Family: ID, Step: "version", Key: "version", From: observed.Version, To: desired.Version})
	}
	// Skill 变更（阶段 7）：期望 digest 与本机标记文件内容比较（drift 信号 = 内容 digest，FR-6.3）。
	if cfg := a.skillsOf(desired); len(cfg) != 0 {
		for _, name := range sortedKeys(cfg) {
			want := cfg[name]
			if got := a.skillDigest(home, name); got != want {
				changes = append(changes, adapter.Change{Family: ID, Step: "skills", Key: "skill:" + name, From: got, To: want})
			}
		}
	}
	return changes, nil
}

// Apply 执行变更（阶段 5/6/7）：合并写（只写受管键，未托管键原样保留，§5.5）、
// 原子写（临时文件 + rename）、版本写入。makeChanges 为空时零写入（幂等，FR-9.3）。
func (a *Adapter) Apply(_ context.Context, home string, desired adapter.AgentDesiredState, changes []adapter.Change) error {
	hasVersion, hasConfig, hasSkill := false, false, map[string]string{}
	for _, c := range changes {
		switch c.Step {
		case "version":
			hasVersion = true
		case "config":
			hasConfig = true
		case "skills":
			name := c.Key[len("skill:"):]
			hasSkill[name] = fmt.Sprintf("%v", c.To)
		}
	}
	if hasVersion {
		if a.fail("version") {
			return fmt.Errorf("fixture: injected version apply failure")
		}
		if err := os.MkdirAll(filepath.Dir(a.versionPath(home)), 0o755); err != nil {
			return fmt.Errorf("fixture: create dir: %w", err)
		}
		if err := atomicWrite(a.versionPath(home), []byte(desired.Version), 0o644); err != nil {
			return fmt.Errorf("fixture: write version: %w", err)
		}
	}
	if hasConfig {
		if a.fail("config") {
			return fmt.Errorf("fixture: injected config apply failure")
		}
		desiredKeys, err := managedProjection(desired.Config)
		if err != nil {
			return err
		}
		if err := a.mergeWriteConfig(home, desiredKeys); err != nil {
			return err
		}
	}
	for name, digest := range hasSkill {
		path := a.skillPath(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("fixture: create skills dir: %w", err)
		}
		if err := atomicWrite(path, []byte(digest), 0o644); err != nil {
			return fmt.Errorf("fixture: write skill %s: %w", name, err)
		}
	}
	return nil
}

// HealthCheck 适配器健康检查（阶段 10）：配置可解析且受管键已到位（§5.5 写后
// 验证子集）；不验证外部服务可用性（FR-4.3）。
func (a *Adapter) HealthCheck(_ context.Context, home string, desired adapter.AgentDesiredState) error {
	if a.fail("health") {
		return fmt.Errorf("fixture: injected health failure")
	}
	current, err := a.readConfig(home)
	if err != nil {
		return fmt.Errorf("fixture: health: config unreadable: %w", err)
	}
	desiredKeys, err := managedProjection(desired.Config)
	if err != nil {
		return err
	}
	for _, k := range managedKeys {
		if _, ok := desiredKeys[k]; ok {
			if _, ok := current[k]; !ok {
				return fmt.Errorf("fixture: health: managed key %q missing after apply", k)
			}
		}
	}
	if v := a.installedVersion(home); desired.Version != "" && v == "" {
		return fmt.Errorf("fixture: health: version missing")
	}
	return nil
}

// ---- 文件布局与投影 ----

func (a *Adapter) configPath(home string) string  { return filepath.Join(home, ID, "config.json") }
func (a *Adapter) versionPath(home string) string { return filepath.Join(home, ID, "version") }
func (a *Adapter) skillPath(home, name string) string {
	return filepath.Join(home, ID, "skills", name+".skill")
}

func (a *Adapter) readConfig(home string) (map[string]any, error) {
	raw, err := os.ReadFile(a.configPath(home))
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fixture: read config: %w", err)
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("fixture: parse config: %w", err)
	}
	return cfg, nil
}

// mergeWriteConfig 合并写：只写受管键，未托管键原样保留（§5.5）；原子写。
func (a *Adapter) mergeWriteConfig(home string, managed map[string]any) error {
	current, err := a.readConfig(home)
	if err != nil {
		return err
	}
	for k, v := range managed {
		current[k] = v
	}
	out, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return fmt.Errorf("fixture: marshal config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(a.configPath(home)), 0o755); err != nil {
		return fmt.Errorf("fixture: create config dir: %w", err)
	}
	return atomicWrite(a.configPath(home), out, 0o644)
}

func (a *Adapter) installedVersion(home string) string {
	v, err := os.ReadFile(a.versionPath(home))
	if err != nil {
		return ""
	}
	return string(v)
}

func (a *Adapter) skillDigest(home, name string) string {
	b, err := os.ReadFile(a.skillPath(home, name))
	if err != nil {
		return ""
	}
	return string(b)
}

func (a *Adapter) skillsOf(desired adapter.AgentDesiredState) map[string]string {
	var skills map[string]map[string]string
	if len(desired.Config) == 0 {
		return nil
	}
	if err := json.Unmarshal(desired.Config, &skills); err != nil {
		return nil
	}
	out := map[string]string{}
	for name, s := range skills {
		if d, ok := s["contentDigest"]; ok {
			out[name] = d
		}
	}
	return out
}

// managedProjection 从期望 config 提取受管键投影（仅受管键参与摘要，FR-8.2）。
func managedProjection(config json.RawMessage) (map[string]any, error) {
	cfg := map[string]any{}
	if len(config) != 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, fmt.Errorf("fixture: parse desired config: %w", err)
		}
	}
	return projectionOf(cfg), nil
}

func projectionOf(cfg map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range managedKeys {
		if v, ok := cfg[k]; ok {
			out[k] = v
		}
	}
	return out
}

// projectionDigest 对受管投影计算摘要（期望侧与观测侧同源同规则，ADR-1）。
func projectionDigest(projection map[string]any) string {
	canonical, err := desiredstate.CanonicalJSON(projection)
	if err != nil {
		// CanonicalJSON 对 map[string]any 不应失败；防御性兜底。
		return "sha256:error"
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("sha256:%x", sum)
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
