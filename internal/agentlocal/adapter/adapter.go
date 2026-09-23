// Package adapter 定义适配器契约（架构 v1.1.2 §5.4/§15）与注册表。
// 核心包（控制面、reconciler）只依赖本接口，不 import 任何家族实现包
// （I-1 / 护栏 #2）；家族实现位于 internal/agentlocal/adapter/<family>。
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// AgentDesiredState 是随操作下发给适配器的单家族期望（§5.4）：
// version + 归一化 config（render.go 输出），外加快照中跨家族的归一化输入
// （skills/mcp/rules）——它们由各适配器翻译为自己的原生表示（§5.4 自包含声明）。
type AgentDesiredState struct {
	Family  string          `json:"family"`
	Version string          `json:"version"`
	Config  json.RawMessage `json:"config,omitempty"`
	// Skills 按名称键：精确 revision/内容 digest（FR-6.2；禁止浮动引用）。
	Skills map[string]domain.SkillDesired `json:"skills,omitempty"`
	// MCP 是归一化 MCP 条目（FR-4.1：envRefs 只含环境变量名）。
	MCP map[string]MCPEntry `json:"mcp,omitempty"`
	// Rules 是归一化 rules 条目（FR-5.1）。
	Rules map[string]RulesEntry `json:"rules,omitempty"`
}

// MCPEntry 是归一化 MCP 条目（spec §19）。
type MCPEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	EnvRefs map[string]string `json:"envRefs,omitempty"`
}

// RulesEntry 是归一化 rules 条目（spec §8.3）。
type RulesEntry struct {
	Content string `json:"content"`
}

// AgentObservedState 是适配器对单家族的观测结果。ADR-1（节点双侧投影）：
// Inventory 必须同时产出期望侧与观测侧受管投影摘要，二者使用同一个
// canonicalizationVersion；服务端只存储与比较两个数（§7.1）。
type AgentObservedState struct {
	Family string `json:"family"`
	// Version 是节点上探测到的已装版本。
	Version string `json:"version,omitempty"`
	// ManagedProjection 是观测侧受管字段的归一化投影（仅受管键，展示用）。
	ManagedProjection map[string]any `json:"managedProjection,omitempty"`
	// DesiredProjectionDigest / ObservedProjectionDigest 是 drift 判据两侧
	// （FR-8.6 唯一权威判据）。
	DesiredProjectionDigest  string `json:"desiredProjectionDigest"`
	ObservedProjectionDigest string `json:"observedProjectionDigest"`
	CanonicalizationVersion  string `json:"canonicalizationVersion"`
	// ConfigPath 是本家族观测到的配置路径（UI 展示；不含内容）。
	ConfigPath string `json:"configPath,omitempty"`
}

// Change 是计划中的一个待执行变更（§5.4 Plan 输出）。
type Change struct {
	// Family 是变更归属的 Agent 家族（多家族快照按此分组执行）。
	Family string `json:"family"`
	// Step 归属的流水线步骤（version|config|skills|mcp|rules，§5.3 阶段 5–9）。
	Step string `json:"step"`
	// Key 标识变更对象（如 "config.model"、"version"、"skill:<name>"）。
	Key string `json:"key"`
	// From/To 是受管值的当前值与目标值（仅受管字段；展示与 planDigest 输入）。
	From any `json:"from,omitempty"`
	To   any `json:"to,omitempty"`
}

// Adapter 是家族适配器契约（§5.4 的 Go 接口）。实现义务：
//  1. Capabilities 如实声明已验证能力（未验证即未验证，矩阵）；
//  2. Validate 在**任何写入之前**拒绝未支持/未验证能力（禁止静默跳过）；
//  3. Inventory 同时产出双侧投影摘要与统一 canonicalizationVersion（ADR-1）；
//  4. 投影可复现：同一 canonicalizationVersion + 同一输入必同摘要（§13.1 fixture 锁定）；
//  5. 只对归一化受管状态计算摘要，绝不对整份配置文件哈希（FR-8.2）。
type Adapter interface {
	ID() string
	// Capabilities 返回本家族逐能力声明（含证据与版本范围）。
	Capabilities() []CapabilityDecl
	// Validate 是流水线阶段 1 的适配器侧前置校验：请求了 unsupported/unverified
	// 能力即返回错误（阶段 1 在任何变更阶段之前，§5.3）。
	Validate(ctx context.Context, home string, desired AgentDesiredState) error
	Detect(ctx context.Context, home string) (DetectedAgent, error)
	Inventory(ctx context.Context, home string, desired AgentDesiredState) (AgentObservedState, error)
	Plan(ctx context.Context, home string, desired AgentDesiredState, observed AgentObservedState) ([]Change, error)
	Apply(ctx context.Context, home string, desired AgentDesiredState, changes []Change) error
	HealthCheck(ctx context.Context, home string, desired AgentDesiredState) error
}

// DetectedAgent 是节点探测结果（§5.4）。
type DetectedAgent struct {
	Family     string `json:"family"`
	Installed  bool   `json:"installed"`
	Version    string `json:"version,omitempty"`
	ConfigPath string `json:"configPath,omitempty"`
	SchemaVer  string `json:"schemaVersion,omitempty"`
}

// Registry 是按家族键的适配器注册表。
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry() *Registry { return &Registry{adapters: map[string]Adapter{}} }

func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.ID()] = a
}

func (r *Registry) Get(family string) (Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[family]
	if !ok {
		return nil, fmt.Errorf("adapter %q not registered", family)
	}
	return a, nil
}

func (r *Registry) Families() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.adapters))
	for f := range r.adapters {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// HasModelProvider 判断期望是否请求了模型/provider 配置（FR-3.2：
// render.go 把解析后的 provider 放进 config.provider；model 在 config.model）。
func HasModelProvider(desired AgentDesiredState) bool {
	cfg := ConfigMap(desired)
	if _, ok := cfg["model"]; ok {
		return true
	}
	_, ok := cfg["provider"]
	return ok
}

// ConfigMap 解析归一化 config；不可解析时返回空 map（Validate 负责报错）。
func ConfigMap(desired AgentDesiredState) map[string]any {
	out := map[string]any{}
	if len(desired.Config) == 0 {
		return out
	}
	if err := json.Unmarshal(desired.Config, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// ProviderConfig 解析归一化的 provider 段（endpoint + apiKeyEnv 环境变量名）。
// 第二个返回值表示是否请求了 provider 配置。绝不读取环境变量值（FR-3.3）。
func ProviderConfig(cfg map[string]any) (endpoint, apiKeyEnv string, ok bool) {
	raw, found := cfg["provider"]
	if !found {
		return "", "", false
	}
	obj, isObj := raw.(map[string]any)
	if !isObj {
		return "", "", true
	}
	endpoint, _ = obj["endpoint"].(string)
	apiKeyEnv, _ = obj["apiKeyEnv"].(string)
	return endpoint, apiKeyEnv, true
}
