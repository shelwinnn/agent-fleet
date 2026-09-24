// 节点侧执行接线（架构 v1.1.2 §5.2/§5.3/FR-13.7/13.8/13.9/§5.6）：
// 单 worker 队列串行执行 ExecuteOperation（跨进程执行权锁 + 服务端互斥双重表达，
// FR-9.9）、同 operationId 重放幂等（不产生第二条变更链）、终态结果 outbox
// （重连重发、Ack 后淘汰）、取消在阶段边界生效。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/reconciler"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// outboxLimit 是节点持久化的最近终态结果条数上限（FR-13.7：最近 N 条）。
const outboxLimit = 64

// queuedOp 是等待执行的操作。
type queuedOp struct {
	OperationID  string
	Generation   int64
	SnapshotJSON []byte
	// Baseline 是控制面随 ExecuteOperation 下发的计划基线（FR-12.7，可为空）。
	Baseline *reconciler.Baseline
}

// workerMsg 是 worker → 主循环的上行消息（主循环是流的唯一发送者，P1-2）。
// baselineFromProto 把协议里的计划基线转换为流水线基线（FR-12.7）：为空时返回
// nil（无基线可比），非空时 apply 前的重算基线必须与它一致，否则零变更拒绝。
//
// 【已知取舍】当前返回 nil = fail-open：控制面还没有生产者在 AutoPlan→apply 流程里
// 填充 baseline（见 docs/ssh-only-oneshot.md 遗留项 4）。补上生产者时必须同时改成
// fail-closed（缺 baseline 即拒绝执行），否则"确认后 apply"会退化成"目标代确认"。
func baselineFromProto(b *fleetv1.PlanBaseline) *reconciler.Baseline {
	if b == nil || b.GetObservedProjectionDigest() == "" {
		return nil
	}
	return &reconciler.Baseline{
		ObservedProjectionDigest: b.GetObservedProjectionDigest(),
		InventorySeq:             b.GetInventorySeq(),
	}
}

type workerMsg struct {
	progress *fleetv1.OperationProgress
	result   *fleetv1.OperationResult
	// observation 是**与该操作绑定的** apply 后观测（§4.4 门禁条件 2/3/4 的
	// 唯一证据形态）：必须在 OperationResult 之前上报，服务端才能先落证据、
	// 再按终态评估门禁。
	observation *fleetv1.ObservedState
}

// observations 是执行宿主依赖的节点观测能力（由 inventory.Collector 实现）：
// 持久化最近一次期望快照（周期 inventory 的期望侧输入）与产出操作绑定观测。
type observations interface {
	NextSeq() (int64, error)
	SaveDesired(snapshotJSON []byte) error
	CollectForOperation(opID string, ev *domain.VerifyEvidence) domain.ObservedState
}

// executor 是 daemon 的操作执行宿主。
type executor struct {
	home    string
	dataDir string
	reg     *adapter.Registry
	obs     observations
	exec    *reconciler.Executor
	lock    *reconciler.ExecutionLock
	log     logger

	mu      sync.Mutex
	queue   []queuedOp
	current *queuedOp
	cancels map[string]chan struct{} // operationID → 取消标志（阶段边界轮询）

	outboxMu sync.Mutex
}

// logger 是 daemon 侧使用的最小日志接口（便于测试注入）。
type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

func newExecutor(home, dataDir string, reg *adapter.Registry, obs observations, log logger) *executor {
	return &executor{
		home:    home,
		dataDir: dataDir,
		reg:     reg,
		obs:     obs,
		exec:    reconciler.New(reconciler.Options{Registry: reg, Seq: obs.NextSeq, Now: time.Now}),
		lock:    reconciler.NewExecutionLock(dataDir),
		log:     log,
		cancels: map[string]chan struct{}{},
	}
}

