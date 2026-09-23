package domain

import (
	"errors"
	"fmt"
)

// SSH 传输错误六类（FR-12.5/§30.1）：分类不得坍缩为 unreachable——DNS 解析失败、
// host-key 校验失败、认证失败、连接超时、远端命令失败、平台不支持必须各自可辨。
const (
	ReasonDNSResolveFailed          = "DNSResolveFailed"
	ReasonHostKeyVerificationFailed = "HostKeyVerificationFailed"
	ReasonAuthenticationFailed      = "AuthenticationFailed"
	ReasonConnectionTimeout         = "ConnectionTimeout"
	ReasonRemoteCommandFailed       = "RemoteCommandFailed"
	ReasonUnsupportedPlatform       = "UnsupportedPlatform"
)

// SSH-only 路径（bundle/Skill 工件/操作协调）使用的 §30 错误码。
// 说明：§6.4 明确错误码共四组；这里的码分别属于 Reconcile 组
// （InstallerFailed / SkillDownloadFailed / SkillDigestMismatch / SkillPathRejected /
// ArtifactTooLarge）与操作协调组（BundleTooLarge）。
const (
	ReasonInstallerFailed     = "InstallerFailed"
	ReasonSkillDownloadFailed = "SkillDownloadFailed"
	ReasonSkillDigestMismatch = "SkillDigestMismatch"
	ReasonSkillPathRejected   = "SkillPathRejected"
	ReasonArtifactTooLarge    = "ArtifactTooLarge"
	ReasonBundleTooLarge      = "BundleTooLarge"
)

// CodedError 是携带 §30 reason code 的错误（SSH 受限执行器、bundle 校验与节点
// oneshot 共用）。它让"人读消息"与"机器可判的 reason code"同行，避免上层
// 靠字符串匹配还原错误类别（§6.4 用户可见错误五要素）。
type CodedError struct {
	Reason  string
	Message string
}

func (e *CodedError) Error() string { return e.Reason + ": " + e.Message }

// Coded 构造一个带 reason code 的错误。
func Coded(reason, format string, args ...any) error {
	return &CodedError{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// ReasonOf 取出错误的 reason code；未携带码时回退 Internal（绝不猜类别）。
func ReasonOf(err error) string {
	if err == nil {
		return ""
	}
	var ce *CodedError
	if errors.As(err, &ce) && ce.Reason != "" {
		return ce.Reason
	}
	return ReasonInternal
}
