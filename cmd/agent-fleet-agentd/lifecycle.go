// 节点侧生命周期执行接线（KM-49；与 execute.go 的配置流水线同构）：
// 单 worker 串行执行 ExecuteLifecycleOperation、同 opId 重放幂等（执行中/已入队
// 忽略；已终结从 outbox 重发）、结果 outbox（重连重发、Ack 后淘汰——Ack 复用
// OperationResultAck，键为生命周期操作 id，与配置 outbox 键空间互斥）。
//
// 执行语义在 internal/agentlocal/lifecycle（NodeExecutor：固定版本意图、执行前
// 组合准入、重发现核验、数据保护）；本文件只做队列/幂等/可靠上行。
package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/lifecycle"
	"google.golang.org/protobuf/encoding/protojson"
)

// lifecycleOutboxLimit 是生命周期结果 outbox 的条数上限（FR-13.7 同款最近 N 条）。
const lifecycleOutboxLimit = 64

// lifecycleTargetDeadline 携带协议里的整单超时（unix 秒）。
func lifecycleTargetDeadline(msg *fleetv1.ExecuteLifecycleOperation) time.Time {
	if ts := msg.GetDeadlineUnix(); ts > 0 {
		return time.Unix(ts, 0)
	}
	return time.Time{}
}

// lifecycleTargetsOf 把协议目标转换为执行引擎输入（Machine 取本机 id——目标
// 身份键的 machine 段由控制面维护，节点上报按其原样回带）。
func lifecycleTargetsOf(machineID string, msg *fleetv1.ExecuteLifecycleOperation) []lifecycle.TargetSpec {
	spec := msg.GetSpec()
	targets := make([]lifecycle.TargetSpec, 0, len(spec.GetTargets()))
	for _, t := range spec.GetTargets() {
		targets = append(targets, lifecycle.TargetSpec{
			Machine: t.GetMachineId(), Type: spec.GetType(),
			User: t.GetUser(), Agent: t.GetAgent(),
			Method: t.GetMethod(), Package: t.GetPackage(),
			Version: spec.GetVersion(), Env: t.GetEnv(),
			OS: t.GetOs(), Arch: t.GetArch(),
			Executables: codexExecutables(t.GetAgent()),
		})
	}
	return targets
}

// codexExecutables 给出程序入口名（定位可执行文件；能力矩阵的 Windows .cmd
// shim 风险见 KM-46 报告——路径可能为空，证据如实上报）。
func codexExecutables(agent string) []string {
	if agent == "codex" {
		return []string{"codex"}
	}
	return nil
}

// lifecycleReportToProto 把执行结果转换为上行消息（节点已脱敏限长；控制面
// 落库前还有第二道闸——两道闸同一纪律）。
func lifecycleReportToProto(rep *lifecycle.Report) *fleetv1.LifecycleOperationResult {
	out := &fleetv1.LifecycleOperationResult{
		OpId:                rep.OpID,
		NeedsReconfirmation: rep.NeedsReconfirmation,
		ReconfirmReason:     rep.ReconfirmReason,
		FinishedAtUnix:      rep.FinishedAt.Unix(),
	}
	for _, r := range rep.Results {
		item := &fleetv1.LifecycleTargetResult{
			State:   r.State,
			Reason:  r.Reason,
			Message: r.Message,
		}
		item.Evidence = &fleetv1.LifecycleEvidence{
			ObservedVersion: r.Evidence.ObservedVersion,
			ExecutablePath:  r.Evidence.ExecutablePath,
			Method:          r.Evidence.Method,
			InventorySeq:    r.Evidence.InventorySeq,
			Message:         r.Evidence.Message,
			RecordedAtUnix:  r.Evidence.RecordedAt.Unix(),
		}
		out.Results = append(out.Results, item)
	}
	return out
}

// queuedLifecycleOp 是等待执行的生命周期操作。
type queuedLifecycleOp struct {
	msg *fleetv1.ExecuteLifecycleOperation
}

// lifecycleExecutor 是 daemon 的生命周期执行宿主（单 worker 串行 + outbox）。
type lifecycleExecutor struct {
	machineID string
	exec      *lifecycle.NodeExecutor
	log       logger

	mu        sync.Mutex
	queue     []queuedLifecycleOp
	current   string
	outboxDir string

	// outbox 是已终结、未 Ack 的结果（内存权威 + 每条一文件的落盘镜像；
	// daemon 重启后经 loadOutbox 恢复，重连重发，Ack 后淘汰）。
	outbox      map[string]*fleetv1.LifecycleOperationResult
	outboxOrder []string
}

func newLifecycleExecutor(machineID, dataDir string, exec *lifecycle.NodeExecutor, log logger) *lifecycleExecutor {
	x := &lifecycleExecutor{
		machineID: machineID, exec: exec, log: log,
		outboxDir: filepath.Join(dataDir, "state", "lifecycle-outbox"),
		outbox:    map[string]*fleetv1.LifecycleOperationResult{},
	}
	x.loadOutbox()
	return x
}

// Enqueue 入队一个 ExecuteLifecycleOperation（重复派发幂等）：
//   - 执行中/已入队 → 忽略；
//   - 已终结（outbox 命中）→ 返回既有结果（调用方经流重发）；
//   - 否则入队等待单 worker 执行。
func (x *lifecycleExecutor) Enqueue(msg *fleetv1.ExecuteLifecycleOperation) *fleetv1.LifecycleOperationResult {
	id := msg.GetSpec().GetOpId()
	x.mu.Lock()
	if x.current == id {
		x.mu.Unlock()
		x.log.Warn("duplicate lifecycle execute ignored (executing)", "op_id", id)
		return nil
	}
	for _, q := range x.queue {
		if q.msg.GetSpec().GetOpId() == id {
			x.mu.Unlock()
			x.log.Warn("duplicate lifecycle execute ignored (queued)", "op_id", id)
			return nil
		}
	}
	if res, ok := x.outbox[id]; ok {
		x.mu.Unlock()
		x.log.Info("duplicate lifecycle execute; replaying terminal result", "op_id", id)
		return res
	}
	x.queue = append(x.queue, queuedLifecycleOp{msg: msg})
	x.mu.Unlock()
	x.log.Info("lifecycle operation queued", "op_id", id, "type", msg.GetSpec().GetType())
	return nil
}

