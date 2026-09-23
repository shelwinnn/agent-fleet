package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Operation 相位（§6.4）。Unknown 不是终态：它占用机器级互斥（FR-15.4），
// 只有节点重报（outbox）、节点确认的取消、或操作者显式跳过才能使其离开。
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

// TerminalModifier 是终态修饰（§6.4：仅终态，可空）。带修饰的成功不满足
// 健康门禁的"操作成功"条件（§4.4：Succeeded(Superseded) 不满足）。
const (
	ModifierSuperseded = "Superseded" // 迟到成功：desiredGeneration 已落后于机器当前代（FR-9.10）
	ModifierCancelled  = "Cancelled"  // 节点确认停止后的取消终态（§9.7）
	ModifierSkipped    = "Skipped"    // 操作者显式跳过：立即释放互斥，不计入成功（§9.6 路径 C）
)

// UnresolvedPhases 是占用机器级互斥的相位集合（§4.3/§4.9 的部分唯一索引谓词，
// v1.1.2 起包含 AwaitingConfirmation）。
var UnresolvedPhases = []string{
	OperationPhasePending,
	OperationPhaseRunning,
	OperationPhaseAwaitingConfirmation,
	OperationPhaseCancelRequested,
	OperationPhaseUnknown,
}

// IsUnresolved 判断相位是否占用机器级互斥。
func IsUnresolved(phase string) bool {
	for _, p := range UnresolvedPhases {
		if p == phase {
			return true
		}
	}
	return false
}

// IsTerminal 判断相位是否为终态。
func IsTerminal(phase string) bool {
	return phase == OperationPhaseSucceeded || phase == OperationPhaseFailed
}

// Transport 的合法取值（§6.1）。
const (
	TransportAgentd = "agentd"
	TransportSSH    = "ssh"
)

// AdapterHealth 取值（§6.4 verify.adapterHealth）。
const (
	AdapterHealthPassed  = "passed"
	AdapterHealthFailed  = "failed"
	AdapterHealthSkipped = "skipped"
)

// OperationSpec 是 Operation 的不可变规格（§6.4）。
type OperationSpec struct {
	Machine           string `json:"machine"`
	Type              string `json:"type"`
	Transport         string `json:"transport,omitempty"`
	DesiredGeneration int64  `json:"desiredGeneration,omitempty"`
	// ReadOnly 标记只读操作（FR-9.7 AutoPlan / FR-15.5 审计充分性）。
	ReadOnly bool `json:"readOnly,omitempty"`
	// PlanDigest 标识"确认对象是具体计划"的那份计划（FR-12.7、§8.1）。
	PlanDigest string `json:"planDigest,omitempty"`
}

// VerifyEvidence 是绑定 operationId 的门禁与审计证据（§6.4/§4.4）。
// 证据必须与操作同源：服务端以 operationId（身份）+ inventorySeq（节点本地
// 单调序）判定，不用接收时间代替因果（§4.4 明确不采纳时间比较）。
type VerifyEvidence struct {
	DesiredProjectionDigest  string `json:"desiredProjectionDigest"`
	ObservedProjectionDigest string `json:"observedProjectionDigest"`
	CanonicalizationVersion  string `json:"canonicalizationVersion"`
	InventorySeq             int64  `json:"inventorySeq"`
	AdapterHealth            string `json:"adapterHealth"` // passed | failed | skipped
}

// OperationStepRecord 是一个执行步骤的审计记录（§6.4 steps；落 operation_steps 表）。
type OperationStepRecord struct {
	Seq        int64     `json:"seq"`
	Name       string    `json:"name"`
	Phase      string    `json:"phase,omitempty"`
	Output     string    `json:"output,omitempty"` // 限长、脱敏（§6.4：永不捕获秘密）
	RecordedAt time.Time `json:"recordedAt"`
}

// OperationStatus 记录相位与起止时间（§6.4）。
type OperationStatus struct {
	Phase      string     `json:"phase"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// TerminalModifier 仅终态可非空（Superseded/Cancelled/Skipped）。
	TerminalModifier string `json:"terminalModifier,omitempty"`
	// Verify 是终态随附的门禁证据（FR-15.5）。
	Verify *VerifyEvidence `json:"verify,omitempty"`
	// PlanExpiresAt 是 AwaitingConfirmation 的确认窗口截止（§9.3：默认 30min）。
	PlanExpiresAt *time.Time `json:"planExpiresAt,omitempty"`
}

// Operation 是系统创建的不可变审计单元（§6.4）。
type Operation struct {
	Metadata ObjectMeta      `json:"metadata"`
	Spec     OperationSpec   `json:"spec"`
	Status   OperationStatus `json:"status"`
}

// ParseOperationStatus 解析 status JSON 列。
func ParseOperationStatus(raw string) (OperationStatus, error) {
	var s OperationStatus
	if raw == "" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, fmt.Errorf("domain: parse operation status: %w", err)
	}
	return s, nil
}

// OperationRepository 是 Operation 的仓储（§4.3：互斥以 operations 表上的
// 部分唯一索引在持久层表达，不以内存表达）。
type OperationRepository interface {
	// Create 插入操作行；该机已有未决操作（含 AwaitingConfirmation/Unknown）时
	// 返回 ErrMachineBusy（FR-1.10），不排队。
	Create(ctx context.Context, op *Operation) error
	// Get 按 id 取操作行。
	Get(ctx context.Context, id string) (*Operation, error)
	// ListByMachine 按机器列出操作（审计视图）。
	ListByMachine(ctx context.Context, machine string) ([]*Operation, error)
	// ListUnresolved 列出全部未决操作（重启自愈与超时扫描）。
	ListUnresolved(ctx context.Context) ([]*Operation, error)
	// Transition 在单事务内做 CAS 相位迁移：当前相位等于 from（from 为空表示
	// 任意未决相位）时迁移到 to 并应用 mutate；否则返回 ErrInvalid。
	// 迁移到终态时释放机器级互斥（部分唯一索引不再命中）。
	Transition(ctx context.Context, id, from, to string, mutate func(*Operation) error) error
	// ReplaceSteps 覆盖操作的步骤审计记录（进度上报）。
	ReplaceSteps(ctx context.Context, id string, steps []OperationStepRecord) error
	// ListSteps 读取操作的步骤记录。
	ListSteps(ctx context.Context, id string) ([]OperationStepRecord, error)
}
