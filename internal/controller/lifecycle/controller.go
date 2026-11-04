// Package lifecycle 是生命周期操作的执行编排控制器（KM-49；T2 落地的模型与
// 契约的唯一消费方）。职责边界：
//
//   - 操作生命周期管理：幂等创建（创建时固定精确版本 + 能力判定快照 + 台账
//     平台事实回填）、确认、仅失败目标重试（retryOf 追溯链）、查询；
//   - 派发编排：已确认操作经节点通道下发；提交前离线标记 not_executed 且
//     上线后不自动执行旧意图（状态机无出边）；派发不可达同理；
//   - 结果回灌：逐目标幂等落库（证据先脱敏限长），未知不计成功、不自动重试；
//   - 超时扫描：running 超过派发 deadline + 宽限仍无结果 → unknown
//    （timeout-no-result）——结果丢失按 spec 显示未知并先核对；
//   - CheckUpdate：只读上游（T3 发现服务），逐目标记录四态，不经节点通道。
//
// 停止管理（unmanaged）只存在于意图文档（T2），不是操作类型：本控制器没有
// 任何把 unmanaged 变成卸载派发的路径（有测试锁定）。
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/ids"
	"github.com/shelwinnn/agent-fleet/internal/upstream"
)

// DefaultOpTimeout 是派发到节点的单操作超时上限（与 agentlocal/lifecycle 的
// DefaultOpTimeout 同源：外部执行必须有界）。
const DefaultOpTimeout = 10 * time.Minute

// DefaultTimeoutGrace 是超时扫描宽限：节点自身会在 deadline 处以失败收场，
// 结果回传需要时间；宽限期内不把 running 改判 unknown，避免与迟到结果竞态。
const DefaultTimeoutGrace = 2 * time.Minute

// Dispatcher 是控制面 → 节点的生命周期下行派发通道（grpcagent.Server 实现，
// 经活跃 Connect 流下发；与 reconcile.Dispatcher 同一双接线模式）。
type Dispatcher interface {
	DispatchLifecycle(ctx context.Context, machine string, req DispatchRequest) error
}

// DispatchRequest 是一次派发的自包含载荷（proto ExecuteLifecycleOperation 的
// 无协议依赖形态；固定版本意图 + 重发现标记 + 超时）。
type DispatchRequest struct {
	OpID           string
	Type           string
	IdempotencyKey string
	Version        string
	CreatedBy      string
	// RediscoveryRequired：重试链上尚无"影响变化→重新确认"时为 true（节点先
	// 重新发现实际状态）；链上任一环已经操作者重新确认后，后续重试为 false
	// ——同一现实不得再次阻断执行（见 chainHasReconfirmation）。
	RediscoveryRequired bool
	// Deadline 是整单超时（控制面时钟）；节点据此给每个目标设 context 上限。
	Deadline time.Time
	// Targets 是本次派发给该机器的 pending 目标（固定版本意图不变）。
	Targets []domain.LifecycleTarget
}

// ResultMsg 是节点上报的逐目标结果（grpcagent 从 proto 转换）。
type ResultMsg struct {
	Machine        string
	OpID           string
	IdempotencyKey string
	Results        []domain.LifecycleTargetResult
	// NeedsReconfirmation：节点重发现发现影响变化（实际状态与固定版本意图
	// 前提不一致）；置位后本操作不得继续派发，直到操作者重新确认。
	NeedsReconfirmation bool
	ReconfirmReason     string
	FinishedAt          time.Time
}

// UpstreamDiscovery 是对 T3 上游版本发现服务的窄依赖（CheckUpdate 只读上游）。
type UpstreamDiscovery interface {
	Check(ctx context.Context, agentID string) (upstream.Report, error)
}

// Config 是控制器装配参数（零值取 Default*）。
type Config struct {
	OpTimeout     time.Duration
	TimeoutGrace  time.Duration
	Now           func() time.Time
	UpstreamQuery UpstreamDiscovery
}

