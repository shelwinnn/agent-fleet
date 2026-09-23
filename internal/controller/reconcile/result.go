// 结果处理（架构 v1.1.2 §4.4/§9.7/FR-9.10/FR-15.4）：幂等去重、迟到结果代绑定、
// verify 证据 → 条件写入。对当前代求值（§4.2 前提 1），不按操作携带的旧代写条件。
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// OperationResultMsg 是节点上报的终态结果（经 grpcagent 从 proto 转换）。
type OperationResultMsg struct {
	OperationID      string
	Phase            string // Succeeded | Failed
	Reason           string
	Message          string
	TerminalModifier string // 节点侧取消终结时携带 Cancelled
	Verify           *domain.VerifyEvidence
	StartedAt        time.Time
	FinishedAt       time.Time
}

// ResultOutcome 描述结果处理结果（Ack 语义：节点据 Ack 淘汰 outbox 项）。
type ResultOutcome struct {
	// Duplicate 表示该 operationId 已终结，本次为重复投递（幂等去重，FR-13.3）。
	Duplicate bool
	// Superseded 表示成功结果因落后于机器当前代被记为 Succeeded(Superseded)。
	Superseded bool
}

// OnStarted 处理 OperationStarted：Pending → Running（幂等；CancelRequested
// 期间到达的 started 不回退取消状态）。
func (c *Controller) OnStarted(ctx context.Context, machine, opID string) error {
	err := c.ops.Transition(ctx, opID, domain.OperationPhasePending, domain.OperationPhaseRunning,
		func(o *domain.Operation) error {
			now := c.now()
			o.Status.StartedAt = &now
			return nil
		})
	if errors.Is(err, domain.ErrOpState) {
		return nil // 已 Running/CancelRequested 等：幂等忽略
	}
	if err != nil {
		return err
	}
	c.setUnresolvedRefPhase(ctx, machine, opID, domain.OperationPhaseRunning)
	c.log.Info("operation started", "event", "operation_started",
		"operation_id", opID, "machine_id", machine)
	return nil
}

// OnProgress 记录步骤审计（operation_steps，§6.4 steps；输出限长脱敏）。
func (c *Controller) OnProgress(ctx context.Context, machine, opID, step, phase, message string) error {
	steps, err := c.ops.ListSteps(ctx, opID)
	if err != nil {
		return err
	}
	const maxSteps = 64
	if len(steps) >= maxSteps {
		steps = steps[len(steps)-maxSteps+1:]
	}
	out := message
	if len(out) > 512 {
		out = out[:512] // 限长（§10.1 steps 含限长脱敏输出）
	}
	steps = append(steps, domain.OperationStepRecord{
		Seq: int64(len(steps) + 1), Name: step, Phase: phase, Output: out,
		RecordedAt: c.now(),
	})
	return c.ops.ReplaceSteps(ctx, opID, steps)
}

