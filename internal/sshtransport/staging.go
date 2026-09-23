package sshtransport

import (
	"path"
	"regexp"
	"strings"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// 远端 staging 固定路径（§4.5 【契约】staging 目录与清理 A10）：
//
//	~/.local/share/agent-fleet/staging/        (0700)
//	  bundle/                                  bundle 落于固定子目录
//	  bin/                                     临时 agentd 二进制
//
// **与操作无关的固定路径**是刻意选择：换成 bundle-<op-id> 会让反复崩溃的节点
// 无限累积垃圾；固定路径把"清理"变成一个可判定的动作（见 DecideCleanup）。
const (
	// StagingRelRoot 是 staging 根相对用户 home 的路径。
	StagingRelRoot = ".local/share/agent-fleet/staging"
	// BundleSubdir / BinSubdir 是固定子目录名。
	BundleSubdir = "bundle"
	BinSubdir    = "bin"
	// 操作级记录文件（与 bundle/ 分离，避免重新上传 bundle 时误删计划基线）。
	planFilePrefix   = "plan-"
	resultFilePrefix = "result-"
	cancelFilePrefix = "cancel-"
)

// RemoteLayout 计算节点侧 staging 的绝对路径（控制面侧使用；节点侧 oneshot
// 用同一组规则自行推导，二者必须一致——本类型是该规则的唯一实现，节点侧
// 通过 agentd 的 --staging 参数接受控制面传来的同一个根）。
type RemoteLayout struct {
	HomeDir string
}

// NewRemoteLayout 校验并构造布局。homeDir 必须是绝对路径（相对路径会让远端
// 命令的语义依赖登录 shell 的 cwd，不可接受）。
func NewRemoteLayout(homeDir string) (RemoteLayout, error) {
	if homeDir == "" || !strings.HasPrefix(homeDir, "/") {
		return RemoteLayout{}, domain.Coded(domain.ReasonInvalid,
			"remote homeDir must be an absolute path, got %q", homeDir)
	}
	if strings.ContainsAny(homeDir, "\n\r\x00") {
		return RemoteLayout{}, domain.Coded(domain.ReasonInvalid, "remote homeDir contains control characters")
	}
	return RemoteLayout{HomeDir: homeDir}, nil
}

func (l RemoteLayout) DataDir() string { return path.Join(l.HomeDir, ".local/share/agent-fleet") }

// Root 是 staging 根。
func (l RemoteLayout) Root() string { return path.Join(l.HomeDir, StagingRelRoot) }

// BundleDir 是固定 bundle 目录。
func (l RemoteLayout) BundleDir() string { return path.Join(l.Root(), BundleSubdir) }

// BinDir 是临时二进制目录。
func (l RemoteLayout) BinDir() string { return path.Join(l.Root(), BinSubdir) }

// PlanFile 是 oneshot plan 记录的基线（apply 据此比对，FR-12.7）。
func (l RemoteLayout) PlanFile(opID string) string {
	return path.Join(l.Root(), planFilePrefix+opID+".json")
}

// ResultFile 是 oneshot 结果文件（与 operationId 持久化绑定，FR-12.8/幂等）。
func (l RemoteLayout) ResultFile(opID string) string {
	return path.Join(l.Root(), resultFilePrefix+opID+".json")
}

// CancelFile 是取消请求标记：控制面经 SSH 落一个空文件，节点流水线在阶段边界
// 观察它（§5.1：SSH-only 没有 CancelOperation 通道；FR-13.9 要求取消只在阶段
// 边界生效且必须由节点确认停止）。
func (l RemoteLayout) CancelFile(opID string) string {
	return path.Join(l.Root(), cancelFilePrefix+opID+".json")
}

// AgentdBinary 是临时 agentd 二进制的固定路径（按 digest 命名，§7.4：须校验 digest）。
func (l RemoteLayout) AgentdBinary(digest string) string {
	return path.Join(l.BinDir(), "agentd-"+strings.TrimPrefix(digest, "sha256:"))
}

// EnsureArgv 返回创建 staging 目录树的远端命令（0700，§4.5 安全点 1）。
// 用 `install -d -m 700` 而不是 `mkdir && chmod`：一条命令完成且权限显式。
func (l RemoteLayout) EnsureArgv() []string {
	return []string{"install", "-d", "-m", "700", l.Root(), l.BundleDir(), l.BinDir()}
}

// CleanBundleArgv 清空固定 bundle 目录的内容（保留目录本身与 0700 权限）。
func (l RemoteLayout) CleanBundleArgv() []string {
	return []string{"rm", "-rf", l.BundleDir()}
}

// HasBundleArgv 判断 bundle 目录是否仍然存在（清理后的复核）。
func (l RemoteLayout) HasBundleArgv() []string {
	return []string{"test", "-e", l.BundleDir()}
}

// CancelRequestedArgv 判断取消标记是否存在（节点侧同语义）。
func (l RemoteLayout) CancelRequestedArgv(opID string) []string {
	return []string{"test", "-f", l.CancelFile(opID)}
}

var opIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidOperationID 校验操作 id 可安全用于 staging 文件名（拒绝路径分隔与 ".."）。
func ValidOperationID(opID string) bool {
	if !opIDRe.MatchString(opID) {
		return false
	}
	return !strings.Contains(opID, "..")
}

// CleanupDecision 是"本次操作开始前能否清理 staging 陈旧内容"的判定结果（§4.5 清理规则 1–2）。
type CleanupDecision struct {
	Clean  bool
	Reason string
}

// DecideCleanup 判定是否可以清理固定 staging 路径中的陈旧内容。
//
// 规则（§4.5 A10 汇总裁决）：
//  1. 只清理可判定为"无主"的内容——即当前不存在引用该路径的未决操作；
//  2. **存在未决操作（含 Unknown）时，不得删除其输入**——bundle 与临时二进制
//     都可能是节点尚在读取的工厂材料；
//  3. 被操作者显式跳过的操作（terminalModifier=Skipped）虽然释放了控制面互斥，
//     但按定义**没有**节点已停止的确认（§9.6 路径 C），同样不得删除其输入。
func DecideCleanup(prev *domain.Operation) CleanupDecision {
	if prev == nil {
		return CleanupDecision{Clean: true, Reason: "no previous operation references staging"}
	}
	if domain.IsUnresolved(prev.Status.Phase) {
		return CleanupDecision{Reason: "unresolved operation " + prev.Metadata.Name +
			" (" + prev.Status.Phase + ") may still read its inputs"}
	}
	if prev.Status.TerminalModifier == domain.ModifierSkipped {
		return CleanupDecision{Reason: "operation " + prev.Metadata.Name +
			" was skipped without confirming the node stopped"}
	}
	return CleanupDecision{Clean: true,
		Reason: "previous operation " + prev.Metadata.Name + " is terminal with node confirmation"}
}

// PostRunCleanup 判定一次执行结束后能否立即清理其输入（§4.5 清理规则 3）：
// 成功 → 可清理；失败/取消 → 必须**先确认节点已停止**（收到节点结果，或下一轮
// 探测确认无进程持有）才清理。清理失败不是操作失败（规则 4），但须告警。
func PostRunCleanup(nodeConfirmed bool) CleanupDecision {
	if nodeConfirmed {
		return CleanupDecision{Clean: true, Reason: "node returned a result; process has exited"}
	}
	return CleanupDecision{Reason: "node stop not confirmed; keeping inputs until a probe proves no owner"}
}