// Controller 是生命周期执行编排控制器。
type Controller struct {
	repo     domain.LifecycleRepository
	machines domain.MachineRepository
	observed domain.ObservedStateRepository
	dispatch Dispatcher
	cfg      Config
	log      *slog.Logger
}

// New 装配控制器。dispatcher 可以后置接线（SetDispatcher，与 reconcile 同模
// ——main 装配打破构造环）。
func New(repo domain.LifecycleRepository, machines domain.MachineRepository,
	observed domain.ObservedStateRepository, cfg Config, log *slog.Logger) *Controller {
	if cfg.OpTimeout <= 0 {
		cfg.OpTimeout = DefaultOpTimeout
	}
	if cfg.TimeoutGrace <= 0 {
		cfg.TimeoutGrace = DefaultTimeoutGrace
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Controller{repo: repo, machines: machines, observed: observed, cfg: cfg, log: log}
}

// SetDispatcher 装配下行派发通道（main 装配：grpcagent.Server）。nil 时派发
// 扫描不执行（REST 创建/查询仍可用）。
func (c *Controller) SetDispatcher(d Dispatcher) { c.dispatch = d }

// ---- 操作生命周期（httpapi 的窄接口见 httpapi.LifecycleAPI）----

// CreateOperation 以幂等键创建持久操作。目标机器必须已在台账；OS/Arch 缺省时
// 从最新观测回填（创建时的平台事实快照）；能力判定由仓储层快照进目标
//（domain.SnapshotCapability），阻塞组合在创建时即被拒绝（ErrUnsupportedCombo）。
func (c *Controller) CreateOperation(ctx context.Context, spec domain.LifecycleOperationSpec) (*domain.LifecycleOperation, bool, error) {
	if spec.IdempotencyKey == "" {
		return nil, false, fmt.Errorf("%w: idempotencyKey required", domain.ErrInvalid)
	}
	targets, err := c.prepareTargets(ctx, spec.Targets)
	if err != nil {
		return nil, false, err
	}
	spec.Targets = targets
	op := &domain.LifecycleOperation{
		Metadata: domain.ObjectMeta{UID: ids.NewUID(), Name: ids.NewUID()},
		Spec:     spec,
	}
	return c.repo.CreateOperation(ctx, op)
}

// GetOperation 按 id 查询操作（逐目标状态与证据）。
// prepareTargets 是两条创建路径（首建与重试）共用的目标准备：归一化、机器
// 必须已在台账、OS/Arch 缺省时从最新观测回填（创建时的平台事实快照）。
func (c *Controller) prepareTargets(ctx context.Context, in []domain.LifecycleTarget) ([]domain.LifecycleTarget, error) {
	targets := make([]domain.LifecycleTarget, len(in))
	for i, t := range in {
		t = t.Normalize()
		if _, err := c.machines.Get(ctx, t.Machine); err != nil {
			return nil, fmt.Errorf("target machine %s: %w", t.Machine, err)
		}
		if t.OS == "" || t.Arch == "" {
			if obs, err := c.observed.Latest(ctx, t.Machine); err == nil && obs != nil {
				var payload domain.ObservedState
				if json.Unmarshal(obs.Payload, &payload) == nil && payload.Machine != nil {
					if t.OS == "" {
						t.OS = payload.Machine.OS
					}
					if t.Arch == "" {
						t.Arch = payload.Machine.Arch
					}
				}
			}
		}
		targets[i] = t
	}
	return targets, nil
}

func (c *Controller) GetOperation(ctx context.Context, id string) (*domain.LifecycleOperation, error) {
	return c.repo.GetOperation(ctx, id)
}

// Confirm 记录操作者确认时刻（未确认的操作不得派发；影响变化置位后须经
// 重新确认才能派发——MarkConfirmed 同时清除标记）。
func (c *Controller) Confirm(ctx context.Context, id string, at time.Time) error {
	if _, err := c.repo.GetOperation(ctx, id); err != nil {
		return err
	}
	if at.IsZero() {
		at = c.cfg.Now()
	}
	return c.repo.MarkConfirmed(ctx, id, at.UTC())
}

// Retry 创建重试操作：仅列原操作中确定失败的目标；类型与固定版本继承原操作
//（改意图必须新建操作）；retryOf 关联追溯链。同一已批准意图的重试不重复确认
// ——继承原操作确认时刻。幂等键服务端生成（重试语义已由追溯校验防重）。
func (c *Controller) Retry(ctx context.Context, originalID string, targets []domain.LifecycleTarget, createdBy string) (*domain.LifecycleOperation, error) {
	orig, err := c.repo.GetOperation(ctx, originalID)
	if err != nil {
		return nil, err
	}
	norm, err := c.prepareTargets(ctx, targets)
	if err != nil {
		return nil, err
	}
	op := &domain.LifecycleOperation{
		Metadata: domain.ObjectMeta{UID: ids.NewUID(), Name: ids.NewUID()},
		Spec: domain.LifecycleOperationSpec{
			Type:           orig.Spec.Type,
			IdempotencyKey: "retry-" + ids.NewUID(),
			Version:        orig.Spec.Version,
			Targets:        norm,
			CreatedBy:      createdBy,
		},
		RetryOf: originalID,
	}
	// 同一已批准操作的重试不重复确认：继承原确认时刻（同时也让派发侧的
	// 重发现标记保持 true——链上尚无重新确认时不得豁免，见 chainHasReconfirmation）。
	if orig.Status.ConfirmedAt != nil {
		op.Status.ConfirmedAt = orig.Status.ConfirmedAt
	}
	stored, _, err := c.repo.CreateOperation(ctx, op)
	return stored, err
}

// GetDesired 返回机器当前意图文档；从未纳管返回 ErrNotFound（显式可查询状态，
// 不是 absent）。
func (c *Controller) GetDesired(ctx context.Context, machine string) (*domain.LifecycleDesiredRecord, error) {
	return c.repo.CurrentDesired(ctx, machine)
}

// PutDesired 以合并语义更新意图（未提及的 agent 原样保留；解除纳管只能显式
// unmanaged——省略绝不构成卸载，MergeLifecycleDesired 是唯一合并入口）。
func (c *Controller) PutDesired(ctx context.Context, machine string, update domain.LifecycleDesired) (*domain.LifecycleDesiredRecord, bool, error) {
	return c.repo.PutDesired(ctx, machine, update)
}

// Capabilities 返回能力矩阵快照（KM-46 §7 文档数据）。
func (c *Controller) Capabilities() []domain.LifecycleCapability {
	return domain.LifecycleCapabilities()
}

// ---- 派发编排与扫描 ----

// RunLoop 是周期编排循环：派发扫描 + CheckUpdate 推进 + 超时扫描。
func (c *Controller) RunLoop(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.ScanOnce(ctx); err != nil {
				c.log.Error("lifecycle scan failed", "event", "lifecycle_scan_error", "err", err)
			}
		}
	}
}