// Enqueue 入队一个 ExecuteOperation（FR-13.8 重复派发幂等）：
//   - 已在执行或已入队 → 忽略（执行中回报已知进度）；
//   - 已终结（outbox 命中）→ 直接重发终态（返回值非 nil，调用方重发）；
//   - 否则入队等待单 worker 执行。
func (x *executor) Enqueue(op *fleetv1.ExecuteOperation) *reconciler.Result {
	id := op.GetOperationId()
	x.mu.Lock()
	if x.current != nil && x.current.OperationID == id {
		x.mu.Unlock()
		x.log.Warn("duplicate execute ignored (executing)", "operation_id", id)
		return nil
	}
	for _, q := range x.queue {
		if q.OperationID == id {
			x.mu.Unlock()
			x.log.Warn("duplicate execute ignored (queued)", "operation_id", id)
			return nil
		}
	}
	x.mu.Unlock()
	if res, ok := x.outboxGet(id); ok {
		x.log.Info("duplicate execute; replaying terminal result from outbox", "operation_id", id)
		return &res
	}
	// 期望快照是周期 inventory 的期望侧输入（§7.1）：随操作持久化，
	// 使周期上报的投影基于"当前快照"而非陈旧内容。
	if err := x.obs.SaveDesired(op.GetSnapshot().GetSnapshotJson()); err != nil {
		x.log.Warn("persist desired snapshot failed", "operation_id", id, "err", err)
	}
	x.mu.Lock()
	x.queue = append(x.queue, queuedOp{
		OperationID:  id,
		Generation:   op.GetDesiredGeneration(),
		SnapshotJSON: op.GetSnapshot().GetSnapshotJson(),
		Baseline:     baselineFromProto(op.GetBaseline()),
	})
	x.mu.Unlock()
	x.log.Info("operation queued", "operation_id", id, "generation", op.GetDesiredGeneration())
	return nil
}

// Cancel 请求取消（FR-13.9）：对在途操作置取消标志（阶段边界生效）；对仅入队
// 未开始的操作直接以 Failed(Cancelled) 终结（无变更、无需恢复）。返回直接终结
// 的结果（无则 nil）。
func (x *executor) Cancel(operationID string) *reconciler.Result {
	x.mu.Lock()
	for i, q := range x.queue {
		if q.OperationID == operationID {
			x.queue = append(x.queue[:i], x.queue[i+1:]...)
			x.mu.Unlock()
			now := time.Now().UTC()
			return &reconciler.Result{
				Phase: "Failed", Reason: "Cancelled", TerminalModifier: "Cancelled",
				Generation: q.Generation,
				Message:    "cancelled before start (queued)",
				StartedAt:  now, FinishedAt: now,
			}
		}
	}
	ch, running := x.cancels[operationID]
	x.mu.Unlock()
	if running {
		close(ch)
		x.log.Info("cancel flag set at next stage boundary", "operation_id", operationID)
	} else {
		x.log.Warn("cancel for unknown operation ignored", "operation_id", operationID)
	}
	return nil
}