// OnResult 幂等处理终态结果（spec §11.5：按 operationId 去重）。核心语义：
//   - 已终态 → Duplicate（不产生第二条变更链，FR-13.8/T14）；
//   - 成功但 desiredGeneration < 机器当前代 → Succeeded(Superseded)（FR-9.10/
//     T3）：保留历史事实，不写 Reconciled=True，不把机器拉回旧代；
//   - 恢复冲突 → Degraded=True（§14.2）；
//   - 成功且对当前代、证据齐备 → 按摘要相等写 Drifted=False + Reconciled=True。
func (c *Controller) OnResult(ctx context.Context, machine string, res OperationResultMsg) (ResultOutcome, error) {
	// 上报相位只允许两个终态（§6.4）。畸形相位（""/"foo"/未决相位）若直接写库，
	// 该行会既不在终态集合、也不在未决集合：部分唯一索引不再命中（机器级互斥被
	// 静默释放，而节点可能仍在写），harvest 又因"非终态"永远不收割该目标。
	if res.Phase != domain.OperationPhaseSucceeded && res.Phase != domain.OperationPhaseFailed {
		return ResultOutcome{}, fmt.Errorf("%w: result phase %q is not a terminal phase (Succeeded|Failed)",
			domain.ErrInvalid, res.Phase)
	}
	op, err := c.ops.Get(ctx, res.OperationID)
	if err != nil {
		return ResultOutcome{}, err
	}
	if op.Spec.Machine != machine {
		return ResultOutcome{}, fmt.Errorf("operation %s does not belong to machine %q",
			res.OperationID, machine)
	}
	if domain.IsTerminal(op.Status.Phase) {
		// 幂等去重：重复结果只回 Ack，不改任何状态（同 operationId 重放不产生
		// 第二条变更链）。
		c.log.Info("duplicate operation result ignored", "event", "operation_result_duplicate",
			"operation_id", res.OperationID, "machine_id", machine, "stored_phase", op.Status.Phase)
		return ResultOutcome{Duplicate: true}, nil
	}

	currentGen := int64(0)
	if snap, err := c.snapshots.Current(ctx, machine); err == nil {
		currentGen = snap.Generation
	}
	superseded := res.Phase == domain.OperationPhaseSucceeded &&
		res.TerminalModifier == "" &&
		op.Spec.DesiredGeneration < currentGen

	modifier := res.TerminalModifier
	if superseded {
		modifier = domain.ModifierSuperseded
	}
	err = c.ops.Transition(ctx, res.OperationID, "", res.Phase, func(o *domain.Operation) error {
		fin := res.FinishedAt
		if fin.IsZero() {
			fin = c.now()
		}
		o.Status.FinishedAt = &fin
		o.Status.TerminalModifier = modifier
		o.Status.Verify = res.Verify
		return nil
	})
	if errors.Is(err, domain.ErrOpState) {
		// 并发下的第二次终态迁移（如超时扫描先到）：按重复处理。
		return ResultOutcome{Duplicate: true}, nil
	}
	if err != nil {
		return ResultOutcome{}, err
	}
	c.clearUnresolvedRef(ctx, machine)

	logAttrs := []any{"event", "operation_result", "operation_id", res.OperationID,
		"machine_id", machine, "phase", res.Phase, "reason", res.Reason,
		"desired_generation", op.Spec.DesiredGeneration, "current_generation", currentGen}
	if modifier != "" {
		logAttrs = append(logAttrs, "terminal_modifier", modifier)
	}
	c.log.Info("operation finished", logAttrs...)

	// 条件写入：对机器**当前代**求值（§4.2 前提 1），不按操作的旧代写。
	switch {
	case superseded:
		// 迟到成功：不写 Reconciled=True；当前代尚无对应观测 → 触发按当前代
		// 重新求值（将得到 Unknown(ObservationPredatesDesired)，§9.7）。
		_, _ = c.EvaluateDrift(ctx, machine)
	case res.Phase == domain.OperationPhaseSucceeded && modifier == domain.ModifierSkipped:
		// 跳过不计入成功：不写 Reconciled（§9.6 路径 C）。
	case res.Phase == domain.OperationPhaseSucceeded:
		c.writeSuccessConditions(ctx, machine, op, res.Verify, currentGen)
	case res.Reason == domain.ReasonRestoreConflict:
		// 恢复也失败 → Degraded=True 并停止向该机继续自动 rollout（§14.2）。
		c.writeCondition(ctx, machine, domain.ConditionDegraded, domain.ConditionTrue,
			domain.ReasonRestoreConflict, "restore after failure also failed", c.now())
		_, _ = c.EvaluateDrift(ctx, machine)
	default:
		_, _ = c.EvaluateDrift(ctx, machine)
	}
	return ResultOutcome{Superseded: superseded}, nil
}

// writeSuccessConditions 依据 verify 证据写 Drifted/Reconciled（§4.2 条件表）。
// Reconciled=True 的 §6.2 语义：一次针对当前代的操作成功且其 apply 后观测证明一致。
func (c *Controller) writeSuccessConditions(ctx context.Context, machine string,
	op *domain.Operation, verify *domain.VerifyEvidence, currentGen int64) {
	now := c.now()
	if op.Spec.DesiredGeneration != currentGen {
		// 非 当前代成功（理论上已被 superseded 分支捕获）；防御性重新求值。
		_, _ = c.EvaluateDrift(ctx, machine)
		return
	}
	if verify == nil {
		// 无证据不宣称收敛（门禁条件 3 不成立；保持观测求值结果）。
		c.log.Warn("succeeded operation lacks verify evidence", "operation_id", op.Metadata.Name)
		_, _ = c.EvaluateDrift(ctx, machine)
		return
	}
	if verify.DesiredProjectionDigest == verify.ObservedProjectionDigest {
		c.writeDriftConditions(ctx, machine, domain.ConditionFalse, "",
			"managed projection matches desired (operation evidence)", domain.ConditionTrue,
			"", "", verify.DesiredProjectionDigest, verify.ObservedProjectionDigest,
			verify.CanonicalizationVersion, now)
		return
	}
	c.writeDriftConditions(ctx, machine, domain.ConditionTrue, "",
		"post-apply observation differs from desired", domain.ConditionFalse,
		"", "", verify.DesiredProjectionDigest, verify.ObservedProjectionDigest,
		verify.CanonicalizationVersion, now)
}

