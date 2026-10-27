package upstream

import "time"

// 本文件把「哪个安装方式/来源能查上游版本、适用于哪些平台、证据到什么程度」
// 固化为代码级能力声明。事实来源（不得在此编造）：
//
//   - 上游 Manager.GetLatestVersion v1.5.0 的 switch 覆盖面：npm/pip/brew 可查；
//     native/curl/binary/bun/... 明确返回 "version checking not supported"
//     （pkg/installer/installer.go）。
//   - codex 的 catalog 条目（pkg/catalog/catalog.json @v1.5.0）：npm 包
//     @openai/codex，平台 darwin/linux/windows；brew cask codex，平台 darwin；
//     binary 仅含下载 metadata、无安装命令（T1 判 BLOCKED）。
//   - 各组合的验证程度：docs/KM-46-agentmanager-接入核查报告.md §5 能力矩阵。
//     未验证不标支持；矩阵更新时同步更新此处。

// Verification 记录一个 OS×架构×来源组合的证据程度（对齐 T1 能力矩阵判定）。
type Verification string

const (
	// VerifiedIsolated：隔离环境运行验证通过（容器/替身，非真机）。
	VerifiedIsolated Verification = "verified_isolated"
	// Unverified：编译可行或理论可行，但无运行证据——不宣称支持。
	Unverified Verification = "unverified"
	// BlockedPendingWindows：上游实现有源码级缺陷，待 Windows 真机确认后
	// 另行决策（windows/amd64 + npm 的 .exe 硬编码问题）。常量名与取值一致
	//（取值即对外 JSON 契约，沿用 T1 报告用语）。
	BlockedPendingWindows Verification = "blocked_pending_windows"
	// BlockedUpstream：上游缺陷导致该来源完全不可用（binary 空 Command）。
	BlockedUpstream Verification = "blocked_upstream"
)

// QuerySupport 表示上游 Manager 对该安装方式的版本查询支持。
type QuerySupport string

const (
	// QuerySupported：上游 GetLatestVersion 有该方式的实现路径。
	QuerySupported QuerySupport = "supported"
	// QueryUnsupported：上游明确不支持该方式的版本查询。
	QueryUnsupported QuerySupport = "unsupported"
)

// PlatformApplicability 是一个来源在某 OS×架构上的适用性与证据。
type PlatformApplicability struct {
	Platform string       `json:"platform"` // darwin / linux / windows（catalog 语义）
	Arch     string       `json:"arch"`     // amd64 / arm64
	Status   Verification `json:"status"`   // T1 能力矩阵判定
	// Evidence 是判定的依据摘要（报告章节或运行证据编号），展示给操作者。
	Evidence string `json:"evidence"`
}

// SourceDef 声明一个 agent 的一个安装来源/渠道能否查询上游版本，以及适用平台。
type SourceDef struct {
	// AgentID 是 Fleet 侧 agent 标识（当前仅 codex）。
	AgentID string
	// Method 是安装方式（npm / brew / binary），对齐 T2 操作模型的来源取值。
	Method string
	// Package 是该来源的包名（npm 形如 "@openai/codex"）。
	Package string
	// Channel 是版本渠道。上游 GetLatestVersion 只查默认 latest 渠道，
	// 没有渠道参数——其他渠道一律 unsupported。
	Channel string
	// Query 声明上游是否支持该方式的版本查询。
	Query QuerySupport
	// Platforms 是适用平台与证据（未验证如实标注）。
	Platforms []PlatformApplicability
	// DefaultChannel 标记该来源是否查询默认渠道（见 Channel 说明）。
	DefaultChannel bool
}

// QueryUnsupportedReason 给出上游不支持查询时的明确原因。
func (d SourceDef) QueryUnsupportedReason() string {
	switch d.Method {
	case "binary":
		return "upstream defect: catalog binary entry has no install command; " +
			"Manager.GetLatestVersion reports version checking not supported for binary"
	default:
		return "upstream Manager.GetLatestVersion does not support method " + d.Method
	}
}