// RunWorker 是单 worker 主循环（FR-9.9：任一时刻至多一条变更流水线）。消息经
// msgs channel 交主循环发送（单写者约束）；服务端 Ack 后由 Forget 淘汰 outbox。
func (x *executor) RunWorker(ctx context.Context, msgs chan<- workerMsg) {
	for {
		x.mu.Lock()
		if len(x.queue) == 0 {
			x.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		q := x.queue[0]
		x.queue = x.queue[1:]
		x.current = &q
		cancelCh := make(chan struct{})
		x.cancels[q.OperationID] = cancelCh
		x.mu.Unlock()

		res := x.runOne(ctx, q, cancelCh, msgs)

		x.mu.Lock()
		delete(x.cancels, q.OperationID)
		x.current = nil
		x.mu.Unlock()
		select {
		case msgs <- workerMsg{result: resultToProto(q.OperationID, res)}:
		case <-ctx.Done():
			return
		}
	}
}

// runOne 执行单个操作：取得执行权（§5.6）→ 流水线 → 终态落 outbox。
func (x *executor) runOne(ctx context.Context, q queuedOp, cancelCh chan struct{}, msgs chan<- workerMsg) *reconciler.Result {
	// 执行权：daemon 取得失败时排队等待（自身单 worker 已串行；此处等待覆盖
	// 锁被外部 oneshot 持有的窗口，§5.6 规则 3）。陈旧锁不静默夺取（规则 5）。
	for {
		err := x.lock.Acquire("daemon", q.OperationID)
		if err == nil {
			break
		}
		var held *reconciler.LockHeldError
		var stale *reconciler.StaleLockError
		switch {
		case errors.As(err, &held):
			x.log.Warn("execution lock held; daemon keeps waiting (BlockedByNodeLock)",
				"operation_id", q.OperationID, "err", err.Error())
		case errors.As(err, &stale):
			x.log.Error("stale execution lock; refusing silent takeover",
				"operation_id", q.OperationID, "err", err.Error())
			res := reconciler.Result{
				Phase: "Failed", Reason: "StaleExecutionLock", Generation: q.Generation,
				Message:   "stale execution lock requires explicit recovery (" + err.Error() + ")",
				StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(),
			}
			x.outboxPut(q.OperationID, res)
			return &res
		default:
			res := reconciler.Result{
				Phase: "Failed", Reason: "ConfigWriteFailed", Generation: q.Generation,
				Message:   fmt.Sprintf("acquire execution lock failed: %v", err),
				StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(),
			}
			x.outboxPut(q.OperationID, res)
			return &res
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
	defer func() { _ = x.lock.Release() }()

	started := time.Now().UTC()
	sendProgress(msgs, q.OperationID, "start", "Running", "")

	res := x.exec.Run(ctx, x.home, reconciler.Execution{
		OperationID:     q.OperationID,
		Generation:      q.Generation,
		SnapshotJSON:    q.SnapshotJSON,
		RequireBaseline: q.Baseline,
		Cancelled: func() bool {
			select {
			case <-cancelCh:
				return true
			default:
				return false
			}
		},
		Progress: func(step, phase, message string) {
			sendProgress(msgs, q.OperationID, step, phase, message)
		},
	})
	if res.StartedAt.IsZero() {
		res.StartedAt = started
	}
	if res.FinishedAt.IsZero() {
		res.FinishedAt = time.Now().UTC()
	}
	// 与该操作绑定的 apply 后观测先上行（§4.4：门禁条件 2/3/4 只接受同一
	// operationId 的观测；周期 inventory 不构成门禁证据）。
	if x.obs != nil && res.Verify != nil {
		bound := x.obs.CollectForOperation(q.OperationID, res.Verify)
		select {
		case msgs <- workerMsg{observation: observedToProto(bound)}:
		case <-ctx.Done():
			return nil
		}
	}
	// 终态落 outbox（§5.2：Ack 之后方可淘汰）。
	x.outboxPut(q.OperationID, res)
	return &res
}

// Forget 在收到服务端 Ack 后淘汰 outbox 项（§8.2：Ack 是唯一淘汰依据）。
func (x *executor) Forget(operationID string) {
	x.outboxMu.Lock()
	defer x.outboxMu.Unlock()
	_ = os.Remove(x.outboxPath(operationID))
}

// PendingResult 是一条待确认的终态结果（含 operationId）。
type PendingResult struct {
	OperationID string
	Result      reconciler.Result
}

// PendingResults 返回 outbox 全部条目（FR-13.7：重连后紧随 Hello + 全量观测重发）。
func (x *executor) PendingResults() []PendingResult {
	x.outboxMu.Lock()
	defer x.outboxMu.Unlock()
	entries, err := os.ReadDir(x.outboxDir())
	if err != nil {
		return nil
	}
	var out []PendingResult
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(x.outboxDir(), e.Name()))
		if err != nil {
			continue
		}
		var res reconciler.Result
		if json.Unmarshal(b, &res) == nil {
			// 文件名即 sanitizeID(operationId)（本节点生成的 id 无需还原——
			// 重发必须携带原始 id，故 id 一并入库存放于条目自身）。
			out = append(out, PendingResult{OperationID: res.OperationID, Result: res})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Result.FinishedAt.Before(out[j].Result.FinishedAt) })
	return out
}

func (x *executor) outboxDir() string { return filepath.Join(x.dataDir, "state", "outbox") }

func (x *executor) outboxPath(id string) string {
	return filepath.Join(x.outboxDir(), sanitizeID(id)+".json")
}

func (x *executor) outboxGet(id string) (reconciler.Result, bool) {
	x.outboxMu.Lock()
	defer x.outboxMu.Unlock()
	b, err := os.ReadFile(x.outboxPath(id))
	if err != nil {
		return reconciler.Result{}, false
	}
	var res reconciler.Result
	if json.Unmarshal(b, &res) != nil {
		return reconciler.Result{}, false
	}
	return res, true
}

func (x *executor) outboxPut(id string, res reconciler.Result) {
	x.outboxMu.Lock()
	defer x.outboxMu.Unlock()
	if err := os.MkdirAll(x.outboxDir(), 0o755); err != nil {
		x.log.Error("outbox mkdir failed", "err", err)
		return
	}
	res.OperationID = id // outbox 条目自证身份（重发时携带原始 operationId）
	b, err := json.Marshal(res)
	if err != nil {
		return
	}
	if err := atomicWrite(x.outboxPath(id), b, 0o644); err != nil {
		x.log.Error("outbox write failed", "operation_id", id, "err", err)
		return
	}
	// 容量上限：超过 outboxLimit 时淘汰最老条目（FR-13.7 最近 N 条）。
	entries, _ := os.ReadDir(x.outboxDir())
	if len(entries) > outboxLimit {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries[:len(entries)-outboxLimit] {
			_ = os.Remove(filepath.Join(x.outboxDir(), e.Name()))
		}
	}
}

// sendProgress 把步骤进度投递给主循环发送（背压式：通道满时流水线暂停片刻，
// 不丢失进度也不另开 goroutine 违反单写者）。
func sendProgress(msgs chan<- workerMsg, opID, step, phase, message string) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case msgs <- workerMsg{progress: &fleetv1.OperationProgress{
		OperationId: opID, Step: step, Phase: phase, Message: message,
	}}:
	case <-timer.C:
	}
}