// EvaluateDrift 按"机器当前期望快照 + 最新观测"求值 drift 三态（§4.2/§6.2/FR-8.6）。
// 权威判据唯一：desiredProjectionDigest == observedProjectionDigest 且版本一致；
// 逐字段 diff 只用于展示。三态：一致(False) / 未知或过期(Unknown) / 漂移(True)。
func (c *Controller) EvaluateDrift(ctx context.Context, machine string) (*domain.DriftEvaluation, error) {
	snap, err := c.snapshots.Current(ctx, machine)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil // 尚无期望代：drift 未定义，不写条件
	}
	if err != nil {
		return nil, err
	}
	rec, err := c.observed.Latest(ctx, machine)
	if errors.Is(err, domain.ErrNotFound) {
		c.writeUnknownDrift(ctx, machine, domain.DriftReasonNeverInventoried,
			"no observation has ever been collected")
		return &domain.DriftEvaluation{DriftStatus: domain.ConditionUnknown,
			DriftReason: domain.DriftReasonNeverInventoried}, nil
	}
	if err != nil {
		return nil, err
	}
	var obs domain.ObservedState
	if err := json.Unmarshal(rec.Payload, &obs); err != nil {
		return nil, fmt.Errorf("evaluate drift: parse observation: %w", err)
	}

	// 期望代已变但尚未按新代采集（§4.2 条件表/T3）。
	if rec.ObservedGeneration < snap.Generation {
		c.writeUnknownDrift(ctx, machine, domain.DriftReasonObservationPredatesDesir,
			fmt.Sprintf("observation is for generation %d, current desired is %d",
				rec.ObservedGeneration, snap.Generation))
		return &domain.DriftEvaluation{DriftStatus: domain.ConditionUnknown,
			DriftReason: domain.DriftReasonObservationPredatesDesir}, nil
	}

	// 投影契约不可比（§7.1/T17）：不产生 drift 判定，置 Unknown。
	if obs.DesiredProjectionDigest == "" || obs.ObservedProjectionDigest == "" {
		c.writeUnknownDrift(ctx, machine, domain.ProjectionVersionMismatch,
			"observation carries no projection digests (agentd without managed projection)")
		return &domain.DriftEvaluation{DriftStatus: domain.ConditionUnknown,
			DriftReason: domain.ProjectionVersionMismatch}, nil
	}
	if st, err := domain.ParseMachineStatus(c.statusSnapshot(ctx, machine)); err == nil &&
		st.CanonicalizationVersion != "" && obs.CanonicalizationVersion != "" &&
		st.CanonicalizationVersion != obs.CanonicalizationVersion {
		c.writeUnknownDrift(ctx, machine, domain.ProjectionVersionMismatch,
			fmt.Sprintf("projection canonicalization changed: stored=%s observed=%s; agentd upgrade required",
				st.CanonicalizationVersion, obs.CanonicalizationVersion))
		return &domain.DriftEvaluation{DriftStatus: domain.ConditionUnknown,
			DriftReason: domain.ProjectionVersionMismatch}, nil
	}

	now := c.now()
	switch {
	case obs.DesiredProjectionDigest == obs.ObservedProjectionDigest:
		// §6.2 Reconciled=True 需要操作证据：绑定当前代的成功操作 + 其 apply 后观测。
		opConverged := false
		if rec.OperationID != "" {
			if op, err := c.ops.Get(ctx, rec.OperationID); err == nil &&
				op.Status.Phase == domain.OperationPhaseSucceeded &&
				op.Status.TerminalModifier == "" &&
				op.Spec.DesiredGeneration == snap.Generation {
				opConverged = true
			}
		}
		reconciled := domain.ConditionFalse
		reason := ""
		msg := "consistent without a reconciling operation"
		if opConverged {
			reconciled = domain.ConditionTrue
			reason, msg = "", "operation for current generation converged and observation proves consistency"
		} else if cur, err := domain.ParseMachineStatus(c.statusSnapshot(ctx, machine)); err != nil {
			return nil, err
		} else if rc, ok := cur.GetCondition(domain.ConditionReconciled); ok && rc.Status == domain.ConditionTrue {
			// 仍一致：保留既有 True（避免无操作证据时翻转抖动）。
			reconciled = domain.ConditionTrue
			reason, msg = "", "still consistent since last reconciling operation"
		}
		c.writeDriftConditions(ctx, machine, domain.ConditionFalse, "", "", reconciled, reason, msg,
			obs.DesiredProjectionDigest, obs.ObservedProjectionDigest, obs.CanonicalizationVersion, now)
		return &domain.DriftEvaluation{DriftStatus: domain.ConditionFalse,
			ReconciledStatus: reconciled}, nil
	default:
		c.writeDriftConditions(ctx, machine, domain.ConditionTrue, "",
			"observed managed projection differs from desired", domain.ConditionFalse, "",
			"managed state drifted", obs.DesiredProjectionDigest, obs.ObservedProjectionDigest,
			obs.CanonicalizationVersion, now)
		return &domain.DriftEvaluation{DriftStatus: domain.ConditionTrue,
			ReconciledStatus:         domain.ConditionFalse,
			DesiredProjectionDigest:  obs.DesiredProjectionDigest,
			ObservedProjectionDigest: obs.ObservedProjectionDigest,
			CanonicalizationVersion:  obs.CanonicalizationVersion}, nil
	}
}

