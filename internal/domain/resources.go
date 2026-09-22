package domain

import "encoding/json"

// ManagementMode 是 Machine spec.managementMode 的合法取值（§6.1）。
const (
	ManagementModeAgentd = "agentd"
	ManagementModeSSH    = "ssh"
)

// MachineCoreSpec 抽取 Machine spec 中控制面需要查询的字段（§10.1：查询列）。
// spec 以原始 JSON 透传存储，未知字段保留给后续切片（控制器、期望状态）。
type MachineCoreSpec struct {
	ManagementMode string `json:"managementMode,omitempty"` // agentd|ssh，缺省按 agentd
	ProfileRef     string `json:"profileRef,omitempty"`
}

// Machine 是受管工作站（§6.2）。
type Machine struct {
	Metadata ObjectMeta      `json:"metadata"`
	Spec     json.RawMessage `json:"spec"`
	Status   json.RawMessage `json:"status"` // 控制器维护（conditions 等），API 创建时初始化为 {}
}

func (m *Machine) Meta() *ObjectMeta               { return &m.Metadata }
func (m *Machine) SpecJSON() json.RawMessage       { return m.Spec }
func (m *Machine) SetSpecJSON(r json.RawMessage)   { m.Spec = r }
func (m *Machine) StatusJSON() json.RawMessage     { return m.Status }
func (m *Machine) SetStatusJSON(r json.RawMessage) { m.Status = r }

// AgentProfile 定义一台（组）机器的期望 Agent 形态（§6.1）。
type AgentProfile struct {
	Metadata ObjectMeta      `json:"metadata"`
	Spec     json.RawMessage `json:"spec"`
	Status   json.RawMessage `json:"status"` // 无控制器，恒为 {}
}

func (p *AgentProfile) Meta() *ObjectMeta               { return &p.Metadata }
func (p *AgentProfile) SpecJSON() json.RawMessage       { return p.Spec }
func (p *AgentProfile) SetSpecJSON(r json.RawMessage)   { p.Spec = r }
func (p *AgentProfile) StatusJSON() json.RawMessage     { return p.Status }
func (p *AgentProfile) SetStatusJSON(r json.RawMessage) { p.Status = r }

// ModelProvider 描述 openai-compatible 端点引用（§6.1）；只存 apiKeyEnv 环境变量名，不含秘密值。
type ModelProvider struct {
	Metadata ObjectMeta      `json:"metadata"`
	Spec     json.RawMessage `json:"spec"`
	Status   json.RawMessage `json:"status"`
}

func (p *ModelProvider) Meta() *ObjectMeta               { return &p.Metadata }
func (p *ModelProvider) SpecJSON() json.RawMessage       { return p.Spec }
func (p *ModelProvider) SetSpecJSON(r json.RawMessage)   { p.Spec = r }
func (p *ModelProvider) StatusJSON() json.RawMessage     { return p.Status }
func (p *ModelProvider) SetStatusJSON(r json.RawMessage) { p.Status = r }

// Skill 引用一个可解析的技能源（§6.1）。
type Skill struct {
	Metadata ObjectMeta      `json:"metadata"`
	Spec     json.RawMessage `json:"spec"`
	Status   json.RawMessage `json:"status"` // resolve 控制器写 resolvedRevision/contentDigest
}

func (s *Skill) Meta() *ObjectMeta               { return &s.Metadata }
func (s *Skill) SpecJSON() json.RawMessage       { return s.Spec }
func (s *Skill) SetSpecJSON(r json.RawMessage)   { s.Spec = r }
func (s *Skill) StatusJSON() json.RawMessage     { return s.Status }
func (s *Skill) SetStatusJSON(r json.RawMessage) { s.Status = r }

// Deployment 是一次机群发布编排（§6.1）；控制器与派发逻辑属于后续切片。
type Deployment struct {
	Metadata ObjectMeta      `json:"metadata"`
	Spec     json.RawMessage `json:"spec"`
	Status   json.RawMessage `json:"status"`
}

func (d *Deployment) Meta() *ObjectMeta               { return &d.Metadata }
func (d *Deployment) SpecJSON() json.RawMessage       { return d.Spec }
func (d *Deployment) SetSpecJSON(r json.RawMessage)   { d.Spec = r }
func (d *Deployment) StatusJSON() json.RawMessage     { return d.Status }
func (d *Deployment) SetStatusJSON(r json.RawMessage) { d.Status = r }