func sanitizeID(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		if r == '/' || r == '\\' || r == ':' {
			out = append(out, '_')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// resultToProto 把终态结果转换为上行消息。verify 证据必须随结果发出（§4.4 门禁
// 四条件与 FR-15.5 审计证据都以它为准；节点算了不发 = 服务端永远没有证据，
// Reconciled 也就永远不会置 True）。Generation 使重发结果与首次上报等价。
func resultToProto(operationID string, res *reconciler.Result) *fleetv1.OperationResult {
	return &fleetv1.OperationResult{
		OperationId:       operationID,
		Phase:             res.Phase,
		Reason:            res.Reason,
		Message:           res.Message,
		DesiredGeneration: res.Generation,
		TerminalModifier:  res.TerminalModifier,
		StartedAtUnix:     res.StartedAt.Unix(),
		FinishedAtUnix:    res.FinishedAt.Unix(),
		Verify:            verifyToProto(res.Verify),
	}
}

// verifyToProto 映射门禁与审计证据（两侧受管投影摘要 + 规范化版本 + 节点本地
// 采集序 + 适配器健康；§6.4/FR-15.5）。
func verifyToProto(v *domain.VerifyEvidence) *fleetv1.VerifyEvidence {
	if v == nil {
		return nil
	}
	return &fleetv1.VerifyEvidence{
		DesiredProjectionDigest:  v.DesiredProjectionDigest,
		ObservedProjectionDigest: v.ObservedProjectionDigest,
		CanonicalizationVersion:  v.CanonicalizationVersion,
		InventorySeq:             v.InventorySeq,
		AdapterHealth:            v.AdapterHealth,
	}
}
