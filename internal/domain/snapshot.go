package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SnapshotProtocolVersion 是快照结构的协议版本（§6.3：快照含 protocolVersion）。
const SnapshotProtocolVersion = "1"

// DesiredState 是归一化的期望状态（§6.3 DesiredStateSnapshot 的内容部分）：
// 纯数据，不含秘密值——provider 只携带 endpoint 与 apiKeyEnv 环境变量名。
// 渲染与摘要见 internal/desiredstate（FR-7.1/7.2，RD-4）。
type DesiredState struct {
	// SchemaVersion 是适配器 schema 版本（渲染输入五要素之一，§9）。
	SchemaVersion string `json:"schemaVersion"`
	// Agents 按家族键：version + 归一化 config（含已解析 provider 引用）。
	Agents map[string]AgentDesired `json:"agents,omitempty"`
	// Skills 按名称键：解析后的精确修订与内容 digest（FR-6.2；禁止浮动引用）。
	Skills map[string]SkillDesired `json:"skills,omitempty"`
	// MCP 归一化条目（FR-4.1：envRefs 只含环境变量名映射）。
	MCP map[string]json.RawMessage `json:"mcp,omitempty"`
	// Rules 全局指令内容（FR-5.1）。
	Rules map[string]json.RawMessage `json:"rules,omitempty"`
}

// AgentDesired 是单个 Agent 家族的期望形态（§6.3 agents.<family>）。
type AgentDesired struct {
	Version string          `json:"version"`
	Config  json.RawMessage `json:"config,omitempty"`
}

// SkillDesired 是一个 Skill 的不可变工件引用（§6.3 skills.<name>）。
type SkillDesired struct {
	Revision       string `json:"revision"`
	ContentDigest  string `json:"contentDigest"`
	ArtifactDigest string `json:"artifactDigest,omitempty"`
}

// DesiredStateSnapshot 是一台机器某一代的不可变期望快照（§6.3）。
// generation 是身份（"第几代期望"），digest 是内容指纹（FR-7.5/ADR-2）：
// 同一内容可出现在多个 generation（A→B→A），按 (machine, generation) 唯一。
type DesiredStateSnapshot struct {
	Machine                 string    `json:"machine"`
	Generation              int64     `json:"generation"`
	Digest                  string    `json:"digest"` // "sha256:<hex>"，对 DesiredState 规范化 JSON 计算
	Protocol                string    `json:"protocolVersion"`
	CanonicalizationVersion string    `json:"canonicalizationVersion"`
	CreatedAt               time.Time `json:"createdAt,omitempty"`

	// Desired 是快照内容（§6.3 的 agents/skills/mcp/rules + schemaVersion）。
	Desired DesiredState `json:"desired"`
}

// ParseSnapshot 反序列化快照 JSON（desired_snapshots.snapshot 列）。
func ParseSnapshot(raw []byte) (*DesiredStateSnapshot, error) {
	s := &DesiredStateSnapshot{}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, fmt.Errorf("%w: snapshot: %v", ErrInvalid, err)
	}
	return s, nil
}

// JSON 序列化快照（落库值，自包含：ExecuteOperation 携带它即可执行，FR-13.6）。
func (s *DesiredStateSnapshot) JSON() ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("domain: marshal snapshot: %w", err)
	}
	return b, nil
}

// SnapshotRepository 是期望快照的仓储（§6.3/ADR-2 单表按代唯一）。
type SnapshotRepository interface {
	// Materialize 落库一代新期望（§6.3 契约）：同一事务内比较——digest 与紧邻
	// 上一代相同则不新增代（FR-7.2），返回现行走与 changed=false；digest 变化
	// 则以 generation=last+1 插入（A→B→A 时允许与更早代同 digest）。并发物化
	// 由 IMMEDIATE 事务串行化。
	Materialize(ctx context.Context, snap *DesiredStateSnapshot) (stored *DesiredStateSnapshot, changed bool, err error)
	// Current 返回机器当前（最大 generation）快照；无任何代时返回 ErrNotFound。
	Current(ctx context.Context, machine string) (*DesiredStateSnapshot, error)
	// Get 按 (machine, generation) 精确解析（回滚引用，FR-7.5：不得因内容去重失联）。
	Get(ctx context.Context, machine string, generation int64) (*DesiredStateSnapshot, error)
}
