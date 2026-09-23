package sqlite

import "sync"

// ChangeSink 在持久层写入成功后接收变更通知（架构 v1.1.2 §8.1/§23.6：SSE 事件
// 枢纽的数据源）。resource 为资源类型（与 REST 路径段/SSE 事件名同形），id 为
// 资源名（operations 为操作 id），revision 为写入后的 resourceVersion。
//
// 为什么钩子放在持久层而不是 HTTP 处理器：控制面的写入有三条来源——REST 处理器、
// 控制器（status/操作相位）、gRPC agent 端点（观测上报）。只有"写成功之后"这一个
// 位置能同时覆盖三者，且天然排除校验失败与被回滚的写入。
type ChangeSink func(resource, id string, revision int64)

// changeNotifier 是 ChangeSink 的可变持有者：各仓储构造时共享同一指针，
// 因此 SetChangeSink 可在仓储构造前后调用（装配顺序不敏感，测试可后注入）。
type changeNotifier struct {
	mu sync.RWMutex
	fn ChangeSink
}

func (n *changeNotifier) set(fn ChangeSink) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.fn = fn
}

// emit 通知一次变更；未注入 sink（或槽为 nil）时是空操作（库层不依赖 SSE 包）。
func (n *changeNotifier) emit(resource, id string, revision int64) {
	if n == nil {
		return
	}
	n.mu.RLock()
	fn := n.fn
	n.mu.RUnlock()
	if fn != nil {
		fn(resource, id, revision)
	}
}

// SetChangeSink 注入变更接收器（生产装配传 SSE 枢纽的 Publish，见 cmd/agent-fleet-server）。
func (d *DB) SetChangeSink(fn ChangeSink) { d.changes.set(fn) }

// 资源类型取值：与 REST 路径段、SSE 事件名一致（§8.1）；资源表的表名与事件名同形。
const (
	resourceMachines    = "machines"
	resourceOperations  = "operations"
	resourceDeployments = "deployments"
)
