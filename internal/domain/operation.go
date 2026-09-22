package domain

import "time"

// Operation 相位（§6.4）。Unknown 不是终态：它占用机器级互斥（FR-15.4）。
const (
	OperationPhasePending              = "Pending"
	OperationPhaseRunning              = "Running"
	OperationPhaseAwaitingConfirmation = "AwaitingConfirmation"
	OperationPhaseCancelRequested      = "CancelRequested"
	OperationPhaseSucceeded            = "Succeeded"
	OperationPhaseFailed               = "Failed"
	OperationPhaseUnknown              = "Unknown"
)

// OperationType 的合法取值（§6.1）。
const (
	OperationTypeReconcile = "Reconcile"
	OperationTypeRepair    = "Repair"
	OperationTypeBootstrap = "Bootstrap"
	OperationTypeRollback  = "Rollback"
	OperationTypeAutoPlan  = "AutoPlan"
)

// UnresolvedPhases 是占用机器级互斥的相位集合（§4.3/§4.9 的部分唯一索引谓词）。
var UnresolvedPhases = []string{
	OperationPhasePending,
	OperationPhaseRunning,
	OperationPhaseAwaitingConfirmation,
	OperationPhaseCancelRequested,
	OperationPhaseUnknown,
}

// Transport 的合法取值（§6.1）。
const (
	TransportAgentd = "agentd"
	TransportSSH    = "ssh"
)

// OperationSpec 是 Operation 的不可变规格（§6.4 子集）。
type OperationSpec struct {
	Machine           string `json:"machine"`
	Type              string `json:"type"`
	Transport         string `json:"transport,omitempty"`
	DesiredGeneration int64  `json:"desiredGeneration,omitempty"`
}

// OperationStatus 记录相位与起止时间（§6.4 子集）；steps/verify 证据由后续切片补充。
type OperationStatus struct {
	Phase      string     `json:"phase"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Operation 是系统创建的不可变审计单元（§6.4）。
type Operation struct {
	Metadata ObjectMeta      `json:"metadata"`
	Spec     OperationSpec   `json:"spec"`
	Status   OperationStatus `json:"status"`
}
