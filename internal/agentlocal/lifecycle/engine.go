// Package lifecycle 是 agent 生命周期引擎的窄适配层（KM-46 阶段 1 交付：
// 接口草案 + 隔离环境运行证据，供 T4 执行引擎对齐；不是已验收的生产实现）。
//
// 设计约束（来源：KM-46 全局约束与 docs/superpowers/specs/2026-10-27-codex-lifecycle-spec.md）：
//
//   - 版本必须是创建操作时固定的精确版本；不接受 range / latest 标签，
//     执行中出现新版本不改变本次目标。
//   - 生命周期操作只允许程序本体变化；调用方负责用 DirSnapshot 对用户
//     数据目录做前后对比，本层不触碰 HOME 下任何数据路径。
//   - 退出码与上游 Result 都不能单独判定成功；每次操作后必须重新发现并
//     独立核验实际版本（或卸载后的实例缺席），零值/空 inventory 一律判失败。
//   - 上游 agentmanager 固定在 v1.5.0（见仓库 go.mod 与
//     docs/KM-46-agentmanager-接入核查报告.md 的能力矩阵）。
package lifecycle

import (
	"context"
)

// Op 是生命周期操作类型（避免裸字符串魔法值；T4 按此分流持久化与审计）。
type Op string

const (
	OpInstall   Op = "install"
	OpUpgrade   Op = "upgrade"
	OpUninstall Op = "uninstall"
	// OpUpdate 仅出现在取证探针（上游 Update 的漂移证据），生产引擎不产生。
	OpUpdate Op = "update"
)

// Request 是一次安装/升级请求。版本在创建操作时固定。
type Request struct {
	// AgentID 是 Fleet 侧 agent 标识（如 "codex"），只用于结果归属与错误信息。
	AgentID string
	// Package 是安装来源的包名（npm 形如 "@openai/codex"）。
	Package string
	// Version 是固定的精确版本（x.y.z[-pre][+build]）。拒绝 range/latest/*。
	Version string
	// Executables 是程序入口名（用于定位可执行文件，如 ["codex"]）。
	// Windows 上游只查 .exe（.cmd shim 风险），路径可能为空——见能力矩阵。
	Executables []string
	// Platforms 声明该方法适用的平台 ID（upstream catalog 语义：linux/darwin/windows）。
	Platforms []string
}

// UninstallRequest 是一次卸载请求。Package 必须与安装时的来源一致；
// 来源未知时由调用方拒绝卸载（本层不做来源推断）。
type UninstallRequest struct {
	AgentID     string
	Package     string
	Executables []string
	Platforms   []string
}

// Outcome 是一次生命周期操作的结果。VerifiedVersion 是操作后重新发现并
// 独立核验的版本——不是上游返回值；卸载成功时为空字符串。
type Outcome struct {
	Op               Op
	AgentID          string
	Package          string
	RequestedVersion string // 安装/升级时请求的固定版本
	VerifiedVersion  string // 操作后实测版本；与请求不一致时操作已报错
	ExecutablePath   string // 上游定位到的程序入口（可能为空，尤其 Windows）
	UpstreamOutput   string // 上游记录的命令 stdout（证据留存）
}

// Detected 是重新发现的结果。
type Detected struct {
	Installed      bool
	Version        string // 精确版本字符串；Installed 时必非空（零值会在引擎内被判失败）
	Method         string // "npm" 等
	ExecutablePath string
}

// Engine 是 Codex 生命周期引擎的窄接口草案（T4 对齐用）。实现必须保证：
// 每个操作在返回前完成对实际状态的独立核验；不能把上游命令成功当作操作成功。
type Engine interface {
	// Detect 重新发现目标实例。未安装返回 Installed=false 且无错误；
	// 探测命令失败或版本不可解析返回错误——绝不以零值版本或空 inventory 冒充成功。
	Detect(ctx context.Context, req Request) (Detected, error)
	// InstallExact 安装固定版本。目标已存在时行为由实现定义并如实上报。
	InstallExact(ctx context.Context, req Request) (Outcome, error)
	// UpgradeToExact 升级到固定版本。实现不得委托上游 Update（它没有目标
	// 版本参数，永远漂移到 latest——证据见验证程序 C4 与核查报告 §4.1）。
	UpgradeToExact(ctx context.Context, req Request) (Outcome, error)
	// Uninstall 按已识别来源卸载目标实例，并核验实例确已消失。
	Uninstall(ctx context.Context, req UninstallRequest) (Outcome, error)
}
