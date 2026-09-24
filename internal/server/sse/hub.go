// Package sse 实现 SSE 事件枢纽（架构 v1.1.2 §8.1、§3.6；spec §23.6）：
// 事件格式固定为 `event: <resource-type>` + `data: {id, revision}`，供 Web UI
// 按资源类型/ID + revision 选择性重取（FR-14.2）。
//
// 设计边界：
//   - 枢纽只广播"哪个资源变了、变到哪个版本"，不承载资源正文——UI 拿到事件后
//     经 REST 重取权威数据，SSE 不成为第二条数据路径（NFR-9：契约不依赖前端框架）；
//   - 事件由持久层写入成功后发布（见 internal/store/sqlite/change.go），
//     因此控制面重启不会丢失"当前状态"，UI 重连后做一次全量重取即可收敛；
//   - 订阅端缓冲写满即被摘除（连接关闭），由客户端重连 + 全量重取自愈；
//     枢纽不静默丢弃单个事件，避免 UI 停在过期视图上。
package sse

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// 资源类型：事件名（`event:` 字段）取值，与 REST 路径段一致（§8.1）。
const (
	ResourceMachines    = "machines"
	ResourceProfiles    = "profiles"
	ResourceSkills      = "skills"
	ResourceProviders   = "providers"
	ResourceDeployments = "deployments"
	ResourceOperations  = "operations"
)

const (
	// DefaultBufferSize 是单个订阅者的缓冲事件数。事件只携带 (id, revision)，
	// 溢出即摘除连接，宁可让客户端重连重取，也不静默丢事件。
	DefaultBufferSize = 128
	// DefaultKeepAlive 是保活注释行周期（防中间层空闲超时断开）。
	DefaultKeepAlive = 15 * time.Second
	// retryHintMS 是 SSE `retry:` 建议重连间隔（毫秒）。
	retryHintMS = 3000
)

// Event 是一次资源变更通知。序列化后即 §8.1 的 `data:` 载荷。
// Resource 只做事件名，不进 JSON 体（事件名已由 `event:` 行承载）。
type Event struct {
	Resource string `json:"-"`
	// ID 是资源名（operations 为操作 id；两者都等于 metadata.name）。
	ID string `json:"id"`
	// Revision 是写入后的 metadata.resourceVersion；删除事件为删除前的版本 +1
	//（即该资源最后一次可见变更的版本），`id` 此后经 REST 读取会得到 404。
	Revision int64 `json:"revision"`
}

type subscriber struct {
	ch   chan Event
	done chan struct{}
}

// Hub 是进程内事件枢纽（单写多播）。Publish 永不阻塞调用方（写路径不得被
// 慢客户端拖住）。
type Hub struct {
	log       *slog.Logger
	keepAlive time.Duration
	buffer    int

	mu   sync.Mutex
	subs map[*subscriber]struct{}

	published   atomic.Int64
	evicted     atomic.Int64
	connectedTo atomic.Int64 // 累计连接数（测试与运维观察）
}

// NewHub 构造枢纽。log 为空时用 slog.Default()。
func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{
		log:       log,
		keepAlive: DefaultKeepAlive,
		buffer:    DefaultBufferSize,
		subs:      map[*subscriber]struct{}{},
	}
}

// Publish 广播一次变更。resource 与 id 为空时忽略（防御性：调用方不应出现空值）。
func (h *Hub) Publish(resource, id string, revision int64) {
	if resource == "" || id == "" {
		return
	}
	ev := Event{Resource: resource, ID: id, Revision: revision}
	h.published.Add(1)

	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		select {
		case sub.ch <- ev:
		default:
			// 缓冲满：摘除该订阅者并关闭其连接。客户端重连时会做全量重取，
			// 因此不会停在过期视图上；反之静默丢事件会。
			h.evictLocked(sub, "subscriber buffer full")
		}
	}
}

// evictLocked 摘除订阅者（调用方须持有 h.mu）。幂等。
func (h *Hub) evictLocked(sub *subscriber, reason string) {
	if _, ok := h.subs[sub]; !ok {
		return
	}
	delete(h.subs, sub)
	close(sub.done)
	h.evicted.Add(1)
	h.log.Warn("sse subscriber evicted", "reason", reason, "subscribers", len(h.subs))
}

// Subscribers 返回当前订阅者数量（可观测性与测试）。
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Stats 返回累计发布/摘除事件数与累计连接数。
func (h *Hub) Stats() (published, evicted, connections int64) {
	return h.published.Load(), h.evicted.Load(), h.connectedTo.Load()
}

// Handler 返回 `GET /api/v1/events` 的处理器（§8.1：Accept: text/event-stream）。
// 鉴权由调用方（httpapi 的 auth 中间件）包裹——SSE 与其余端点同一凭据模型（§29.14）。
func (h *Hub) Handler() http.Handler {
	return http.HandlerFunc(h.serve)
}

func (h *Hub) serve(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeStreamError(w, "response writer does not support streaming")
		return
	}

	sub := &subscriber{ch: make(chan Event, h.buffer), done: make(chan struct{})}
	h.mu.Lock()
	h.subs[sub] = struct{}{}
	n := len(h.subs)
	h.mu.Unlock()
	h.connectedTo.Add(1)
	h.log.Info("sse subscriber connected", "remote", r.RemoteAddr, "subscribers", n)

	defer func() {
		h.mu.Lock()
		delete(h.subs, sub)
		rest := len(h.subs)
		h.mu.Unlock()
		h.log.Info("sse subscriber disconnected", "remote", r.RemoteAddr, "subscribers", rest)
	}()

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("Connection", "keep-alive")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// 重连建议 + 立刻 flush 响应头：客户端据此确认流已建立。
	if _, err := w.Write([]byte("retry: " + strconv.Itoa(retryHintMS) + "\n\n")); err != nil {
		return
	}
	flusher.Flush()

	keepAlive := time.NewTicker(h.keepAlive)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.done:
			// 被枢纽摘除（缓冲溢出）：关闭连接，客户端重连后全量重取。
			return
		case ev := <-sub.ch:
			if !writeEvent(w, ev) {
				return
			}
			flusher.Flush()
		case <-keepAlive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeEvent 写一条 `event:`/`data:` 记录；返回 false 表示连接已不可写。
func writeEvent(w http.ResponseWriter, ev Event) bool {
	payload, err := json.Marshal(ev)
	if err != nil {
		// Event 只含 string/int64，正常不可能失败；失败也不写半条记录。
		return true
	}
	if _, err := w.Write([]byte("event: " + ev.Resource + "\ndata: ")); err != nil {
		return false
	}
	if _, err := w.Write(payload); err != nil {
		return false
	}
	_, err = w.Write([]byte("\n\n"))
	return err == nil
}

// writeStreamError 是本包内最小错误体（§6.4 五要素形状）：枢纽不 import
// httpapi（避免循环依赖），而 net/http 默认的纯文本错误会破坏契约形状。
func writeStreamError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	body, err := json.Marshal(map[string]string{
		"reason":    "Internal",
		"message":   message,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return
	}
	_, _ = w.Write(append(body, '\n'))
}
