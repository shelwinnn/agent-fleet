package domain

import (
	"encoding/json"
	"time"
)

// ObjectMeta 是全部资源的公共元数据（K8s 风格 metadata/spec/status，架构 v1.1.2 §6.1）。
// uid、resourceVersion、creationTimestamp 由服务端管理：创建时赋值，请求携带的值被忽略。
type ObjectMeta struct {
	Name              string    `json:"name"`
	UID               string    `json:"uid,omitempty"`
	ResourceVersion   int64     `json:"resourceVersion,omitempty"`
	CreationTimestamp time.Time `json:"creationTimestamp,omitempty"`
}

// ResourceObject 是资源类型须满足的存取契约，供泛型仓储与处理器使用。
// 全部以指针接收者实现（T 的指针类型作为类型参数）。
type ResourceObject interface {
	Meta() *ObjectMeta
	SpecJSON() json.RawMessage
	SetSpecJSON(json.RawMessage)
	StatusJSON() json.RawMessage
	SetStatusJSON(json.RawMessage)
}
