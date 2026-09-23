// Package deployment 实现控制面 Deployment 控制器（架构 v1.1.2 §4.4/FR-10.x）：
// 状态机 Pending → Canary → RollingOut → (Paused | Succeeded | Failed)、
// 目标代一致性检查（FR-10.6）、未决目标不推进（FR-10.7）、回滚型发布的
// "每机物化新代"分支（FR-10.4/10.6 v1.1.2）与因果健康门禁（§4.4）。
package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// Reconciler 是 Deployment 控制器对 Reconcile 控制器的能力依赖（窄接口）。
type Reconciler interface {
	// ReconcileGeneration 针对已物化的特定代创建并派发操作（互斥同 §4.3）。
	ReconcileGeneration(ctx context.Context, machine string, generation int64, opType string) (*domain.Operation, error)
	// UnresolvedRef 返回机器当前未决操作引用（FR-10.7 阻塞判定）；无则 nil。
	UnresolvedRef(ctx context.Context, machine string) *domain.UnresolvedOperationRef
}

// Controller 是 Deployment 控制器。
type Controller struct {
	deploys   domain.DeploymentRepository
	targets   domain.DeploymentTargetRepository
	machines  domain.MachineRepository
	snapshots domain.SnapshotRepository
	ops       domain.OperationRepository
	observed  domain.ObservedStateRepository
	reconcile Reconciler
	log       *slog.Logger
	now       func() time.Time
}

