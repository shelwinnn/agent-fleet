package adapter

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/shelwinnn/agent-fleet/internal/desiredstate"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// ProjectionCanonicalizationVersion 是**节点侧受管投影规范化规则**的版本
// （§7.1/ADR-1）：期望侧与观测侧摘要必须用它标记，服务端只存两个数与这个版本。
//
// 【本片裁决】多家族共用一个节点侧规则版本，而不是每家族各一个：
//   - §7.1 契约 1 要求"同一操作的比较双方版本一致"，比较双方是 desired/observed
//     两侧；跨家族一致不是架构要求，但聚合判据（单机单摘要对）必须可比；
//   - agentd 的一次 inventory 只有一个聚合版本字段上行，家族级版本无法同时表达；
//   - 任一家族投影规则变化即 bump 本版本，使旧的比较结论整体失效（宁停不错），
//     服务端将置 Unknown 并提示升级（§7.1 契约 2）。
//
// 家族级的受管 schema 版本另由各适配器 DetectedAgent.SchemaVer 上报。
const ProjectionCanonicalizationVersion = "agentlocal-projection-v1"

// CombineProjectionDigests 把多家族摘要聚合为单机判据：对 {family: digest}
// 规范化 JSON 再取 SHA-256（同源同规则，不引入第二套比较语义，ADR-1）。
func CombineProjectionDigests(set map[string]string) string {
	b, err := desiredstate.CanonicalJSON(set)
	if err != nil {
		return "sha256:error"
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("sha256:%x", sum)
}

// DesiredStatesFromSnapshot 把不可变期望快照转换为各家族的下发形态
// （§5.4 AgentDesiredState）。reconciler 与 inventory 共用同一转换，
// 保证"执行时算的投影"与"周期上报算的投影"输入一致。
func DesiredStatesFromSnapshot(snap *domain.DesiredStateSnapshot) (map[string]AgentDesiredState, error) {
	if snap == nil {
		return nil, fmt.Errorf("adapter: nil snapshot")
	}
	mcp, err := mcpEntries(snap.Desired.MCP)
	if err != nil {
		return nil, err
	}
	rules := rulesEntries(snap.Desired.Rules)
	out := make(map[string]AgentDesiredState, len(snap.Desired.Agents))
	for family, da := range snap.Desired.Agents {
		out[family] = AgentDesiredState{
			Family:  family,
			Version: da.Version,
			Config:  da.Config,
			Skills:  snap.Desired.Skills,
			MCP:     mcp,
			Rules:   rules,
		}
	}
	return out, nil
}

func mcpEntries(raw map[string]json.RawMessage) (map[string]MCPEntry, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]MCPEntry, len(raw))
	for name, body := range raw {
		var e MCPEntry
		if err := json.Unmarshal(body, &e); err != nil {
			return nil, fmt.Errorf("adapter: mcp entry %q: %w", name, err)
		}
		out[name] = e
	}
	return out, nil
}

func rulesEntries(raw map[string]json.RawMessage) map[string]RulesEntry {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]RulesEntry, len(raw))
	for name, body := range raw {
		var e RulesEntry
		if err := json.Unmarshal(body, &e); err != nil {
			var s string
			if json.Unmarshal(body, &s) == nil {
				out[name] = RulesEntry{Content: s}
			}
			continue
		}
		out[name] = e
	}
	return out
}
