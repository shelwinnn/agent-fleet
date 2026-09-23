// SSH-only 通道的状态机接线（架构 v1.1.2 §9.3、FR-12.7）：plan 产出 → 操作者确认
// （携 confirmPlanDigest）→ apply 重取观测、重算 plan、比对基线，不一致零变更
// ReplanRequired。状态机（Operation 相位、机器级互斥、超时、取消）仍由本控制器
// 独占写入；SSH 机制（bundle/上传/oneshot）藏在 SSHPlanner 接口之后。
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// SSHPlanner 是 SSH-only 通道的编排接口（实现：internal/controller/sshops）。
// 它不写 Operation/状态——只做"经 SSH 驱动节点上的同一套 reconciler"，
// 全部状态迁移留在本控制器内，保证"每个 condition/相位恰有一个置位方"。
type SSHPlanner interface {
	// Plan 构建并上传 bundle、执行 `oneshot plan`，返回 planDigest 与观测。
	Plan(ctx context.Context, machine, opID string, generation int64, snapshotJSON []byte) (*SSHPlanOutcome, error)
	// Apply 执行已确认的计划；节点侧重取观测、重算 plan、比对基线（FR-12.7）。
	Apply(ctx context.Context, machine, opID, planDigest string, generation int64, snapshotJSON []byte) (*SSHApplyOutcome, error)
	// Cancel 经 SSH 投递取消请求（标记文件；节点在阶段边界观察，FR-13.9）。
	Cancel(ctx context.Context, machine, opID string) error
}

// SSHPlanOutcome 是 plan 阶段的产物。
type SSHPlanOutcome struct {
	PlanDigest     string
	BaselineDigest string
	BaselineSeq    int64
	ObservedState  []byte
	Generation     int64
}

// SSHApplyOutcome 是 apply 阶段的产物（与节点 OperationResult 同形）。
type SSHApplyOutcome struct {
	Phase            string
	Reason           string
	Message          string
	TerminalModifier string
	// PlanDigest 是节点实际执行的计划摘要（与确认值一致，否则节点会拒绝执行）。
	PlanDigest    string
	Verify        *domain.VerifyEvidence
	ObservedState []byte
	StartedAt     time.Time
	FinishedAt    time.Time
	// NodeConfirmed 表示节点已交回结果（进程退出）；这是清理其 staging 输入
	// 的唯一依据（§4.5 清理规则 3）。
	NodeConfirmed bool
}

// SSHFailure 是 SSH 路径的失败语义：Unknown=true 表示远端命令已启动但结果不可知
// （超时/连接中断），此时**不得**判 Failed——节点可能仍在写，必须置 Unknown、
// 保留机器级互斥并保留其输入（§6.4 契约/FR-15.4/§4.5 规则 2）。
type SSHFailure struct {
	Reason  string
	Message string
	Unknown bool
}

func (e *SSHFailure) Error() string { return e.Reason + ": " + e.Message }

