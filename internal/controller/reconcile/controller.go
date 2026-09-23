// Package reconcile 实现服务端 Reconcile 控制器（架构 v1.1.2 §4.3/§6.4/§9.6/§9.7）：
// Operation 状态机与机器级互斥（持久层表达）、ExecuteOperation 派发、结果处理
// （幂等去重、迟到结果代绑定）、取消/跳过/确认三例外端点、计划超时、重启自愈、
// drift 三态求值。条件写入纪律：本控制器只写 Drifted/Reconciled/Degraded（§6.2）。
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/desiredstate"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// Dispatcher 是控制面 → 节点的下行派发通道（由 grpcagent 的 Connect 流实现）。
// 机器无活跃流时返回 domain.ErrAgentDisconnected（§4.3 路由）。
type Dispatcher interface {
	ExecuteOperation(ctx context.Context, machine string, op *domain.Operation, snap *domain.DesiredStateSnapshot) error
	CancelOperation(ctx context.Context, machine, operationID string) error
}

// Config 是控制器参数。
type Config struct {
	// PlanTimeout 是确认窗口（§9.3：默认 30 分钟，可配置）。
	PlanTimeout time.Duration
	// FreshnessWindow 是观测新鲜度阈值（§6.2：默认 3×inventory 间隔 = 15min）。
	FreshnessWindow time.Duration
	// InventoryTimeout 是 SSH 手动 inventory 的整体超时（默认 2 分钟：含 scp 与
	// 远端 agentd 启动；FR-9.5 的 <1s 指标不适用于 SSH 路径，这里给足余量）。
	InventoryTimeout time.Duration
}

// Controller 是 Reconcile 控制器。
type Controller struct {
	machines   domain.MachineRepository
	status     domain.MachineStatusRepository
	snapshots  domain.SnapshotRepository
	ops        domain.OperationRepository
	observed   domain.ObservedStateRepository
	render     RenderFn
	dispatcher Dispatcher
	ssh        SSHPlanner
	cfg        Config
	log        *slog.Logger
	now        func() time.Time
}

// NewRenderer 把 desiredstate.Render 与各资源仓储装配为 RenderFn
// （§4.8 输入组装：Machine(含 overrides) → Profile → Skill 精确修订 → Provider → schema 版本）。
func NewRenderer(machines domain.MachineRepository, profiles domain.ProfileRepository,
	skills domain.SkillRepository, providers domain.ProviderRepository,
	schemaVersion string) RenderFn {
	return func(ctx context.Context, machine string) (*domain.DesiredStateSnapshot, error) {
		m, err := machines.Get(ctx, machine)
		if err != nil {
			return nil, err
		}
		var core domain.MachineCoreSpec
		if len(m.SpecJSON()) != 0 {
			if err := json.Unmarshal(m.SpecJSON(), &core); err != nil {
				return nil, fmt.Errorf("%w: machine spec: %v", domain.ErrInvalid, err)
			}
		}
		if core.ProfileRef == "" {
			return nil, fmt.Errorf("%w: machine %q has no profileRef", domain.ErrInvalid, machine)
		}
		profile, err := profiles.Get(ctx, core.ProfileRef)
		if err != nil {
			return nil, fmt.Errorf("%w: profile %q of machine %q: %v", domain.ErrInvalid, core.ProfileRef, machine, err)
		}
		skillMap := map[string]*domain.Skill{}
		if list, err := skills.List(ctx); err == nil {
			for _, s := range list {
				skillMap[s.Metadata.Name] = s
			}
		}
		providerMap := map[string]*domain.ModelProvider{}
		if list, err := providers.List(ctx); err == nil {
			for _, p := range list {
				providerMap[p.Metadata.Name] = p
			}
		}
		state, err := desiredstate.Render(desiredstate.RenderInputs{
			Machine: m, Profile: profile, Skills: skillMap, Providers: providerMap,
			SchemaVersion: schemaVersion,
		})
		if err != nil {
			return nil, err
		}
		return desiredstate.BuildSnapshot(machine, 0, state)
	}
}

