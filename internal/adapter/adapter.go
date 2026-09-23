// Package adapter 定义适配器契约（架构 v1.1.2 §5.4/§15）与注册表。
// 核心包（控制面、reconciler）只依赖本接口，不 import 任何家族实现包
// （I-1 / 护栏 #2）；真实家族适配器随第 4 片接入，本片仅有 fixture 实现。
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// AgentDesiredState 是随操作下发给适配器的单家族期望（§5.4）。
type AgentDesiredState struct {
	Family  string          `json:"family"`
	Version string          `json:"version"`
	Config  json.RawMessage `json:"config,omitempty"`
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
//  1. Inventory 同时产出双侧投影摘要与统一 canonicalizationVersion（ADR-1）；
//  2. 投影可复现：同一 canonicalizationVersion + 同一输入必同摘要（§13.1 fixture 锁定）；
//  3. 只对归一化受管状态计算摘要，绝不对整份配置文件哈希（FR-8.2）。
type Adapter interface {
	ID() string
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