// NewSSHFailure 构造 SSH 路径失败（reason 取 §30 四组错误码之一）。
func NewSSHFailure(reason, format string, args ...any) *SSHFailure {
	return &SSHFailure{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// SetSSHPlanner 装配 SSH 通道（main 装配；为空时 ssh 机器的操作会如实失败，
// 不会静默走别的通道）。
func (c *Controller) SetSSHPlanner(p SSHPlanner) { c.ssh = p }

// SSHPlannerWired 报告 SSH 通道是否已装配（HTTP 层据此判定端点可用性）。
func (c *Controller) SSHPlannerWired() bool { return c.ssh != nil }

// planOverSSH 是 SSH 路径的"产出计划 → 等待确认"入口（§9.3）。
func (c *Controller) planOverSSH(ctx context.Context, machine string, op *domain.Operation,
	snap *domain.DesiredStateSnapshot) (*domain.Operation, error) {
	if c.ssh == nil {
		return c.failOperation(ctx, op, domain.ReasonAgentDisconnected,
			"ssh transport is not wired on this control plane")
	}
	snapJSON, err := snap.JSON()
	if err != nil {
		return nil, err
	}
	out, err := c.ssh.Plan(ctx, machine, op.Metadata.Name, snap.Generation, snapJSON)
	if err != nil {
		return c.sshFailure(ctx, machine, op, err)
	}
	expires := c.now().Add(c.cfg.PlanTimeout)
	err = c.ops.Transition(ctx, op.Metadata.Name, domain.OperationPhasePending,
		domain.OperationPhaseAwaitingConfirmation, func(o *domain.Operation) error {
			o.Spec.PlanDigest = out.PlanDigest
			o.Status.PlanExpiresAt = &expires
			return nil
		})
	if err != nil {
		return nil, err
	}
	c.setUnresolvedRefPhase(ctx, machine, op.Metadata.Name, domain.OperationPhaseAwaitingConfirmation)
	c.log.Info("ssh plan awaiting confirmation", "event", "plan_awaiting_confirmation",
		"operation_id", op.Metadata.Name, "machine_id", machine,
		"plan_digest", shortDigest(out.PlanDigest), "expires_at", expires.Format(time.RFC3339))
	return c.ops.Get(ctx, op.Metadata.Name)
}

// applyOverSSH 是确认之后的 apply 入口：节点侧重算 plan 并比对基线。
func (c *Controller) applyOverSSH(ctx context.Context, machine string, op *domain.Operation,
	snap *domain.DesiredStateSnapshot) (*domain.Operation, error) {
	if c.ssh == nil {
		return c.failOperation(ctx, op, domain.ReasonAgentDisconnected,
			"ssh transport is not wired on this control plane")
	}
	snapJSON, err := snap.JSON()
	if err != nil {
		return nil, err
	}
	res, err := c.ssh.Apply(ctx, machine, op.Metadata.Name, op.Spec.PlanDigest,
		op.Spec.DesiredGeneration, snapJSON)
	if err != nil {
		return c.sshFailure(ctx, machine, op, err)
	}
	// 节点终态结论必须留痕（§6.4 步骤审计 + 用户可见错误五要素）：SSH 路径没有
	// 逐步 OperationProgress 流，reason code 若不落 steps，操作者只能从日志里找。
	stepPhase := res.Phase
	if stepPhase == "" {
		stepPhase = domain.OperationPhaseFailed
	}
	_ = c.OnProgress(ctx, machine, op.Metadata.Name, "result", stepPhase,
		fmt.Sprintf("reason_code=%s message=%s", res.Reason, res.Message))
	if _, err := c.OnResult(ctx, machine, OperationResultMsg{
		OperationID:      op.Metadata.Name,
		Phase:            res.Phase,
		Reason:           res.Reason,
		Message:          res.Message,
		TerminalModifier: res.TerminalModifier,
		Verify:           res.Verify,
		StartedAt:        res.StartedAt,
		FinishedAt:       res.FinishedAt,
	}); err != nil {
		return nil, err
	}
	return c.ops.Get(ctx, op.Metadata.Name)
}

// sshFailure 落实 SSH 失败的三分法（§6.4/FR-15.4）：
//   - 结果不可知（远端命令已启动）→ Unknown，保留互斥与输入；
//   - 明确失败（连接阶段失败 / 节点交回 Failed）→ Failed + 具体 reason；
//   - 其它异常 → Failed + Internal（不虚报具体原因）。
func (c *Controller) sshFailure(ctx context.Context, machine string, op *domain.Operation, err error) (*domain.Operation, error) {
	var f *SSHFailure
	if errors.As(err, &f) {
		if f.Unknown {
			if terr := c.ops.Transition(ctx, op.Metadata.Name, "", domain.OperationPhaseUnknown, nil); terr != nil &&
				!errors.Is(terr, domain.ErrOpState) {
				return nil, terr
			}
			c.setUnresolvedRefPhase(ctx, machine, op.Metadata.Name, domain.OperationPhaseUnknown)
			c.log.Warn("ssh operation result unknown; inputs retained",
				"event", "operation_unknown", "operation_id", op.Metadata.Name,
				"machine_id", machine, "reason_code", f.Reason, "message", f.Message)
			return c.ops.Get(ctx, op.Metadata.Name)
		}
		return c.failOperation(ctx, op, f.Reason, f.Message)
	}
	return c.failOperation(ctx, op, domain.ReasonOf(err), err.Error())
}

// RequestInventory 是 POST /machines/{id}/inventory 的实现（§8.1、§7.8）：
// 记录一条 readOnly=true 的 AutoPlan 类操作并采集一次观测。SSH-only 机器的
// 观测只能经 SSH 取得（§9.3），因此这条路径是 SSH-only 新鲜度三态的数据来源
// （§6.2：NeverInventoried / StaleObservation / ObservationPredatesDesired）。
//
// 说明：本片的手动 inventory 只做"validate + inventory"（不含 plan）——完整的
// 只读 plan 在 reconcile 路径上由 `oneshot plan` 产出并进入确认窗口；两者都
// 是 readOnly=true 的操作，不产生任何变更。
func (c *Controller) RequestInventory(ctx context.Context, machine string) (*domain.Operation, error) {
	if _, err := c.machines.Get(ctx, machine); err != nil {
		return nil, err
	}
	snap, _, err := c.EnsureSnapshot(ctx, machine)
	if err != nil {
		return nil, err
	}
	mode, _, err := c.channelState(ctx, machine)
	if err != nil {
		return nil, err
	}
	if mode != domain.TransportSSH {
		return nil, fmt.Errorf("%w: manual inventory over the agentd channel is not wired in this slice",
			domain.ErrInvalid)
	}
	op := &domain.Operation{
		Spec: domain.OperationSpec{
			Machine: machine, Type: domain.OperationTypeAutoPlan, Transport: mode,
			DesiredGeneration: snap.Generation, ReadOnly: true,
		},
		Status: domain.OperationStatus{Phase: domain.OperationPhasePending},
	}
	if err := c.ops.Create(ctx, op); err != nil {
		return nil, err
	}
	c.setUnresolvedRef(ctx, machine, op)
	if c.ssh == nil {
		return c.failOperation(ctx, op, domain.ReasonAgentDisconnected,
			"ssh transport is not wired on this control plane")
	}
	snapJSON, err := snap.JSON()
	if err != nil {
		return nil, err
	}
	ctx2, cancel := context.WithTimeout(ctx, c.cfg.InventoryTimeout)
	defer cancel()
	out, err := c.ssh.Plan(ctx2, machine, op.Metadata.Name, snap.Generation, snapJSON)
	if err != nil {
		return c.sshFailure(ctx, machine, op, err)
	}
	// 计划摘要进审计（AutoPlan 不改机器，但"系统看过这台机器、看到的是哪份计划"
	// 必须可追溯，§7.8 审计要求 1）。
	if out.PlanDigest != "" {
		if err := c.ops.Transition(ctx, op.Metadata.Name, "", op.Status.Phase,
			func(o *domain.Operation) error {
				o.Spec.PlanDigest = out.PlanDigest
				return nil
			}); err != nil {
			c.log.Warn("record autoplan digest failed", "operation_id", op.Metadata.Name, "err", err)
		}
	}
	if _, err := c.OnResult(ctx, machine, OperationResultMsg{
		OperationID: op.Metadata.Name,
		Phase:       domain.OperationPhaseSucceeded,
		Verify:      nil, // 只读：不宣称收敛，交由 EvaluateDrift 按新观测求值
		FinishedAt:  c.now(),
	}); err != nil {
		return nil, err
	}
	return c.ops.Get(ctx, op.Metadata.Name)
}
