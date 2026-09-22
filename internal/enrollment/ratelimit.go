package enrollment

import (
	"sync"
	"time"
)

// IPLimiter 是 enroll 端点的按源 IP 失败限速器（§11：S3 升为强制——
// 失败尝试默认每源 IP 每分钟 ≤10 次，可配置）。用法：
// Check(ip) 放行判断（不计数），失败后 RecordFailure(ip) 计数；
// 成功的 enroll（通过 token 认证）不占限额。
type IPLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*ipWindow
}

type ipWindow struct {
	start time.Time
	count int
}

func NewIPLimiter(max int, window time.Duration) *IPLimiter {
	if max <= 0 {
		max = 10
	}
	if window <= 0 {
		window = time.Minute
	}
	return &IPLimiter{max: max, window: window, hits: make(map[string]*ipWindow)}
}

// Check 报告该 IP 的失败额度是否尚未用尽（不计数）。
func (l *IPLimiter) Check(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.hits[ip]
	if !ok || now.Sub(w.start) >= l.window {
		return true
	}
	return w.count < l.max
}

// RecordFailure 记一次失败尝试；窗口过多时惰性整体重置（MVP 规模足够）。
func (l *IPLimiter) RecordFailure(ip string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.hits[ip]
	if !ok || now.Sub(w.start) >= l.window {
		if len(l.hits) > 4096 {
			l.hits = make(map[string]*ipWindow)
		}
		l.hits[ip] = &ipWindow{start: now, count: 0}
		w = l.hits[ip]
	}
	w.count++
}
