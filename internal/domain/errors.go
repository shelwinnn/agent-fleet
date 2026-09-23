package domain

import "errors"

// 存储层映射为这几类哨兵错误，HTTP 层再映射为 reason code 与状态码。
// 本切片使用的 reason code 是 §6.4/§30.3 五要素错误体的最小子集；
// §30 四组业务错误码随对应切片引入。
var (
	ErrNotFound      = errors.New("resource not found")
	ErrAlreadyExists = errors.New("resource already exists")
	ErrInvalid       = errors.New("resource invalid")
	// ErrMachineBusy 表示目标机器已有未决操作（§4.3：互斥以持久层唯一索引表达）。
	ErrMachineBusy = errors.New("machine has an unresolved operation")
	// ErrOpState 表示操作的相位迁移不合法（CAS from 不匹配等，§6.4 状态机）。
	ErrOpState = errors.New("operation state transition invalid")
	// ErrAgentDisconnected 表示该机器当前没有活跃 Connect 流，无法派发（§4.3 路由）。
	ErrAgentDisconnected = errors.New("agent connect stream not available")
	// ErrRollbackUnsupported 表示回滚目标代不可用或安装器不支持（§7.2/FR-2.4：
	// 如实报错，绝不虚报成功）。
	ErrRollbackUnsupported = errors.New("rollback unsupported")
	// ErrReplanRequired 表示计划基线已失效，需重新 plan 与重新确认
	//（FR-12.7：确认窗口内基线变化按 ReplanRequired 处理）。
	ErrReplanRequired = errors.New("replan required")
)

// reason code（错误体首要素，§6.4）。MachineBusy 属 §30 第四组（操作协调），
// 其余为本切片 CRUD/骨架所需的通用码；业务错误码随对应切片引入。
const (
	ReasonNotFound         = "NotFound"
	ReasonAlreadyExists    = "AlreadyExists"
	ReasonInvalid          = "Invalid"
	ReasonNotImplemented   = "NotImplemented"
	ReasonInternal         = "Internal"
	ReasonMachineBusy      = "MachineBusy"
	ReasonUnauthorized     = "Unauthorized"
	ReasonNotReady         = "NotReady"
	ReasonMethodNotAllowed = "MethodNotAllowed"
)

// NotImplemented 标记骨架路由：资源类型已规划、实现归属后续切片。
type NotImplemented struct{ Resource string }

func (e *NotImplemented) Error() string {
	return "resource " + e.Resource + " is not implemented in this slice"
}
