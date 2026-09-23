package adapter

import (
	"fmt"
	"sort"
	"strings"
)

// Capability 是矩阵（docs/agent-support-matrix.md）要求逐家族声明的能力项。
// 声明随适配器版本登记，通过能力协商暴露给控制面；核心只读取声明，
// 不硬编码各家族路径（矩阵「统一适配契约」）。
type Capability string

const (
	// CapabilityVersion 是版本安装与验证（FR-2.x）。
	CapabilityVersion Capability = "version"
	// CapabilityModelProvider 是模型/provider 配置（FR-3.x）。
	CapabilityModelProvider Capability = "modelProvider"
	// CapabilityMCP 是 MCP 条目（FR-4.x）。
	CapabilityMCP Capability = "mcp"
	// CapabilitySkills 是 Skill 目的地软链/复制（FR-6.x）。
	CapabilitySkills Capability = "skills"
	// CapabilityRules 是指令文件受管标记块（FR-5.x）。
	CapabilityRules Capability = "rules"
)

// SupportState 是能力支持状态。**未知能力一律是未验证，不是支持**
// （矩阵：不得为凑齐矩阵而虚构原生能力）。
type SupportState string

const (
	// SupportSupported 表示已按本机探测或官方文档确认，并有 fixture 证据。
	SupportSupported SupportState = "supported"
	// SupportUnsupported 表示该家族原生不存在该能力，或明确不纳入受管范围。
	SupportUnsupported SupportState = "unsupported"
	// SupportUnverified 表示尚未取得证据：既不能当支持，也不能静默跳过。
	SupportUnverified SupportState = "unverified"
)

// CapabilityDecl 是单条能力声明。
type CapabilityDecl struct {
	Capability Capability   `json:"capability"`
	State      SupportState `json:"state"`
	// VerifiedVersions 是结论成立的运行时版本范围（未验证时为空）。
	VerifiedVersions string `json:"verifiedVersions,omitempty"`
	// Reason 解释 unsupported/unverified 的具体原因（进入拒绝消息）。
	Reason string `json:"reason,omitempty"`
	// Evidence 是证据出处：本机探测命令 + 输出，或官方文档 URL。
	Evidence string `json:"evidence,omitempty"`
	// Paths 是该能力实际读写的原生路径（相对 HOME 根，护栏 #12）。
	Paths []string `json:"paths,omitempty"`
}

// Declarer 是能力声明接口：适配器必须回答"本家族在哪个版本上支持什么"。
type Declarer interface {
	Capabilities() []CapabilityDecl
}

// ValidateError 是"profile 请求了未支持/未验证能力"的显式拒绝（矩阵：
// 禁止静默跳过或写入后才报成功）。消息必须点名家族、能力与原因。
type ValidateError struct {
	Family     string
	Capability Capability
	State      SupportState
	Reason     string
}

func (e *ValidateError) Error() string {
	state := string(e.State)
	reason := e.Reason
	if reason == "" {
		reason = string(e.State)
	}
	return fmt.Sprintf("agent %q capability %q is %s: %s", e.Family, e.Capability, state, reason)
}

// CapabilityError 在声明的状态不允许使用时返回 *ValidateError；支持则返回 nil。
func CapabilityError(family string, decls []CapabilityDecl, cap Capability) error {
	for _, d := range decls {
		if d.Capability != cap {
			continue
		}
		if d.State == SupportSupported {
			return nil
		}
		return &ValidateError{Family: family, Capability: cap, State: d.State, Reason: d.Reason}
	}
	// 未声明 = 未验证（矩阵：未知能力不得当作支持）。
	return &ValidateError{Family: family, Capability: cap, State: SupportUnverified,
		Reason: "adapter declares no capability entry for it"}
}

// SortDecls 让声明顺序稳定（能力协商输出与 fixture 断言的可复现性）。
func SortDecls(decls []CapabilityDecl) []CapabilityDecl {
	out := append([]CapabilityDecl(nil), decls...)
	sort.Slice(out, func(i, j int) bool { return out[i].Capability < out[j].Capability })
	return out
}

// RequestedCapabilities 从归一化期望中推导"本次请求用到哪些能力"。
// 归一化契约（render.go 输出 + spec §19/§8.3）：
//   - config.model / config.provider → modelProvider；
//   - skills 非空 → skills；mcp 非空 → mcp；rules 非空 → rules；
//   - 任何期望都带精确版本 → version。
func RequestedCapabilities(desired AgentDesiredState) []Capability {
	req := []Capability{CapabilityVersion}
	if HasModelProvider(desired) {
		req = append(req, CapabilityModelProvider)
	}
	if len(desired.MCP) != 0 {
		req = append(req, CapabilityMCP)
	}
	if len(desired.Skills) != 0 {
		req = append(req, CapabilitySkills)
	}
	if len(desired.Rules) != 0 {
		req = append(req, CapabilityRules)
	}
	sort.Slice(req, func(i, j int) bool { return req[i] < req[j] })
	return req
}

// CheckCapabilities 逐个校验请求能力；返回全部拒绝原因（一次性报全，
// 便于操作者一轮修正 profile）。
func CheckCapabilities(family string, decls []CapabilityDecl, desired AgentDesiredState) error {
	var msgs []string
	for _, c := range RequestedCapabilities(desired) {
		if err := CapabilityError(family, decls, c); err != nil {
			msgs = append(msgs, err.Error())
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}