func NewController(machines domain.MachineRepository, status domain.MachineStatusRepository,
	snapshots domain.SnapshotRepository, ops domain.OperationRepository,
	observed domain.ObservedStateRepository, render RenderFn, cfg Config, log *slog.Logger) *Controller {
	if cfg.PlanTimeout <= 0 {
		cfg.PlanTimeout = 30 * time.Minute
	}
	if cfg.FreshnessWindow <= 0 {
		cfg.FreshnessWindow = 15 * time.Minute
	}
	if cfg.InventoryTimeout <= 0 {
		cfg.InventoryTimeout = 2 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	return &Controller{
		machines: machines, status: status, snapshots: snapshots, ops: ops,
		observed: observed, render: render, cfg: cfg, log: log,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// SetDispatcher 装配下行派发通道（main 装配：grpcagent.Server 实现本接口；
// 二者相互引用，以 setter 打破构造环）。
func (c *Controller) SetDispatcher(d Dispatcher) { c.dispatcher = d }

// RenderFn 渲染一台机器的期望状态（由 main 以 desiredstate.Render + 各仓储装配）。
type RenderFn func(ctx context.Context, machine string) (*domain.DesiredStateSnapshot, error)

// EnsureSnapshot 确认目标快照 generation（§4.3：每次触发先确认）：
// 渲染当前有效期望并物化——digest 与上一代相同则复用现行走（FR-7.2）。
func (c *Controller) EnsureSnapshot(ctx context.Context, machine string) (*domain.DesiredStateSnapshot, bool, error) {
	// renderFn 由 NewRenderer 注入；此处经包内适配避免循环依赖。
	snap, err := c.render(ctx, machine)
	if err != nil {
		return nil, false, err
	}
	stored, changed, err := c.snapshots.Materialize(ctx, snap)
	if err != nil {
		return nil, false, err
	}
	if changed {
		c.log.Info("desired state generation materialized", "machine_id", machine,
			"generation", stored.Generation, "digest", stored.Digest)
	}
	return stored, changed, nil
}

// ReconcileRequest 是 POST /machines/{id}/reconcile 的语义载荷（§8.1）。
type ReconcileRequest struct {
	// ConfirmPlanDigest 非空表示"确认该操作自己的计划"（互斥三例外之一，
	// §4.3 第 6 条）：不创建新操作，只迁移处于 AwaitingConfirmation 的操作。
	ConfirmPlanDigest string `json:"confirmPlanDigest,omitempty"`
}

// Reconcile 触发一次收敛（§4.3 派发点获取：以一次事务创建 Pending 操作行，
// 唯一约束冲突即 409 MachineBusy，不排队）。
func (c *Controller) Reconcile(ctx context.Context, machine string, req ReconcileRequest) (*domain.Operation, error) {
	if req.ConfirmPlanDigest != "" {
		return c.confirmPlan(ctx, machine, req.ConfirmPlanDigest)
	}
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
	op := &domain.Operation{
		Spec: domain.OperationSpec{
			Machine:           machine,
			Type:              domain.OperationTypeReconcile,
			Transport:         mode,
			DesiredGeneration: snap.Generation,
		},
		Status: domain.OperationStatus{Phase: domain.OperationPhasePending},
	}
	if err := c.ops.Create(ctx, op); err != nil {
		return nil, err // ErrMachineBusy 原样上抛（HTTP 409 + diagnostics）
	}
	c.setUnresolvedRef(ctx, machine, op)
	c.log.Info("operation created", "event", "operation_created", "operation_id", op.Metadata.Name,
		"machine_id", machine, "type", op.Spec.Type, "desired_generation", op.Spec.DesiredGeneration)

	// SSH-only 通道（§9.3）：人发起的路径必须"产出计划 → 操作者按 planDigest 确认
	// → apply 重算并比对基线"，因此这里先做 plan 并把操作置 AwaitingConfirmation。
	if op.Spec.Transport == domain.TransportSSH {
		return c.planOverSSH(ctx, machine, op, snap)
	}
	if err := c.dispatchExecute(ctx, machine, op, snap); err != nil {
		if !errors.Is(err, domain.ErrAgentDisconnected) {
			c.log.Error("execute dispatch failed", "operation_id", op.Metadata.Name, "err", err)
		}
		// §4.3 路由：不可达 → 报错并写 Operation.Failed（reason 明确）。
		return c.failOperation(ctx, op, domain.ReasonAgentDisconnected,
			fmt.Sprintf("machine is not reachable via %s: %v", mode, err))
	}
	return op, nil
}

// confirmPlan 落实确认端点语义（§9.3）：确认对象是具体计划（planDigest）。
// 不创建第二条未决操作；确认后仍由节点在 apply 前重算基线（FR-12.7）。
func (c *Controller) confirmPlan(ctx context.Context, machine, confirmDigest string) (*domain.Operation, error) {
	op, err := c.unresolvedOp(ctx, machine)
	if err != nil {
		return nil, err
	}
	if op.Status.Phase != domain.OperationPhaseAwaitingConfirmation {
		return nil, fmt.Errorf("%w: operation %s is %s, not AwaitingConfirmation",
			domain.ErrOpState, op.Metadata.Name, op.Status.Phase)
	}
	if op.Spec.PlanDigest == "" || op.Spec.PlanDigest != confirmDigest {
		// 确认的不是当初那份计划：要求重新 plan 与重新确认。
		return nil, fmt.Errorf("%w: confirmPlanDigest does not match the awaiting plan (%s)",
			domain.ErrReplanRequired, shortDigest(op.Spec.PlanDigest))
	}
	err = c.ops.Transition(ctx, op.Metadata.Name, domain.OperationPhaseAwaitingConfirmation,
		domain.OperationPhaseRunning, func(o *domain.Operation) error {
			now := c.now()
			o.Status.StartedAt = &now
			o.Status.PlanExpiresAt = nil
			return nil
		})
	if err != nil {
		return nil, err
	}
	confirmed, err := c.ops.Get(ctx, op.Metadata.Name)
	if err != nil {
		return nil, err
	}
	c.log.Info("plan confirmed", "event", "plan_confirmed", "operation_id", confirmed.Metadata.Name,
		"machine_id", machine, "plan_digest", shortDigest(confirmDigest))
	snap, err := c.snapshots.Get(ctx, machine, confirmed.Spec.DesiredGeneration)
	if err != nil {
		return nil, err
	}
	if confirmed.Spec.Transport == domain.TransportSSH {
		return c.applyOverSSH(ctx, machine, confirmed, snap)
	}
	if err := c.dispatchExecute(ctx, machine, confirmed, snap); err != nil {
		return c.failOperation(ctx, confirmed, domain.ReasonAgentDisconnected, err.Error())
	}
	return confirmed, nil
}

// RequestPlanConfirmation 是"产出计划 → 等待确认"的进入点（slice 6 的 SSH
// oneshot plan 与本片状态机测试共用）：创建操作行并置 AwaitingConfirmation。
// 节点不持执行权、控制面互斥仍持有（§6.4 契约）。
func (c *Controller) RequestPlanConfirmation(ctx context.Context, machine, planDigest string, readOnly bool) (*domain.Operation, error) {
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
	op := &domain.Operation{
		Spec: domain.OperationSpec{
			Machine: machine, Type: domain.OperationTypeReconcile, Transport: mode,
			DesiredGeneration: snap.Generation, ReadOnly: readOnly, PlanDigest: planDigest,
		},
		Status: domain.OperationStatus{Phase: domain.OperationPhasePending},
	}
	if err := c.ops.Create(ctx, op); err != nil {
		return nil, err
	}
	expires := c.now().Add(c.cfg.PlanTimeout)
	err = c.ops.Transition(ctx, op.Metadata.Name, domain.OperationPhasePending,
		domain.OperationPhaseAwaitingConfirmation, func(o *domain.Operation) error {
			o.Status.PlanExpiresAt = &expires
			return nil
		})
	if err != nil {
		return nil, err
	}
	c.setUnresolvedRef(ctx, machine, op)
	c.log.Info("plan awaiting confirmation", "event", "plan_awaiting_confirmation",
		"operation_id", op.Metadata.Name, "machine_id", machine,
		"plan_digest", shortDigest(planDigest), "expires_at", expires.Format(time.RFC3339))
	return c.ops.Get(ctx, op.Metadata.Name)
}

// ReconcileGeneration 针对**已物化的特定代**创建并派发操作（Deployment 控制器
// 的推进路径：目标代由部署/回滚语义决定，而非 profile 重新渲染）。互斥与派发
// 语义与 Reconcile 完全一致（§4.3：跨通道同表同约束，禁止绕过互斥）。
func (c *Controller) ReconcileGeneration(ctx context.Context, machine string, generation int64, opType string) (*domain.Operation, error) {
	snap, err := c.snapshots.Get(ctx, machine, generation)
	if err != nil {
		return nil, err
	}
	if _, err := c.machines.Get(ctx, machine); err != nil {
		return nil, err
	}
	mode, _, err := c.channelState(ctx, machine)
	if err != nil {
		return nil, err
	}
	op := &domain.Operation{
		Spec: domain.OperationSpec{
			Machine: machine, Type: opType, Transport: mode,
			DesiredGeneration: generation,
		},
		Status: domain.OperationStatus{Phase: domain.OperationPhasePending},
	}
	if err := c.ops.Create(ctx, op); err != nil {
		return nil, err
	}
	c.setUnresolvedRef(ctx, machine, op)
	c.log.Info("operation created", "event", "operation_created", "operation_id", op.Metadata.Name,
		"machine_id", machine, "type", op.Spec.Type, "desired_generation", generation)
	if op.Spec.Transport == domain.TransportSSH {
		// Deployment 驱动路径同样先产出计划；"按策略自动确认"本片未定义策略，
		// 因此一律要求操作者确认（宁可停在确认窗口，也不静默应用）。
		return c.planOverSSH(ctx, machine, op, snap)
	}
	if err := c.dispatchExecute(ctx, machine, op, snap); err != nil {
		return c.failOperation(ctx, op, domain.ReasonAgentDisconnected, err.Error())
	}
	return op, nil
}

// Cancel 是互斥例外端点之一（§4.3 第 6 条/§9.7）：置 CancelRequested，仍属
// 未决集合；终态只在节点确认停止后写入。节点不可达时不单方面置 Cancelled。
// 重复取消幂等。
func (c *Controller) Cancel(ctx context.Context, machine, opID string) (*domain.Operation, error) {
	op, err := c.ops.Get(ctx, opID)
	if err != nil {
		return nil, err
	}
	if op.Spec.Machine != machine {
		return nil, fmt.Errorf("%w: operation %s belongs to %s", domain.ErrNotFound, opID, op.Spec.Machine)
	}
	if domain.IsTerminal(op.Status.Phase) {
		return op, nil // 幂等：已终态原样返回
	}
	switch op.Status.Phase {
	case domain.OperationPhaseCancelRequested:
		return op, nil // 重复取消幂等（FR-13.9）
	case domain.OperationPhaseAwaitingConfirmation:
		// 节点不持有执行权、无可中断流水线：直接终态并释放互斥（§9.3 第 5 条）。
		err := c.ops.Transition(ctx, opID, domain.OperationPhaseAwaitingConfirmation,
			domain.OperationPhaseFailed, func(o *domain.Operation) error {
				now := c.now()
				o.Status.FinishedAt = &now
				o.Status.TerminalModifier = domain.ModifierCancelled
				return nil
			})
		if err != nil {
			return nil, err
		}
		c.clearUnresolvedRef(ctx, machine)
		c.log.Info("awaiting plan cancelled", "event", "operation_cancelled",
			"operation_id", opID, "machine_id", machine)
		// 尽力通知节点清理排队状态；失败不影响终态。
		if c.dispatcher != nil {
			_ = c.dispatcher.CancelOperation(ctx, machine, opID)
		}
		return c.ops.Get(ctx, opID)
	default:
		// Pending/Running/Unknown：置 CancelRequested 并投递取消；节点确认
		// （OperationResult Failed(Cancelled) 或 outbox 重报）后才离开未决集合。
		err := c.ops.Transition(ctx, opID, "", domain.OperationPhaseCancelRequested, nil)
		if err != nil {
			return nil, err
		}
		c.setUnresolvedRefPhase(ctx, machine, opID, domain.OperationPhaseCancelRequested)
		c.log.Info("cancel requested", "event", "operation_cancel_requested",
			"operation_id", opID, "machine_id", machine, "previous_phase", op.Status.Phase)
		if op.Spec.Transport == domain.TransportSSH && c.ssh != nil {
			// SSH-only：没有 gRPC 通道，取消经 SSH 落标记文件，节点在阶段边界
			// 观察（§5.1）。投递失败同样只保持 CancelRequested，绝不单方面终态。
			if cerr := c.ssh.Cancel(ctx, machine, opID); cerr != nil {
				c.log.Warn("ssh cancel delivery failed; waiting for node confirmation",
					"operation_id", opID, "machine_id", machine, "err", cerr)
			}
		} else if c.dispatcher != nil {
			if derr := c.dispatcher.CancelOperation(ctx, machine, opID); derr != nil {
				// 节点不可达：保持 CancelRequested，等待节点重连后取消或重报
				//（服务端不得单方面置 Cancelled，FR-13.9）。
				c.log.Warn("cancel dispatch failed; waiting for node confirmation",
					"operation_id", opID, "machine_id", machine, "err", derr)
			}
		}
		return c.ops.Get(ctx, opID)
	}
}

// SkipRequest 是跳过端点的载荷（§9.6 路径 C：必须填原因，留审计）。
type SkipRequest struct {
	Reason   string `json:"reason"`
	Operator string `json:"operator,omitempty"`
}

// Skip 是互斥例外端点之一：显式跳过未决操作（含 Unknown）。立即释放控制面
// 互斥，但保留"节点可能仍在写"的提示；不计入成功（terminalModifier=Skipped
// 使健康门禁的"操作成功"条件不满足），不允许静默跳过。
func (c *Controller) Skip(ctx context.Context, machine, opID string, req SkipRequest) (*domain.Operation, error) {
	if req.Reason == "" {
		return nil, fmt.Errorf("%w: skip requires a reason (audited, FR-15.4)", domain.ErrInvalid)
	}
	op, err := c.ops.Get(ctx, opID)
	if err != nil {
		return nil, err
	}
	if op.Spec.Machine != machine {
		return nil, fmt.Errorf("%w: operation %s belongs to %s", domain.ErrNotFound, opID, op.Spec.Machine)
	}
	if domain.IsTerminal(op.Status.Phase) {
		return op, nil
	}
	err = c.ops.Transition(ctx, opID, "", domain.OperationPhaseSucceeded, func(o *domain.Operation) error {
		now := c.now()
		o.Status.FinishedAt = &now
		o.Status.TerminalModifier = domain.ModifierSkipped
		return nil
	})
	if err != nil {
		return nil, err
	}
	c.clearUnresolvedRef(ctx, machine)
	c.log.Warn("operation skipped by operator", "event", "operation_skipped",
		"operation_id", opID, "machine_id", machine, "previous_phase", op.Status.Phase,
		"reason", req.Reason, "operator", req.Operator,
		"note", "node may still be writing; not counted as success")
	return c.ops.Get(ctx, opID)
}

// Rollback 机器级回滚（§7.2）：对目标 generation 的快照重放一次 reconcile——
// 先把目标代内容物化为该机的新期望代（回滚不把机器拉回旧代，FR-7.5/ADR-2），
// 再派发普通 Reconcile（走适配器合并写，天然保留未托管字段）。
func (c *Controller) Rollback(ctx context.Context, machine string, targetGeneration int64) (*domain.Operation, error) {
	target, err := c.snapshots.Get(ctx, machine, targetGeneration)
	if err != nil {
		// 目标代不可用（不存在/已被清理）→ RollbackUnsupported，不虚报成功。
		return nil, fmt.Errorf("%w: generation %d of machine %q: %v",
			domain.ErrRollbackUnsupported, targetGeneration, machine, err)
	}
	if _, err := c.machines.Get(ctx, machine); err != nil {
		return nil, err
	}
	// 物化新代：内容 = 目标代内容。digest 与当前上一代相同时 Materialize 会
	// 复用现行走——此时机器已在目标内容上，回滚即普通收敛。
	replayed := *target
	replayed.Generation = 0
	stored, _, err := c.snapshots.Materialize(ctx, &replayed)
	if err != nil {
		return nil, err
	}
	mode, connected, err := c.channelState(ctx, machine)
	if err != nil {
		return nil, err
	}
	op := &domain.Operation{
		Spec: domain.OperationSpec{
			Machine: machine, Type: domain.OperationTypeRollback, Transport: mode,
			DesiredGeneration: stored.Generation,
		},
		Status: domain.OperationStatus{Phase: domain.OperationPhasePending},
	}
	if err := c.ops.Create(ctx, op); err != nil {
		return nil, err
	}
	c.setUnresolvedRef(ctx, machine, op)
	c.log.Info("rollback materialized", "event", "rollback_materialized",
		"operation_id", op.Metadata.Name, "machine_id", machine,
		"rollback_of", targetGeneration, "new_generation", stored.Generation)
	if !connected {
		return c.failOperation(ctx, op, domain.ReasonAgentDisconnected,
			fmt.Sprintf("machine is not reachable via %s", mode))
	}
	if err := c.dispatchExecute(ctx, machine, op, stored); err != nil {
		return c.failOperation(ctx, op, domain.ReasonAgentDisconnected, err.Error())
	}
	return op, nil
}

// RecoverInflight 服务重启自愈（§4.3 第 4 条/FR-15.3）：把 Pending/Running 置
// Unknown，但**不**移出未决集合——互斥随数据库恢复而恢复。
func (c *Controller) RecoverInflight(ctx context.Context) error {
	unresolved, err := c.ops.ListUnresolved(ctx)
	if err != nil {
		return err
	}
	for _, op := range unresolved {
		if op.Status.Phase != domain.OperationPhasePending && op.Status.Phase != domain.OperationPhaseRunning {
			continue // AwaitingConfirmation/CancelRequested/Unknown 是持久状态，跨重启保持
		}
		err := c.ops.Transition(ctx, op.Metadata.Name, "", domain.OperationPhaseUnknown, nil)
		if err != nil {
			return err
		}
		c.setUnresolvedRefPhase(ctx, op.Spec.Machine, op.Metadata.Name, domain.OperationPhaseUnknown)
		c.log.Warn("in-flight operation marked unknown after restart", "event", "operation_unknown",
			"operation_id", op.Metadata.Name, "machine_id", op.Spec.Machine,
			"previous_phase", op.Status.Phase)
	}
	return nil
}

// ScanPlanTimeout 是确认窗口超时扫描（§9.3 第 4 条）：计划超时后操作以 Failed
// 终结（审计保留 planDigest 与超时时刻），互斥同时释放。
func (c *Controller) ScanPlanTimeout(ctx context.Context, now time.Time) error {
	unresolved, err := c.ops.ListUnresolved(ctx)
	if err != nil {
		return err
	}
	for _, op := range unresolved {
		if op.Status.Phase != domain.OperationPhaseAwaitingConfirmation || op.Status.PlanExpiresAt == nil {
			continue
		}
		if now.Before(*op.Status.PlanExpiresAt) {
			continue
		}
		err := c.ops.Transition(ctx, op.Metadata.Name, domain.OperationPhaseAwaitingConfirmation,
			domain.OperationPhaseFailed, func(o *domain.Operation) error {
				t := now.UTC()
				o.Status.FinishedAt = &t
				return nil
			})
		if err != nil {
			return err
		}
		c.clearUnresolvedRef(ctx, op.Spec.Machine)
		c.log.Warn("plan confirmation window expired", "event", "plan_timeout",
			"operation_id", op.Metadata.Name, "machine_id", op.Spec.Machine,
			"plan_digest", shortDigest(op.Spec.PlanDigest), "expired_at",
			op.Status.PlanExpiresAt.Format(time.RFC3339), "reason_code", domain.ReasonReplanRequired)
	}
	return nil
}

// ---- 内部辅助 ----

// dispatchExecute 经 Connect 流下发 ExecuteOperation（一律携带完整快照，FR-13.6）。
func (c *Controller) dispatchExecute(ctx context.Context, machine string, op *domain.Operation, snap *domain.DesiredStateSnapshot) error {
	if c.dispatcher == nil {
		return fmt.Errorf("%w: dispatcher not wired", domain.ErrAgentDisconnected)
	}
	return c.dispatcher.ExecuteOperation(ctx, machine, op, snap)
}

// failOperation 把操作写为 Failed 并返回（互斥释放）。
func (c *Controller) failOperation(ctx context.Context, op *domain.Operation, reason, msg string) (*domain.Operation, error) {
	err := c.ops.Transition(ctx, op.Metadata.Name, "", domain.OperationPhaseFailed,
		func(o *domain.Operation) error {
			now := c.now()
			o.Status.FinishedAt = &now
			return nil
		})
	if err != nil && !errors.Is(err, domain.ErrOpState) {
		return nil, err
	}
	// reason/message 进审计日志与步骤表（操作行 spec 不可变；诊断信息不丢）。
	_ = c.ops.ReplaceSteps(ctx, op.Metadata.Name, []domain.OperationStepRecord{{
		Seq: 1, Name: "dispatch", Phase: domain.OperationPhaseFailed,
		Output:     fmt.Sprintf("reason_code=%s message=%s", reason, msg),
		RecordedAt: c.now(),
	}})
	c.clearUnresolvedRef(ctx, op.Spec.Machine)
	c.log.Warn("operation failed before execution", "event", "operation_failed",
		"operation_id", op.Metadata.Name, "machine_id", op.Spec.Machine,
		"reason_code", reason, "message", msg)
	return c.ops.Get(ctx, op.Metadata.Name)
}

func (c *Controller) unresolvedOp(ctx context.Context, machine string) (*domain.Operation, error) {
	ops, err := c.ops.ListByMachine(ctx, machine)
	if err != nil {
		return nil, err
	}
	var unresolved []*domain.Operation
	for _, op := range ops {
		if domain.IsUnresolved(op.Status.Phase) {
			unresolved = append(unresolved, op)
		}
	}
	if len(unresolved) == 0 {
		return nil, fmt.Errorf("%w: machine %q has no unresolved operation", domain.ErrOpState, machine)
	}
	sort.Slice(unresolved, func(i, j int) bool {
		return unresolved[i].Metadata.CreationTimestamp.Before(unresolved[j].Metadata.CreationTimestamp)
	})
	return unresolved[0], nil
}

// UnresolvedRef 供 409 MachineBusy 的 diagnostics 使用（§8.1：带未决操作 id 与
// phase，UI 直接呈现处置入口）。
func (c *Controller) UnresolvedRef(ctx context.Context, machine string) *domain.UnresolvedOperationRef {
	op, err := c.unresolvedOp(ctx, machine)
	if err != nil {
		return nil
	}
	return &domain.UnresolvedOperationRef{ID: op.Metadata.Name, Phase: op.Status.Phase, Type: op.Spec.Type}
}

// channelState 返回派发通道与管理模式（§4.3 路由：agentd 且在线 → gRPC；
// SSH → 第 6 片；都不可用 → AgentDisconnected）。
func (c *Controller) channelState(ctx context.Context, machine string) (mode string, connected bool, err error) {
	m, err := c.machines.Get(ctx, machine)
	if err != nil {
		return "", false, err
	}
	var core domain.MachineCoreSpec
	if len(m.SpecJSON()) != 0 {
		if err := json.Unmarshal(m.SpecJSON(), &core); err != nil {
			return "", false, fmt.Errorf("%w: machine spec: %v", domain.ErrInvalid, err)
		}
	}
	if core.ManagementMode == "" {
		core.ManagementMode = domain.ManagementModeAgentd
	}
	if core.ManagementMode != domain.ManagementModeAgentd {
		// SSH-only（§9.3）：可达性来自 SSHReachable 条件（SSH 探测写入）。未探测过
		// 时返回 false，但 SSH 路径仍会自行探测并把真实失败的 §30.1 reason 带回来
		// ——比"未探测即拒绝派发"更诚实。
		st, err := domain.ParseMachineStatus(m.StatusJSON())
		if err != nil {
			return "", false, err
		}
		cond, ok := st.GetCondition(domain.ConditionSSHReachable)
		return domain.TransportSSH, ok && cond.Status == domain.ConditionTrue, nil
	}
	st, err := domain.ParseMachineStatus(m.StatusJSON())
	if err != nil {
		return "", false, err
	}
	cond, ok := st.GetCondition(domain.ConditionAgentConnected)
	connected = ok && cond.Status == domain.ConditionTrue
	return domain.TransportAgentd, connected, nil
}

func (c *Controller) setUnresolvedRef(ctx context.Context, machine string, op *domain.Operation) {
	c.setUnresolvedRefPhase(ctx, machine, op.Metadata.Name, op.Status.Phase)
}

func (c *Controller) setUnresolvedRefPhase(ctx context.Context, machine, opID, phase string) {
	err := c.status.UpdateStatus(ctx, machine, func(st *domain.MachineStatus) error {
		st.UnresolvedOperation = &domain.UnresolvedOperationRef{ID: opID, Phase: phase}
		return nil
	})
	if err != nil {
		c.log.Error("set unresolved ref failed", "machine_id", machine, "operation_id", opID, "err", err)
	}
}

func (c *Controller) clearUnresolvedRef(ctx context.Context, machine string) {
	err := c.status.UpdateStatus(ctx, machine, func(st *domain.MachineStatus) error {
		st.UnresolvedOperation = nil
		return nil
	})
	if err != nil {
		c.log.Error("clear unresolved ref failed", "machine_id", machine, "err", err)
	}
}

func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19] + "…"
	}
	return d
}
