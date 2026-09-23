package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Deployment phase（§4.4：Pending → Canary → RollingOut → (Paused | Succeeded | Failed)）。
const (
	DeploymentPhasePending    = "Pending"
	DeploymentPhaseCanary     = "Canary"
	DeploymentPhaseRollingOut = "RollingOut"
	DeploymentPhasePaused     = "Paused"
	DeploymentPhaseSucceeded  = "Succeeded"
	DeploymentPhaseFailed     = "Failed"
)

// DeploymentTarget phase 与 reason（§6.1：targets 记录每机 phase/result 与
// superseded/skipped 原因；FR-10.6/10.7、§4.4）。
const (
	TargetPhasePending    = "Pending"
	TargetPhaseRunning    = "Running"
	TargetPhaseSucceeded  = "Succeeded"
	TargetPhaseFailed     = "Failed"
	TargetPhaseSuperseded = "Superseded"
	TargetPhaseSkipped    = "Skipped"
	TargetPhaseBlocked    = "Blocked"
)

// SupersededByNewerGeneration 是 Deployment 目标/终态 reason（FR-15.2 v1.1.2：
// 非错误码的展示与终态枚举，显式归类）。
const SupersededByNewerGeneration = "SupersededByNewerGeneration"

// BlockedByNodeLock 是 phase 展示修饰（§5.6 规则 3：daemon 因节点执行权忙排队）。
const BlockedByNodeLock = "BlockedByNodeLock"

// DeploymentCoreSpec 抽取 Deployment spec 中控制器消费的字段（§6.1/FR-10.1）。
type DeploymentCoreSpec struct {
	// MachineNames 是显式目标列表（spec.selector.machineNames）。
	MachineNames []string `json:"machineNames,omitempty"`
	// TargetGeneration 是目标内容代（§8.6）。回滚型 Deployment 中它的语义是
	// "内容代"——要重放的内容来自哪一代；逐机新代由控制器物化并记录（FR-10.1）。
	TargetGeneration int64 `json:"targetGeneration"`
	// RollbackOf 非空即回滚型 Deployment：指向被回滚的 generation（FR-10.1/10.4）。
	// 回滚不把机器拉回旧代，而是为每台目标机物化新代（内容 = 目标代内容，§7.2）。
	RollbackOf int64              `json:"rollbackOf,omitempty"`
	Strategy   DeploymentStrategy `json:"strategy,omitempty"`
}

// DeploymentStrategy 是发布策略（§6.1）。
type DeploymentStrategy struct {
	Canary         int  `json:"canary,omitempty"`         // 金丝雀批台数
	BatchSize      int  `json:"batchSize,omitempty"`      // 整数 batchSize
	MaxUnavailable int  `json:"maxUnavailable,omitempty"` // 同批同时变更上限
	PauseOnFailure bool `json:"pauseOnFailure,omitempty"`
}

// DeploymentTargetStatus 是单个目标机的推进状态（§10.1 deployment_targets）。
type DeploymentTargetStatus struct {
	Machine string `json:"machine"`
	Phase   string `json:"phase"`
	// Reason 记录 Superseded/Skipped/Failed 的原因码（FR-14.5：UI 必须呈现）。
	Reason string `json:"reason,omitempty"`
	// OperationID 是该目标当前/最近一次派发的操作（门禁因果绑定，§4.4）。
	OperationID string `json:"operationId,omitempty"`
	// EffectiveGeneration 是门禁与后续校验使用的有效目标代：普通发布 = 目标代；
	// 回滚 = 控制器为该机物化的新代（FR-10.6 v1.1.2 分支）。
	EffectiveGeneration int64     `json:"effectiveGeneration,omitempty"`
	UpdatedAt           time.Time `json:"updatedAt,omitempty"`
}

// DeploymentStatus 是 deployments.status（§6.1）。
type DeploymentStatus struct {
	Phase   string                   `json:"phase"`
	Reason  string                   `json:"reason,omitempty"`
	Targets []DeploymentTargetStatus `json:"targets,omitempty"`
}

// ParseDeploymentCore 解析 spec 中的核心字段。
func ParseDeploymentCore(spec json.RawMessage) (DeploymentCoreSpec, error) {
	var core DeploymentCoreSpec
	if len(spec) != 0 {
		if err := json.Unmarshal(spec, &core); err != nil {
			return core, fmt.Errorf("%w: deployment spec: %v", ErrInvalid, err)
		}
	}
	return core, nil
}

// DeploymentTargetRepository 维护 deployment_targets（§10.1）。
type DeploymentTargetRepository interface {
	// Replace 以事务覆盖一个 Deployment 的目标集合（创建/回滚重建时）。
	Replace(ctx context.Context, deployment string, targets []DeploymentTargetStatus) error
	// List 返回目标的推进状态。
	List(ctx context.Context, deployment string) ([]DeploymentTargetStatus, error)
	// Update 更新单个目标行。
	Update(ctx context.Context, deployment string, t DeploymentTargetStatus) error
}