// Forget 在收到服务端 Ack 后淘汰 outbox 项（§8.2：Ack 是唯一淘汰依据）。
// 返回是否命中（未命中时调用方应转试配置 outbox——两套键空间互斥）。
func (x *lifecycleExecutor) Forget(opID string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, ok := x.outbox[opID]; !ok {
		return false
	}
	delete(x.outbox, opID)
	for i, id := range x.outboxOrder {
		if id == opID {
			x.outboxOrder = append(x.outboxOrder[:i], x.outboxOrder[i+1:]...)
			break
		}
	}
	_ = os.Remove(x.outboxPath(opID))
	return true
}

// PendingResults 返回 outbox 全部条目（重连后紧随 Hello + 全量观测重发，
// 按 opId 序稳定重放）。
func (x *lifecycleExecutor) PendingResults() []*fleetv1.LifecycleOperationResult {
	x.mu.Lock()
	defer x.mu.Unlock()
	ids := append([]string(nil), x.outboxOrder...)
	sort.Strings(ids)
	out := make([]*fleetv1.LifecycleOperationResult, 0, len(ids))
	for _, id := range ids {
		out = append(out, x.outbox[id])
	}
	return out
}

func (x *lifecycleExecutor) outboxPath(opID string) string {
	return filepath.Join(x.outboxDir, sanitizeID(opID)+".json")
}

// RunWorker 是生命周期单 worker 主循环（任一时刻至多一份生命周期操作在执行；
// 与配置流水线的执行权锁相互独立——两者资源面不相交）。结果经 msgs 交主循环
// 发送（单写者约束）；服务端 Ack 后由 Forget 淘汰 outbox。
func (x *lifecycleExecutor) RunWorker(ctx context.Context, msgs chan<- *fleetv1.LifecycleOperationResult) {
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
		x.current = q.msg.GetSpec().GetOpId()
		x.mu.Unlock()

		rep := x.exec.Execute(ctx, lifecycle.OpRequest{
			OpID:                q.msg.GetSpec().GetOpId(),
			Type:                q.msg.GetSpec().GetType(),
			Version:             q.msg.GetSpec().GetVersion(),
			RediscoveryRequired: q.msg.GetRediscoveryRequired(),
			Deadline:            lifecycleTargetDeadline(q.msg),
			Targets:             lifecycleTargetsOf(x.machineID, q.msg),
		})
		res := lifecycleReportToProto(&rep)

		x.mu.Lock()
		x.current = ""
		x.outbox[res.GetOpId()] = res
		x.outboxOrder = append(x.outboxOrder, res.GetOpId())
		x.outboxPutLocked(res)
		if len(x.outboxOrder) > lifecycleOutboxLimit {
			for _, old := range x.outboxOrder[:len(x.outboxOrder)-lifecycleOutboxLimit] {
				delete(x.outbox, old)
				_ = os.Remove(x.outboxPath(old))
			}
			x.outboxOrder = append([]string(nil), x.outboxOrder[len(x.outboxOrder)-lifecycleOutboxLimit:]...)
		}
		x.mu.Unlock()
		x.log.Info("lifecycle operation finished", "op_id", res.GetOpId(),
			"needs_reconfirmation", res.GetNeedsReconfirmation(), "targets", len(res.GetResults()))

		select {
		case msgs <- res:
		case <-ctx.Done():
			return
		}
	}
}

// outboxPutLocked 把一条终态结果落盘（每条一文件，文件名即 sanitize 后的
// opId；内容是 protojson——损坏文件在 loadOutbox 丢弃，控制面按超时/离线路径
// 落 unknown，先核对再重试）。落盘失败只记日志：内存态仍是权威，Ack 淘汰以
// 内存为准。
func (x *lifecycleExecutor) outboxPutLocked(res *fleetv1.LifecycleOperationResult) {
	if err := os.MkdirAll(x.outboxDir, 0o755); err != nil {
		x.log.Error("lifecycle outbox mkdir failed", "err", err)
		return
	}
	b, err := protojson.Marshal(res)
	if err != nil {
		x.log.Error("lifecycle outbox marshal failed", "op_id", res.GetOpId(), "err", err)
		return
	}
	if err := atomicWrite(x.outboxPath(res.GetOpId()), b, 0o644); err != nil {
		x.log.Error("lifecycle outbox write failed", "op_id", res.GetOpId(), "err", err)
	}
}

// loadOutbox 从磁盘恢复 outbox（daemon 启动时调用；单文件损坏只丢该条，
// 不阻断其余——结果丢失时控制面按超时/离线路径落 unknown，先核对再重试）。
func (x *lifecycleExecutor) loadOutbox() {
	entries, err := os.ReadDir(x.outboxDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(x.outboxDir, e.Name()))
		if err != nil {
			continue
		}
		var res fleetv1.LifecycleOperationResult
		if protojson.Unmarshal(b, &res) != nil {
			x.log.Warn("lifecycle outbox entry unreadable; dropped", "file", e.Name())
			continue
		}
		if res.GetOpId() == "" {
			continue
		}
		x.outbox[res.GetOpId()] = &res
		x.outboxOrder = append(x.outboxOrder, res.GetOpId())
	}
}
