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
	// API 侧的 409 MachineBusy 在 reconcile 切片接入；本切片由存储层先行返回。
	ErrMachineBusy = errors.New("machine has an unresolved operation")
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