func NewController(deploys domain.DeploymentRepository, targets domain.DeploymentTargetRepository,
	machines domain.MachineRepository, snapshots domain.SnapshotRepository,
	ops domain.OperationRepository, observed domain.ObservedStateRepository,
	reconcile Reconciler, log *slog.Logger) *Controller {
	if log == nil {
		log = slog.Default()
	}
	return &Controller{
		deploys: deploys, targets: targets, machines: machines, snapshots: snapshots,
		ops: ops, observed: observed, reconcile: reconcile, log: log,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// Create 落库一个 Deployment 并初始化目标集合（§4.4 目标解析：显式机器列表）。
func (c *Controller) Create(ctx context.Context, d *domain.Deployment) error {
	core, err := domain.ParseDeploymentCore(d.SpecJSON())
	if err != nil {
		return err
	}
	if core.TargetGeneration <= 0 {
		return fmt.Errorf("%w: targetGeneration is required", domain.ErrInvalid)
	}
	if len(core.MachineNames) == 0 {
		return fmt.Errorf("%w: selector.machineNames must be explicit", domain.ErrInvalid)
	}
	// 目标代必须可解析（引用既往 recorded generation，FR-7.5）。
	if _, err := c.snapshots.Get(ctx, core.MachineNames[0], core.TargetGeneration); err != nil {
		// 各机代空间独立；目标代按"内容代"校验：至少一台目标机可达该代即合法，
		// 回滚型发布按机物化（FR-10.1 v1.1.2）。
		var firstErr error
		for _, name := range core.MachineNames {
			if _, err := c.snapshots.Get(ctx, name, core.TargetGeneration); err == nil {
				firstErr = nil
				break
			} else if firstErr == nil {
				firstErr = err
			}
		}
		if firstErr != nil {
			return fmt.Errorf("%w: target generation %d not resolvable: %v",
				domain.ErrRollbackUnsupported, core.TargetGeneration, firstErr)
		}
	}
	if err := c.deploys.Create(ctx, d); err != nil {
		return err
	}
	now := c.now()
	var targets []domain.DeploymentTargetStatus
	for _, name := range core.MachineNames {
		targets = append(targets, domain.DeploymentTargetStatus{
			Machine: name, Phase: domain.TargetPhasePending, UpdatedAt: now,
		})
	}
	if err := c.targets.Replace(ctx, d.Metadata.Name, targets); err != nil {
		return err
	}
	c.writeStatus(ctx, d.Metadata.Name, domain.DeploymentStatus{Phase: domain.DeploymentPhasePending})
	c.log.Info("deployment created", "event", "deployment_created",
		"deployment", d.Metadata.Name, "target_generation", core.TargetGeneration,
		"rollback_of", core.RollbackOf, "targets", len(core.MachineNames))
	return nil
}

// CreateRollback 创建回滚型 Deployment（§4.4/FR-10.4：POST /deployments/{id}/rollback
// → 新 Deployment，spec.rollbackOf = 被回滚 generation；历史不可变）。
func (c *Controller) CreateRollback(ctx context.Context, sourceName string, targetGeneration int64) (*domain.Deployment, error) {
	source, err := c.deploys.Get(ctx, sourceName)
	if err != nil {
		return nil, err
	}
	core, err := domain.ParseDeploymentCore(source.SpecJSON())
	if err != nil {
		return nil, err
	}
	if targetGeneration <= 0 {
		targetGeneration = core.TargetGeneration // 缺省回滚到该发布自己的目标代内容
	}
	rb := &domain.Deployment{
		Spec: mustJSON(map[string]any{
			"machineNames":     core.MachineNames,
			"targetGeneration": targetGeneration,
			"rollbackOf":       core.TargetGeneration,
			"strategy":         core.Strategy,
		}),
		Status: json.RawMessage("{}"),
	}
	rb.Metadata.Name = fmt.Sprintf("%s-rollback-%d", sourceName, targetGeneration)
	if err := c.Create(ctx, rb); err != nil {
		return nil, err
	}
	c.log.Info("rollback deployment created", "event", "deployment_rollback_created",
		"deployment", rb.Metadata.Name, "source", sourceName,
		"content_generation", targetGeneration, "rollback_of", core.TargetGeneration)
	return rb, nil
}

// SkipTarget 显式跳过一个被未决操作阻塞的目标（FR-10.7：记为 Skipped 并留审计，
// 不允许静默跳过、不计入成功）。
func (c *Controller) SkipTarget(ctx context.Context, deploymentName, machine, reason string) error {
	if reason == "" {
		return fmt.Errorf("%w: skip requires a reason", domain.ErrInvalid)
	}
	targets, err := c.targets.List(ctx, deploymentName)
	if err != nil {
		return err
	}
	for _, t := range targets {
		if t.Machine != machine {
			continue
		}
		t.Phase = domain.TargetPhaseSkipped
		t.Reason = "SkippedByOperator: " + reason
		t.UpdatedAt = c.now()
		if err := c.targets.Update(ctx, deploymentName, t); err != nil {
			return err
		}
		c.log.Warn("deployment target skipped", "event", "deployment_target_skipped",
			"deployment", deploymentName, "machine_id", machine, "reason", reason)
		return nil
	}
	return fmt.Errorf("%w: target %s/%s", domain.ErrNotFound, deploymentName, machine)
}

// Advance 推进一个 Deployment 一个批次步（控制面重启后从持久化批进度继续）。
// 同步等待当批操作终态并评估门禁后返回；由 main 的周期循环驱动。
func (c *Controller) Advance(ctx context.Context, name string) error {
	dep, err := c.deploys.Get(ctx, name)
	if err != nil {
		return err
	}
	core, err := domain.ParseDeploymentCore(dep.SpecJSON())
	if err != nil {
		return err
	}
	status, err := parseStatus(dep.StatusJSON())
	if err != nil {
		return err
	}
	switch status.Phase {
	case domain.DeploymentPhaseSucceeded, domain.DeploymentPhaseFailed:
		return nil // 终态
	case domain.DeploymentPhasePaused:
		return nil // 暂停：不推进（pauseOnFailure 或操作者暂停）
	}

	targets, err := c.targets.List(ctx, name)
	if err != nil {
		return err
	}
	// 先收割上一批的终态并评估门禁。
	if err := c.harvest(ctx, name, targets); err != nil {
		return err
	}
	targets, err = c.targets.List(ctx, name)
	if err != nil {
		return err
	}

	running := 0
	succeeded, failed, superseded, skipped := 0, 0, 0, 0
	var pending []*domain.DeploymentTargetStatus
	for i := range targets {
		switch targets[i].Phase {
		case domain.TargetPhaseRunning:
			running++
		case domain.TargetPhaseSucceeded:
			succeeded++
		case domain.TargetPhaseFailed:
			failed++
		case domain.TargetPhaseSuperseded:
			superseded++
		case domain.TargetPhaseSkipped:
			skipped++
		case domain.TargetPhasePending, domain.TargetPhaseBlocked:
			t := targets[i]
			pending = append(pending, &t)
		}
	}
	total := len(targets)
	done := succeeded + failed + superseded + skipped

	if done == total {
		return c.finish(ctx, name, core, succeeded, failed, superseded)
	}
	// pauseOnFailure：任一失败立即暂停（FR-10.2）。
	if failed > 0 && core.Strategy.PauseOnFailure && status.Phase != domain.DeploymentPhasePaused {
		c.writeStatus(ctx, name, domain.DeploymentStatus{Phase: domain.DeploymentPhasePaused,
			Reason: "pauseOnFailure triggered; failed targets present"})
		c.log.Warn("deployment paused on failure", "event", "deployment_paused",
			"deployment", name, "failed", failed)
		return nil
	}

	// 批次推进：canary 批优先，其后按 batchSize；maxUnavailable 限制同批在途数。
	batch := c.nextBatchSize(core, status.Phase, len(pending), running)
	if batch == 0 {
		// 无可推进批次（等待在途目标或低代目标收敛）。
		if running == 0 && len(pending) > 0 {
			// 全部 pending 都被阻塞/等待且无在途：仍可能被阻塞目标解除后推进。
			c.bumpPhaseIfWaiting(ctx, name, status, core, pending)
		}
		return nil
	}
	phase := domain.DeploymentPhaseRollingOut
	if status.Phase == domain.DeploymentPhasePending {
		phase = domain.DeploymentPhaseCanary
	}
	dispatched := 0
	for _, t := range pending {
		if dispatched >= batch {
			break
		}
		switch c.advanceTarget(ctx, name, core, t) {
		case targetDispatched:
			dispatched++
		case targetBlocked:
			// 保持 Pending/Blocked，下轮再试（FR-10.7：不计入失败也不推进）。
		case targetWait:
			// 当前代低于目标：正常落后，等待。
		}
	}
	if dispatched > 0 {
		c.writeStatus(ctx, name, domain.DeploymentStatus{Phase: phase})
	}
	return nil
}

type targetOutcome int

const (
	targetDispatched targetOutcome = iota
	targetBlocked
	targetWait
)

// advanceTarget 推进单个目标：代一致性检查（FR-10.6）→ 回滚物化分支 → 派发。
func (c *Controller) advanceTarget(ctx context.Context, name string, core domain.DeploymentCoreSpec,
	t *domain.DeploymentTargetStatus) targetOutcome {
	machine := t.Machine
	// FR-10.7：目标机存在未决操作（含 Unknown/AwaitingConfirmation）时阻塞。
	if ref := c.reconcile.UnresolvedRef(ctx, machine); ref != nil {
		t.Phase = domain.TargetPhaseBlocked
		t.Reason = fmt.Sprintf("BlockedByUnresolvedOperation %s (%s)", ref.ID, ref.Phase)
		t.UpdatedAt = c.now()
		_ = c.targets.Update(ctx, name, *t)
		c.log.Info("deployment target blocked by unresolved operation", "deployment", name,
			"machine_id", machine, "operation_id", ref.ID, "phase", ref.Phase)
		return targetBlocked
	}
	current, err := c.snapshots.Current(ctx, machine)
	if errors.Is(err, domain.ErrNotFound) {
		current = nil // 尚无期望代：视作"低于目标代"
	} else if err != nil {
		c.log.Error("read current generation failed", "machine_id", machine, "err", err)
		return targetWait
	}

	effectiveGen := core.TargetGeneration
	if core.RollbackOf != 0 {
		// 回滚型发布：不适用代一致性判定（v1.1.2 P-2 分支）。先把目标代**内容**
		// 物化为该机新期望代，再以新代为有效目标代参与门禁。
		content, err := c.snapshots.Get(ctx, machine, core.TargetGeneration)
		if err != nil {
			// 目标代内容不可用 → Failed(RollbackUnsupported)（FR-10.6）。
			t.Phase = domain.TargetPhaseFailed
			t.Reason = domain.ReasonRollbackUnsupported + ": generation content unavailable"
			t.UpdatedAt = c.now()
			_ = c.targets.Update(ctx, name, *t)
			return targetBlocked
		}
		replayed := *content
		replayed.Generation = 0
		stored, _, err := c.snapshots.Materialize(ctx, &replayed)
		if err != nil {
			c.log.Error("rollback materialize failed", "machine_id", machine, "err", err)
			return targetWait
		}
		effectiveGen = stored.Generation
		c.log.Info("rollback target materialized", "event", "deployment_rollback_materialized",
			"deployment", name, "machine_id", machine,
			"content_generation", core.TargetGeneration, "new_generation", effectiveGen)
	} else if current != nil {
		// 普通发布的目标代一致性检查（FR-10.6）。
		switch {
		case current.Generation > core.TargetGeneration:
			// 机器当前代高于目标代 → Superseded，不执行，不把机器拉回旧代。
			t.Phase = domain.TargetPhaseSuperseded
			t.Reason = domain.SupersededByNewerGeneration
			t.EffectiveGeneration = current.Generation
			t.UpdatedAt = c.now()
			_ = c.targets.Update(ctx, name, *t)
			c.log.Info("deployment target superseded", "event", "deployment_target_superseded",
				"deployment", name, "machine_id", machine,
				"target_generation", core.TargetGeneration, "current_generation", current.Generation)
			return targetBlocked
		case current.Generation < core.TargetGeneration:
			return targetWait // 正常落后，等待收敛
		}
	}

	op, err := c.reconcile.ReconcileGeneration(ctx, machine, effectiveGen, domain.OperationTypeReconcile)
	if err != nil {
		if errors.Is(err, domain.ErrMachineBusy) {
			t.Phase = domain.TargetPhaseBlocked
			t.Reason = "MachineBusy"
			t.UpdatedAt = c.now()
			_ = c.targets.Update(ctx, name, *t)
			return targetBlocked
		}
		if errors.Is(err, domain.ErrAgentDisconnected) {
			// 不可达目标保持 pending 并呈现原因（§4.4 目标解析）。
			t.Phase = domain.TargetPhaseBlocked
			t.Reason = domain.ReasonAgentDisconnected
			t.UpdatedAt = c.now()
			_ = c.targets.Update(ctx, name, *t)
			return targetBlocked
		}
		c.log.Error("dispatch deployment target failed", "deployment", name,
			"machine_id", machine, "err", err)
		return targetBlocked
	}
	t.Phase = domain.TargetPhaseRunning
	t.Reason = ""
	t.OperationID = op.Metadata.Name
	t.EffectiveGeneration = effectiveGen
	t.UpdatedAt = c.now()
	_ = c.targets.Update(ctx, name, *t)
	return targetDispatched
}

// harvest 收割在途目标的操作终态并评估门禁（§4.4 四条件因果绑定）。
func (c *Controller) harvest(ctx context.Context, name string, targets []domain.DeploymentTargetStatus) error {
	for _, t := range targets {
		if t.Phase != domain.TargetPhaseRunning || t.OperationID == "" {
			continue
		}
		op, err := c.ops.Get(ctx, t.OperationID)
		if err != nil {
			continue
		}
		if !domain.IsTerminal(op.Status.Phase) {
			continue // 仍在途（含 Unknown/CancelRequested：阻塞等待，FR-15.4）
		}
		gate := c.evaluateGate(ctx, t, op)
		now := c.now()
		if gate.Passed {
			t.Phase = domain.TargetPhaseSucceeded
			t.Reason = ""
		} else {
			t.Phase = domain.TargetPhaseFailed
			t.Reason = fmt.Sprintf("gate condition %d failed: %s", gate.FailedCondition, gate.Reason)
		}
		t.UpdatedAt = now
		if err := c.targets.Update(ctx, name, t); err != nil {
			return err
		}
		c.log.Info("deployment target gate evaluated", "event", "deployment_gate",
			"deployment", name, "machine_id", t.Machine, "operation_id", t.OperationID,
			"passed", gate.Passed, "failed_condition", gate.FailedCondition, "reason", gate.Reason)
	}
	return nil
}

// evaluateGate 组装门禁输入（§4.4）：操作记录 + 与该操作绑定的 apply 后观测。
func (c *Controller) evaluateGate(ctx context.Context, t domain.DeploymentTargetStatus, op *domain.Operation) GateResult {
	in := GateInput{MachineID: t.Machine, TargetGeneration: t.EffectiveGeneration, Op: op}
	// 门禁条件 2/3/4 的证据只接受与该操作绑定的观测（§4.4）：周期 inventory
	// 会更新"每机最新观测"主行，故不得用 Latest（它可能已被更高 seq 的
	// 周期报文覆盖）。
	if rec, err := c.observed.LatestBound(ctx, t.Machine, op.Metadata.Name); err == nil {
		var obs domain.ObservedState
		if json.Unmarshal(rec.Payload, &obs) == nil {
			in.PostApply = &BoundObservation{
				OperationID:              rec.OperationID,
				InventorySeq:             rec.InventorySeq,
				ObservedProjectionDigest: obs.ObservedProjectionDigest,
				DesiredProjectionDigest:  obs.DesiredProjectionDigest,
				CanonicalizationVersion:  obs.CanonicalizationVersion,
				AdapterHealth:            healthFromObservation(obs),
			}
		}
	}
	return EvaluateGate(in)
}

// healthFromObservation 取同一份观测携带的适配器健康结果（§4.4 条件 4）。
// 适配器未上报健康时返回空串，门禁退回该操作的 verify 证据（两者都缺即不通过）。
func healthFromObservation(obs domain.ObservedState) string {
	return obs.AdapterHealth
}

func (c *Controller) finish(ctx context.Context, name string, core domain.DeploymentCoreSpec,
	succeeded, failed, superseded int) error {
	phase := domain.DeploymentPhaseSucceeded
	reason := ""
	switch {
	case superseded == succeeded+failed+superseded && superseded > 0 && succeeded == 0:
		// 所有目标都被 Superseded → Failed(SupersededByNewerGeneration)（§4.4）。
		phase = domain.DeploymentPhaseFailed
		reason = domain.SupersededByNewerGeneration
	case failed > 0:
		phase = domain.DeploymentPhaseFailed
		reason = "one or more targets failed"
	case succeeded == 0:
		phase = domain.DeploymentPhaseFailed
		reason = "no target succeeded"
	}
	c.writeStatus(ctx, name, domain.DeploymentStatus{Phase: phase, Reason: reason})
	c.log.Info("deployment finished", "event", "deployment_finished", "deployment", name,
		"phase", phase, "reason", reason, "succeeded", succeeded, "failed", failed,
		"superseded", superseded)
	return nil
}

// nextBatchSize 计算本步可派发数量（canary 批优先，其后 batchSize；默认逐台）。
func (c *Controller) nextBatchSize(core domain.DeploymentCoreSpec, phase string, pending, running int) int {
	maxUnavailable := core.Strategy.MaxUnavailable
	if maxUnavailable <= 0 {
		maxUnavailable = 1
	}
	slot := maxUnavailable - running
	if slot <= 0 {
		return 0
	}
	size := core.Strategy.BatchSize
	if size <= 0 {
		size = 1
	}
	if phase == domain.DeploymentPhasePending && core.Strategy.Canary > 0 {
		size = core.Strategy.Canary
	}
	if size > slot {
		size = slot
	}
	if size > pending {
		size = pending
	}
	return size
}

// bumpPhaseIfWaiting 在无在途但仍有阻塞/等待目标时推进展示状态（Pending → Canary）。
func (c *Controller) bumpPhaseIfWaiting(ctx context.Context, name string, status domain.DeploymentStatus,
	core domain.DeploymentCoreSpec, pending []*domain.DeploymentTargetStatus) {
	if status.Phase == domain.DeploymentPhasePending {
		c.writeStatus(ctx, name, domain.DeploymentStatus{Phase: domain.DeploymentPhaseCanary})
	}
}

func (c *Controller) writeStatus(ctx context.Context, name string, st domain.DeploymentStatus) {
	dep, err := c.deploys.Get(ctx, name)
	if err != nil {
		return
	}
	targets, err := c.targets.List(ctx, name)
	if err == nil {
		st.Targets = targets
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	dep.SetStatusJSON(b)
	if err := c.deploys.Update(ctx, dep); err != nil {
		c.log.Error("write deployment status failed", "deployment", name, "err", err)
	}
}

// RunLoop 周期推进全部活跃 Deployment（控制面重启后从持久化批进度继续，§4.4）。
func (c *Controller) RunLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 2 * time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deps, err := c.deploys.List(ctx)
			if err != nil {
				c.log.Error("deployment loop list failed", "err", err)
				continue
			}
			for _, d := range deps {
				if err := c.Advance(ctx, d.Metadata.Name); err != nil {
					c.log.Error("deployment advance failed", "deployment", d.Metadata.Name, "err", err)
				}
			}
		}
	}
}

func parseStatus(raw json.RawMessage) (domain.DeploymentStatus, error) {
	var st domain.DeploymentStatus
	if len(raw) == 0 {
		st.Phase = domain.DeploymentPhasePending
		return st, nil
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, fmt.Errorf("deployment: parse status: %w", err)
	}
	if st.Phase == "" {
		st.Phase = domain.DeploymentPhasePending
	}
	return st, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