// DefaultQueryTimeout 是单次外部查询的超时上限。上游不在命令上设 deadline，
// 必须由接入方沿 context 提供（与 lifecycle.DefaultOpTimeout 同一约束来源，
// 查询是轻量注册表往返，取远小于生命周期操作的 10 分钟）。
const DefaultQueryTimeout = 30 * time.Second

// DefaultMaxAttempts 是单次 Check 的总尝试上限（1 次初始 + 有限重试）。
const DefaultMaxAttempts = 2

// DefaultRetryBackoff 是可重试失败之间的固定间隔（有限重试，不做指数放大——
// 操作者在界面上等着结果，快速失败后由缓存回退兜底）。
const DefaultRetryBackoff = 250 * time.Millisecond

// codexPlatforms 按 T1 能力矩阵（§5）展开；矩阵里没有的 OS×架构组合
// （如 darwin/amd64 + npm）一律 Unverified——未验证不标支持。
func codexNpmPlatforms() []PlatformApplicability {
	return []PlatformApplicability{
		{Platform: "linux", Arch: "amd64", Status: VerifiedIsolated,
			Evidence: "KM-46 §5: 隔离环境运行验证 GO（有条件），真机未验证"},
		{Platform: "linux", Arch: "arm64", Status: Unverified,
			Evidence: "KM-46 §5: 交叉编译通过，运行未验证"},
		{Platform: "darwin", Arch: "arm64", Status: Unverified,
			Evidence: "KM-46 §5: 交叉编译通过，行为零验证"},
		{Platform: "darwin", Arch: "amd64", Status: Unverified,
			Evidence: "KM-46 §5 能力矩阵无此组合"},
		{Platform: "windows", Arch: "amd64", Status: BlockedPendingWindows,
			Evidence: "KM-46 §5/§7: 上游平台层只认 .exe，待真机确认后另行决策"},
	}
}

func codexBrewPlatforms() []PlatformApplicability {
	return []PlatformApplicability{
		{Platform: "darwin", Arch: "arm64", Status: Unverified,
			Evidence: "KM-46 §5: brew provider 无任何运行证据，另有固定版本缝隙待解决"},
		{Platform: "darwin", Arch: "amd64", Status: Unverified,
			Evidence: "KM-46 §5 能力矩阵无此组合"},
	}
}

// CodexSources 返回 codex 的来源声明（顺序稳定：npm、brew、binary）。
// binary 保留在清单里是为了让界面能把「该来源存在但不可用」如实展示出来，
// 而不是从清单上消失。
func CodexSources() []SourceDef {
	return []SourceDef{
		{
			AgentID: "codex", Method: "npm", Package: "@openai/codex",
			Channel: "latest", DefaultChannel: true,
			Query:     QuerySupported,
			Platforms: codexNpmPlatforms(),
		},
		{
			AgentID: "codex", Method: "brew", Package: "codex",
			Channel: "latest", DefaultChannel: true,
			Query:     QuerySupported,
			Platforms: codexBrewPlatforms(),
		},
		{
			AgentID: "codex", Method: "binary", Package: "",
			Channel: "latest", DefaultChannel: true,
			Query: QueryUnsupported,
			Platforms: []PlatformApplicability{
				{Platform: "darwin", Arch: "arm64", Status: BlockedUpstream,
					Evidence: "KM-46 §5/§7: catalog binary 条目空 Command，上游直接报错"},
				{Platform: "linux", Arch: "amd64", Status: BlockedUpstream,
					Evidence: "KM-46 §5/§7: 同上"},
			},
		},
	}
}

// SourcesFor 返回指定 agent 的来源声明；未知 agent 返回 nil。
// 当前 registry 只有 codex——生命周期纵向闭环（KM-42）的范围即 Codex。
func SourcesFor(agentID string) []SourceDef {
	if agentID == "codex" {
		return CodexSources()
	}
	return nil
}
