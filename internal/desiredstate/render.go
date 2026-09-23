// Package desiredstate 实现期望状态渲染器（架构 v1.1.2 §4.8/§6.3/§9、FR-7.x）：
// 输入五要素（AgentProfile + Skill 精确修订/digest + ModelProvider + Machine
// overrides + adapter schema 版本）→ 归一化 DesiredState → 确定性摘要。
// 渲染必须纯函数化（同输入必同输出，§32.1）；摘要对规范化 JSON 计算（RD-4），
// 仅用于内容寻址与代变更判定，不参与 drift 比较（FR-8.6 三类摘要职责边界）。
package desiredstate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// SnapshotCanonicalizationVersion 是本包规范化规则的版本（§7.1 契约 3：快照
// 落库时同时记录，供事后审计"当时是用哪版规则"）。注意：它标识**快照 JSON
// 规范化**；节点投影的 canonicalizationVersion 是另一套规则（ADR-1 节点侧）。
const SnapshotCanonicalizationVersion = "snapshot-json-v1"

// profileSpec 是 AgentProfile.spec 的归一化解释（本片契约；第 4 片适配器接入
// 后按家族扩展字段）。JSON 形态：
//
//	{
//	  "agents": {"codex": {"enabled": true, "version": "1.2.3",
//	                       "provider": "openai-main", "config": {...}}},
//	  "skills": [{"name": "superpowers", "skill": "superpowers"}],
//	  "mcp": {...}, "rules": {...}
//	}
//
// machine.spec.overrides 与其同构，逐家族深合并 config（overrides 优先）。
type profileSpec struct {
	Agents map[string]profileAgent    `json:"agents"`
	Skills []profileSkillRef          `json:"skills"`
	MCP    map[string]json.RawMessage `json:"mcp"`
	Rules  map[string]json.RawMessage `json:"rules"`
}

type profileAgent struct {
	Enabled  bool            `json:"enabled"`
	Version  string          `json:"version"`
	Provider string          `json:"provider"`
	Config   json.RawMessage `json:"config"`
}

type profileSkillRef struct {
	Name  string `json:"name"`
	Skill string `json:"skill"` // Skill 资源名
}

// providerSpec 是 ModelProvider.spec 的归一化解释（FR-3.1：不存储凭据值）。
type providerSpec struct {
	Type      string `json:"type"`
	Endpoint  string `json:"endpoint"`
	APIKeyEnv string `json:"apiKeyEnv"`
}

// skillStatus 是 Skill.status 中本包消费的解析结果（FR-6.2）。
type skillStatus struct {
	ResolvedRevision string `json:"resolvedRevision"`
	ContentDigest    string `json:"contentDigest"`
	ArtifactDigest   string `json:"artifactDigest,omitempty"`
}

// RenderInputs 是渲染的全部输入（§9 五要素的载体）。Profile/Skills/Providers
// 由调用方从仓储取出后传入——本包不依赖存储层，保持纯函数。
type RenderInputs struct {
	Machine       *domain.Machine
	Profile       *domain.AgentProfile
	Skills        map[string]*domain.Skill         // 按 Skill 资源名索引
	Providers     map[string]*domain.ModelProvider // 按 Provider 资源名索引
	SchemaVersion string                           // 适配器 schema 版本（快照透传）
}

