package domain

import "time"

// ConditionStatus 的三态取值（§6.2：True/False/Unknown，SSH-only 与无新鲜观测必须显式 Unknown）。
const (
	ConditionTrue    = "True"
	ConditionFalse   = "False"
	ConditionUnknown = "Unknown"
)

// Machine 的六个 conditions（§6.2）。每个 condition 恰有一个置位方，
// 条件间禁止互相推导；本切片仅定义契约常量，写入方在后续切片的控制器中。
const (
	ConditionSSHReachable   = "SSHReachable"   // Machine 控制器
	ConditionAgentConnected = "AgentConnected" // Machine 控制器
	ConditionInventoryReady = "InventoryReady" // Machine 控制器
	ConditionDrifted        = "Drifted"        // Reconcile 控制器
	ConditionReconciled     = "Reconciled"     // Reconcile 控制器
	ConditionDegraded       = "Degraded"       // Reconcile 控制器
)

// Condition 是状态条件的一条记录（§22）：写入时必须完整记录全部字段。
type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"` // True | False | Unknown
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}