// ScanOnce 执行一轮编排：CheckUpdate（只读、无需确认）→ 派发（已确认操作）→
// 超时扫描。单轮内错误逐操作隔离（一个操作的失败不阻断其余）。
func (c *Controller) ScanOnce(ctx context.Context) error {
	ops, err := c.repo.ListUnfinishedOperations(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, op := range ops {
		var err error
		switch op.Spec.Type {
		case domain.LifecycleOpCheckUpdate:
			err = c.advanceCheckUpdate(ctx, op)
		case domain.LifecycleOpInstall, domain.LifecycleOpUpgrade, domain.LifecycleOpUninstall:
			err = c.dispatchPending(ctx, op)
		default:
			err = fmt.Errorf("unknown op type %q", op.Spec.Type)
		}
		if err != nil {
			c.log.Error("lifecycle op scan step failed", "event", "lifecycle_op_step_error", "op_id", op.Metadata.Name,
				"type", op.Spec.Type, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if err := c.scanTimeouts(ctx, ops, c.cfg.Now()); err != nil {
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// dispatchPending 把已确认、未受阻操作的 pending 目标派发到各自机器：
//   - 未确认 → 跳过（执行层不得派发未确认意图）；
//   - needsReconfirmation → 跳过（影响变化后必须重新确认）；
//   - 机器离线 → not_executed(target-offline)：提交前离线不积压执行；
//   - 派发成功 → running（证据记录派发时刻供超时扫描）；
//   - 派发不可达（队列满/连接刚断）→ not_executed(dispatch-failed)。
//
// 上线后不自动执行旧意图：not_executed 没有出边，重启扫描不会复活它们。
func (c *Controller) dispatchPending(ctx context.Context, op *domain.LifecycleOperation) error {
	if op.Status.ConfirmedAt == nil || op.Status.NeedsReconfirmation {
		return nil
	}
	if c.dispatch == nil {
		return nil
	}
	pending := map[string][]domain.LifecycleTarget{}
	var order []string
	for _, r := range op.Status.Results {
		if r.State != domain.TargetPending {
			continue
		}
		m := r.Target.Machine
		if _, ok := pending[m]; !ok {
			order = append(order, m)
		}
		pending[m] = append(pending[m], r.Target)
	}
	sort.Strings(order)
	req := DispatchRequest{
		OpID:                op.Metadata.Name,
		Type:                op.Spec.Type,
		IdempotencyKey:      op.Spec.IdempotencyKey,
		Version:             op.Spec.Version,
		CreatedBy:           op.Spec.CreatedBy,
		RediscoveryRequired: op.RetryOf != "" && !c.chainHasReconfirmation(ctx, op),
		Deadline:            c.cfg.Now().Add(c.cfg.OpTimeout),
	}
	for _, machine := range order {
		req.Targets = pending[machine]
		connected, err := c.machineConnected(ctx, machine)
		if err != nil {
			return fmt.Errorf("machine %s: %w", machine, err)
		}
		if !connected {
			c.transitionTargets(ctx, op.Metadata.Name, pending[machine],
				domain.TargetNotExecuted, domain.ReasonTargetOffline,
				"machine is offline at dispatch time; intent is not queued (not_executed has no re-run path: create a new operation once the machine is online)")
			continue
		}
		err = c.dispatch.DispatchLifecycle(ctx, machine, req)
		if err == nil {
			now := c.cfg.Now()
			c.transitionTargets(ctx, op.Metadata.Name, pending[machine],
				domain.TargetRunning, "",
				"dispatched to node", withDispatchEvidence(now))
			c.log.Info("lifecycle op dispatched", "event", "lifecycle_dispatched", "op_id", op.Metadata.Name,
				"machine_id", machine, "targets", len(pending[machine]),
				"rediscovery_required", req.RediscoveryRequired)
			continue
		}
		c.transitionTargets(ctx, op.Metadata.Name, pending[machine],
			domain.TargetNotExecuted, domain.ReasonDispatchFailed,
			fmt.Sprintf("dispatch failed: %v", err))
	}
	return nil
}

// chainHasReconfirmation 报告重试链（含本操作）是否已经发生过"影响变化后的
// 重新确认"。判据锚在**同一时钟域**的控制器写入值上：重试创建时继承父操作
// 的确认时刻，重新确认经 MarkConfirmed 写入更晚时刻——子确认晚于父确认当且
// 仅当发生过重新确认。用途：重试操作的首派必须携带重发现标记（继承的确认
// 不豁免重发现）；而链上任何一环已经历"重发现→影响变化→重新确认"之后，其
// 后续重试（继承重新确认时刻）不再携带——操作者已接受当时的实际状态，同一
// 现实不得再次阻断执行（否则"重发现→标记→确认→重试"永远循环）。
func (c *Controller) chainHasReconfirmation(ctx context.Context, op *domain.LifecycleOperation) bool {
	cur := op
	for cur != nil && cur.RetryOf != "" {
		parent, err := c.repo.GetOperation(ctx, cur.RetryOf)
		if err != nil || parent.Status.ConfirmedAt == nil || cur.Status.ConfirmedAt == nil {
			return false
		}
		if cur.Status.ConfirmedAt.After(*parent.Status.ConfirmedAt) {
			return true
		}
		cur = parent
	}
	return false
}

// machineConnected 查询机器 agentd 连接条件（与 reconcile.channelState 的
// agentd 分支同一判据）。
func (c *Controller) machineConnected(ctx context.Context, machine string) (bool, error) {
	m, err := c.machines.Get(ctx, machine)
	if err != nil {
		return false, err
	}
	st, err := domain.ParseMachineStatus(m.StatusJSON())
	if err != nil {
		return false, err
	}
	cond, ok := st.GetCondition(domain.ConditionAgentConnected)
	return ok && cond.Status == domain.ConditionTrue, nil
}

// advanceCheckUpdate 推进 CheckUpdate 操作（只读上游，不经节点、无需确认）。
// 逐目标按其安装方式取对应来源的查询结果：
//   - 来源查询成功 → succeeded（证据携带上游版本/渠道/查询时间；reason 用
//     verified-present：白名单中最贴近"发现到版本"的核验原因）；
//   - 查询失败（含缓存回退仍在报告内）→ failed(discovery-failed)，失败原因
//     与缓存时间进 message；
//   - 来源不适用该目标平台 / 上游不支持该方式查询 / 未知 agent →
//     failed(unsupported-combo)。
func (c *Controller) advanceCheckUpdate(ctx context.Context, op *domain.LifecycleOperation) error {
	if c.cfg.UpstreamQuery == nil {
		return nil
	}
	for _, r := range op.Status.Results {
		if r.State != domain.TargetPending {
			continue
		}
		t := r.Target
		report, err := c.cfg.UpstreamQuery.Check(ctx, t.Agent)
		if err != nil {
			reason := domain.ReasonOpFailed
			if errors.Is(err, upstream.ErrUnknownAgent) {
				reason = domain.ReasonUnsupportedCombo
			}
			c.transitionTargets(ctx, op.Metadata.Name, []domain.LifecycleTarget{t},
				domain.TargetFailed, reason, truncateMsg(redact(err.Error())))
			continue
		}
		var src *upstream.SourceResult
		for i := range report.Sources {
			if report.Sources[i].Method == t.Method {
				src = &report.Sources[i]
				break
			}
		}
		if src == nil {
			c.transitionTargets(ctx, op.Metadata.Name, []domain.LifecycleTarget{t},
				domain.TargetFailed, domain.ReasonUnsupportedCombo,
				fmt.Sprintf("upstream declares no %s source for agent %s", t.Method, t.Agent))
			continue
		}
		switch src.Status {
		case upstream.StatusOK:
			msg := fmt.Sprintf("channel=%s queriedAt=%s attempts=%d",
				src.Channel, src.QueriedAt.Format(time.RFC3339), src.Attempts)
			ev := &domain.LifecycleEvidence{
				ObservedVersion: src.Version.Raw,
				Method:          src.Method,
				Message:         truncateMsg(msg),
				RecordedAt:      c.cfg.Now(),
			}
			c.transitionTargets(ctx, op.Metadata.Name, []domain.LifecycleTarget{t},
				domain.TargetSucceeded, domain.ReasonVerifiedPresent, msg, withEvidence(ev))
		case upstream.StatusUnsupported:
			detail := ""
			if src.Failure != nil {
				detail = src.Failure.Detail
			}
			c.transitionTargets(ctx, op.Metadata.Name, []domain.LifecycleTarget{t},
				domain.TargetFailed, domain.ReasonUnsupportedCombo, truncateMsg(redact(detail)))
		case upstream.StatusFailed:
			detail := ""
			if src.Failure != nil {
				detail = src.Failure.Detail
			}
			if src.LastSuccess != nil {
				detail += fmt.Sprintf(" (last success %s: %s)",
					src.LastSuccess.QueriedAt.Format(time.RFC3339), src.LastSuccess.Version.Raw)
			}
			c.transitionTargets(ctx, op.Metadata.Name, []domain.LifecycleTarget{t},
				domain.TargetFailed, domain.ReasonDiscoveryFailed, truncateMsg(redact(detail)))
		default:
			c.transitionTargets(ctx, op.Metadata.Name, []domain.LifecycleTarget{t},
				domain.TargetFailed, domain.ReasonDiscoveryFailed,
				truncateMsg(fmt.Sprintf("unknown upstream status %q", src.Status)))
		}
	}
	return nil
}

// scanTimeouts 把 running 超过派发 deadline + 宽限仍无结果的目标改判
// unknown(timeout-no-result)（spec：结果丢失显示未知并先核对；未知不计成功、
// 不自动重试）。派发时刻取 running 证据的 RecordedAt（dispatchPending 写入）。
func (c *Controller) scanTimeouts(ctx context.Context, ops []*domain.LifecycleOperation, now time.Time) error {
	var firstErr error
	for _, op := range ops {
		for _, r := range op.Status.Results {
			if r.State != domain.TargetRunning {
				continue
			}
			dispatchedAt := op.Metadata.CreationTimestamp
			if r.Evidence != nil && !r.Evidence.RecordedAt.IsZero() {
				dispatchedAt = r.Evidence.RecordedAt
			} else if op.Status.StartedAt != nil {
				dispatchedAt = *op.Status.StartedAt
			}
			if now.Sub(dispatchedAt) <= c.cfg.OpTimeout+c.cfg.TimeoutGrace {
				continue
			}
			err := c.repo.TransitionTarget(ctx, op.Metadata.Name, r.Target.Key(),
				domain.TargetUnknown, domain.ReasonTimeoutNoResult,
				fmt.Sprintf("no result within %s of dispatch; verify actual state before any retry", c.cfg.OpTimeout),
				nil)
			if err != nil && !errors.Is(err, domain.ErrOpState) {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			c.log.Warn("lifecycle target timed out without result", "event", "lifecycle_timeout", "op_id", op.Metadata.Name,
				"target", r.Target.Key(), "dispatched_at", dispatchedAt)
		}
	}
	return firstErr
}

// ---- 结果回灌 ----

// OnLifecycleResult 幂等处理节点上报（grpcagent → 本控制器）：
//   - 操作/幂等键不匹配 → 拒绝应用（审计日志）；
//   - needsReconfirmation → 置位（派发闸门），操作者重新确认前不得续派；
//   - 逐目标：已是结果态 → 重复上报幂等跳过；状态/原因非法（畸形上报）→
//     落 unknown(result-lost)（结果不可用，先核对）；
//   - 证据先脱敏限长再落库（不记录敏感值；模型层 2000 上限会被拒，这里截断
//     保证上报不因超限丢失）。
func (c *Controller) OnLifecycleResult(ctx context.Context, msg ResultMsg) error {
	op, err := c.repo.GetOperation(ctx, msg.OpID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			c.log.Warn("lifecycle result for unknown operation ignored", "event", "lifecycle_result_unknown_op", "op_id", msg.OpID)
			return nil
		}
		return err
	}
	if op.Spec.IdempotencyKey != msg.IdempotencyKey {
		c.log.Warn("lifecycle result idempotency key mismatch", "event", "lifecycle_result_key_mismatch", "op_id", msg.OpID)
		return nil
	}
	if msg.NeedsReconfirmation {
		if err := c.repo.SetNeedsReconfirmation(ctx, msg.OpID, true,
			truncateMsg(redact(msg.ReconfirmReason))); err != nil {
			return err
		}
		c.log.Warn("lifecycle op needs reconfirmation", "event", "lifecycle_needs_reconfirmation", "op_id", msg.OpID,
			"reason", msg.ReconfirmReason)
	}
	for i := range msg.Results {
		r := msg.Results[i]
		key := r.Target.Normalize().Key()
		stored := op.TargetByKey(key)
		if stored == nil {
			c.log.Warn("lifecycle result for unknown target ignored", "event", "lifecycle_result_unknown_target", "op_id", msg.OpID, "target", key)
			continue
		}
		if stored.Target.Machine != msg.Machine {
			c.log.Warn("lifecycle result machine mismatch ignored", "event", "lifecycle_result_machine_mismatch", "op_id", msg.OpID,
				"target", key, "reported", msg.Machine, "stored", stored.Target.Machine)
			continue
		}
		if domain.IsTargetResultState(stored.State) {
			continue // 终态无出边：重复上报幂等跳过（§8.2 Ack 语义由通道层处理）
		}
		state, reason := r.State, r.Reason
		message := truncateMsg(redact(r.Message))
		if !domain.IsTargetResultState(state) ||
			domain.ValidateTargetResult(state, reason) != nil {
			// 畸形上报：状态/原因配对不在白名单。落未知（结果不可用），
			// 绝不静默丢——操作者必须看到"这个目标没结果"。
			c.log.Warn("malformed lifecycle result recorded as unknown", "event", "lifecycle_result_malformed", "op_id", msg.OpID,
				"target", key, "state", state, "reason", reason)
			state, reason = domain.TargetUnknown, domain.ReasonResultLost
			message = truncateMsg(fmt.Sprintf("node reported unusable result (state=%q reason=%q); verify actual state", r.State, r.Reason))
		}
		ev := r.Evidence
		if ev != nil {
			red := *ev
			red.Message = truncateMsg(redact(ev.Message))
			ev = &red
		} else if state == domain.TargetSucceeded || state == domain.TargetFailed {
			ev = &domain.LifecycleEvidence{RecordedAt: c.cfg.Now()}
		}
		if err := c.repo.TransitionTarget(ctx, msg.OpID, key, state, reason, message, ev); err != nil {
			if errors.Is(err, domain.ErrOpState) {
				continue // 并发下已被迁移：按重复处理
			}
			return err
		}
		c.log.Info("lifecycle target result stored", "event", "lifecycle_result_stored", "op_id", msg.OpID,
			"target", key, "state", state, "reason", reason)
	}
	return nil
}

// ---- 内部辅助 ----

type evidenceOpt func(*domain.LifecycleEvidence)

func withEvidence(ev *domain.LifecycleEvidence) evidenceOpt {
	return func(dst *domain.LifecycleEvidence) { *dst = *ev }
}

// withDispatchEvidence 让 running 状态携带派发证据（RecordedAt 是超时扫描的
// 派发时刻依据）。
func withDispatchEvidence(at time.Time) evidenceOpt {
	return func(dst *domain.LifecycleEvidence) {
		*dst = domain.LifecycleEvidence{RecordedAt: at, Message: "dispatched to node"}
	}
}

// transitionTargets 对一组目标做同一迁移（错误逐目标记日志不中断——编排
// 扫描必须推进其余目标）。
func (c *Controller) transitionTargets(ctx context.Context, opID string, targets []domain.LifecycleTarget,
	state, reason, message string, opts ...evidenceOpt) {
	var ev *domain.LifecycleEvidence
	if state == domain.TargetRunning || state == domain.TargetSucceeded || state == domain.TargetFailed {
		e := domain.LifecycleEvidence{RecordedAt: c.cfg.Now()}
		for _, o := range opts {
			o(&e)
		}
		ev = &e
	}
	for _, t := range targets {
		err := c.repo.TransitionTarget(ctx, opID, t.Key(), state, reason, truncateMsg(message), ev)
		if err != nil {
			c.log.Error("lifecycle target transition failed", "event", "lifecycle_transition_failed", "op_id", opID,
				"target", t.Key(), "to", state, "err", err)
		}
	}
}

// redact / truncateMsg：入库前脱敏限长（节点已做一遍，这里兜底——两道闸
// 共用 domain.RedactEvidence 同一份规则，不会漂移）。
func redact(s string) string { return domain.RedactEvidence(s) }

func truncateMsg(s string) string {
	const max = domain.MaxEvidenceMessageLen
	if len(s) <= max {
		return s
	}
	return s[:max-len("…(truncated)")] + "…(truncated)"
}