// Render 把输入解析为归一化 DesiredState（FR-7.1）。任何浮动引用（未解析的
// Skill、未知 Provider）都视为无效输入返回错误——快照禁止 latest 式不确定性。
func Render(in RenderInputs) (domain.DesiredState, error) {
	if in.Machine == nil || in.Profile == nil {
		return domain.DesiredState{}, fmt.Errorf("%w: render requires machine and profile", domain.ErrInvalid)
	}
	if in.SchemaVersion == "" {
		return domain.DesiredState{}, fmt.Errorf("%w: adapter schema version is required", domain.ErrInvalid)
	}
	profile, err := decodeJSON[profileSpec](in.Profile.SpecJSON())
	if err != nil {
		return domain.DesiredState{}, fmt.Errorf("%w: profile spec: %v", domain.ErrInvalid, err)
	}
	overrides, err := decodeJSON[profileSpec](overridesOf(in.Machine))
	if err != nil {
		return domain.DesiredState{}, fmt.Errorf("%w: machine overrides: %v", domain.ErrInvalid, err)
	}

	state := domain.DesiredState{
		SchemaVersion: in.SchemaVersion,
		Agents:        map[string]domain.AgentDesired{},
		Skills:        map[string]domain.SkillDesired{},
	}

	// agents：enabled 的家族进入快照；provider 引用就地解析为 endpoint/apiKeyEnv。
	families := make([]string, 0, len(profile.Agents))
	for f := range profile.Agents {
		families = append(families, f)
	}
	sort.Strings(families)
	for _, family := range families {
		pa := profile.Agents[family]
		ov := overrides.Agents[family]
		if ov.Enabled {
			pa.Enabled = true
		}
		if ov.Version != "" {
			pa.Version = ov.Version
		}
		if ov.Provider != "" {
			pa.Provider = ov.Provider
		}
		if !pa.Enabled {
			continue
		}
		if pa.Version == "" || isFloating(pa.Version) {
			return domain.DesiredState{}, fmt.Errorf("%w: agent %q version %q must be pinned (FR-2.1)",
				domain.ErrInvalid, family, pa.Version)
		}
		cfg := map[string]any{}
		if len(pa.Config) != 0 {
			if err := json.Unmarshal(pa.Config, &cfg); err != nil {
				return domain.DesiredState{}, fmt.Errorf("%w: agent %q config: %v", domain.ErrInvalid, family, err)
			}
		}
		if len(ov.Config) != 0 {
			ovCfg := map[string]any{}
			if err := json.Unmarshal(ov.Config, &ovCfg); err != nil {
				return domain.DesiredState{}, fmt.Errorf("%w: agent %q overrides config: %v", domain.ErrInvalid, family, err)
			}
			mergeMaps(cfg, ovCfg)
		}
		if pa.Provider != "" {
			prov, ok := in.Providers[pa.Provider]
			if !ok {
				return domain.DesiredState{}, fmt.Errorf("%w: agent %q references unknown provider %q",
					domain.ErrInvalid, family, pa.Provider)
			}
			ps, err := decodeJSON[providerSpec](prov.SpecJSON())
			if err != nil {
				return domain.DesiredState{}, fmt.Errorf("%w: provider %q spec: %v", domain.ErrInvalid, pa.Provider, err)
			}
			// FR-3.3：只携带环境变量名，绝不读取/持久化其指向的值。
			cfg["provider"] = map[string]any{"endpoint": ps.Endpoint, "apiKeyEnv": ps.APIKeyEnv}
		}
		cfgJSON, err := json.Marshal(cfg)
		if err != nil {
			return domain.DesiredState{}, fmt.Errorf("render: marshal agent %q config: %w", family, err)
		}
		state.Agents[family] = domain.AgentDesired{Version: pa.Version, Config: cfgJSON}
	}

	// skills：逐个解析为精确修订/digest（FR-6.2/6.4；禁止浮动引用）。
	for _, ref := range profile.Skills {
		if ref.Name == "" {
			ref.Name = ref.Skill
		}
		if ref.Name == "" || ref.Skill == "" {
			return domain.DesiredState{}, fmt.Errorf("%w: skill ref requires name and skill", domain.ErrInvalid)
		}
		sk, ok := in.Skills[ref.Skill]
		if !ok {
			return domain.DesiredState{}, fmt.Errorf("%w: profile references unknown skill %q", domain.ErrInvalid, ref.Skill)
		}
		st, err := decodeJSON[skillStatus](sk.StatusJSON())
		if err != nil {
			return domain.DesiredState{}, fmt.Errorf("%w: skill %q status: %v", domain.ErrInvalid, ref.Skill, err)
		}
		if st.ResolvedRevision == "" || st.ContentDigest == "" {
			return domain.DesiredState{}, fmt.Errorf("%w: skill %q is not resolved (run resolve first)", domain.ErrInvalid, ref.Skill)
		}
		state.Skills[ref.Name] = domain.SkillDesired{
			Revision:       st.ResolvedRevision,
			ContentDigest:  st.ContentDigest,
			ArtifactDigest: st.ArtifactDigest,
		}
	}

	if len(profile.MCP) != 0 {
		state.MCP = profile.MCP
	}
	if len(profile.Rules) != 0 {
		state.Rules = profile.Rules
	}
	return state, nil
}

// SnapshotDigest 对 DesiredState 规范化 JSON 计算确定性 SHA-256（§4.8/RD-4：
// 键序字典序、无易变字段）。规范化 = Go map 序列化（键排序）+ 紧凑输出。
func SnapshotDigest(state domain.DesiredState) (string, error) {
	canonical, err := CanonicalJSON(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("sha256:%x", sum), nil
}

// BuildSnapshot 组装完整快照（含 digest 与规范化版本标注）。
func BuildSnapshot(machine string, generation int64, state domain.DesiredState) (*domain.DesiredStateSnapshot, error) {
	digest, err := SnapshotDigest(state)
	if err != nil {
		return nil, err
	}
	return &domain.DesiredStateSnapshot{
		Machine:                 machine,
		Generation:              generation, // Materialize 时由仓储最终确定
		Digest:                  digest,
		Protocol:                domain.SnapshotProtocolVersion,
		CanonicalizationVersion: SnapshotCanonicalizationVersion,
		Desired:                 state,
	}, nil
}

// CanonicalJSON 输出规范化 JSON：map 键字典序、紧凑分隔符。同内容必同字节。
func CanonicalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("desiredstate: marshal: %w", err)
	}
	// 二次穿过 map[string]any：结构体字段序、空白等非语义差异被抹平，
	// 只剩键排序后的紧凑 JSON（RD-4）。
	var normalized any
	if err := json.Unmarshal(b, &normalized); err != nil {
		return nil, fmt.Errorf("desiredstate: normalize: %w", err)
	}
	out, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("desiredstate: canonical marshal: %w", err)
	}
	return out, nil
}

func overridesOf(m *domain.Machine) json.RawMessage {
	spec, err := decodeJSON[map[string]json.RawMessage](m.SpecJSON())
	if err != nil {
		return nil
	}
	return spec["overrides"]
}

func isFloating(version string) bool {
	v := strings.ToLower(strings.TrimSpace(version))
	return v == "" || v == "latest" || v == "stable" || strings.HasPrefix(v, "latest-")
}

// mergeMaps 深合并 src 到 dst（src 优先；嵌套对象递归，标量与数组覆盖）。
func mergeMaps(dst, src map[string]any) {
	for k, v := range src {
		if srcObj, ok := v.(map[string]any); ok {
			if dstObj, ok := dst[k].(map[string]any); ok {
				mergeMaps(dstObj, srcObj)
				continue
			}
		}
		dst[k] = v
	}
}

func decodeJSON[T any](raw json.RawMessage) (T, error) {
	var out T
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