// ScanFreshness 是新鲜度扫描（§6.2 契约：超出 driftFreshnessWindow 且当前无
// 未决操作时，Drifted/Reconciled 置 Unknown(StaleObservation)，保留
// lastInventoryAt 供 UI 展示"上次确认于 T"）。
func (c *Controller) ScanFreshness(ctx context.Context, now time.Time) error {
	machines, err := c.machines.List(ctx)
	if err != nil {
		return err
	}
	for _, m := range machines {
		st, err := domain.ParseMachineStatus(m.StatusJSON())
		if err != nil {
			continue
		}
		drift, ok := st.GetCondition(domain.ConditionDrifted)
		if !ok || drift.Status == domain.ConditionUnknown {
			continue // 已是三态 Unknown 或从未求值
		}
		if st.LastInventoryAt == nil || now.Sub(*st.LastInventoryAt) <= c.cfg.FreshnessWindow {
			continue
		}
		if ref := st.UnresolvedOperation; ref != nil {
			continue // §6.2：存在未决操作时不按新鲜度改写
		}
		c.writeUnknownDrift(ctx, m.Metadata.Name, domain.DriftReasonStaleObservation,
			fmt.Sprintf("last observation at %s exceeded freshness window %s",
				st.LastInventoryAt.Format(time.RFC3339), c.cfg.FreshnessWindow))
	}
	return nil
}

// AssignObservationGeneration 为入库前的观测做因果归属代标注（§4.4/FR-8.7）：
// 携带 operationId 的观测按**该操作的目标代**归属（该观测是那次 apply 后采集的
// 因果证据）；周期观测按存储时的当前代归属。实际落库与高水位检查仍由 Machine
// 控制器的 OnInventory 完成（单一写方）。
func (c *Controller) AssignObservationGeneration(ctx context.Context, rec *domain.ObservedStateRecord) {
	if rec.OperationID != "" {
		if op, err := c.ops.Get(ctx, rec.OperationID); err == nil && op.Spec.DesiredGeneration > 0 {
			rec.ObservedGeneration = op.Spec.DesiredGeneration
			return
		}
	}
	if snap, err := c.snapshots.Current(ctx, rec.Machine); err == nil {
		rec.ObservedGeneration = snap.Generation
	}
}

// ---- 条件写入辅助（Drifted/Reconciled/Degraded 的置位方是本控制器，§6.2）----

func (c *Controller) writeUnknownDrift(ctx context.Context, machine, reason, message string) {
	now := c.now()
	err := c.status.UpdateStatus(ctx, machine, func(st *domain.MachineStatus) error {
		st.SetCondition(domain.ConditionDrifted, domain.ConditionUnknown, reason, message, now)
		st.SetCondition(domain.ConditionReconciled, domain.ConditionUnknown, reason, message, now)
		return nil
	})
	if err != nil {
		c.log.Error("write unknown drift failed", "machine_id", machine, "reason", reason, "err", err)
	}
}

func (c *Controller) writeDriftConditions(ctx context.Context, machine string,
	driftStatus, driftReason, driftMsg, reconciledStatus, reconciledReason, reconciledMsg,
	desiredDigest, observedDigest, canonVersion string, evalTime time.Time) {
	err := c.status.UpdateStatus(ctx, machine, func(st *domain.MachineStatus) error {
		st.SetCondition(domain.ConditionDrifted, driftStatus, driftReason, driftMsg, evalTime)
		st.SetCondition(domain.ConditionReconciled, reconciledStatus, reconciledReason, reconciledMsg, evalTime)
		st.DesiredProjectionDigest = desiredDigest
		st.ObservedProjectionDigest = observedDigest
		st.CanonicalizationVersion = canonVersion
		return nil
	})
	if err != nil {
		c.log.Error("write drift conditions failed", "machine_id", machine, "err", err)
	}
}

func (c *Controller) writeCondition(ctx context.Context, machine, condType, status, reason, message string, at time.Time) {
	err := c.status.UpdateStatus(ctx, machine, func(st *domain.MachineStatus) error {
		st.SetCondition(condType, status, reason, message, at)
		return nil
	})
	if err != nil {
		c.log.Error("write condition failed", "machine_id", machine, "condition", condType, "err", err)
	}
}

func (c *Controller) statusSnapshot(ctx context.Context, machine string) json.RawMessage {
	m, err := c.machines.Get(ctx, machine)
	if err != nil {
		return nil
	}
	return m.StatusJSON()
}
