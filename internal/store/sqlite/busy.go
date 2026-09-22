package sqlite

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// busyRetriesTotal 是进程内 SQLITE_BUSY 有界重试的累计计数（§12.1/NFR-7：
// SQLITE_BUSY/重试次数须可观测）。/metrics 端点随可观测性切片暴露该值。
var busyRetriesTotal atomic.Int64

// BusyRetriesTotal 返回进程启动以来 SQLITE_BUSY 触发重试的次数。
func BusyRetriesTotal() int64 { return busyRetriesTotal.Load() }

// busyMaxAttempts 与退避基数：5 次尝试，100ms 起指数退避（100/200/400/800ms），
// 覆盖 busy_timeout=5s 之外仍出现写竞争的窗口。
const busyMaxAttempts = 5

const busyBaseBackoff = 100 * time.Millisecond

// isBusyErr 识别 SQLITE_BUSY / database is locked 类错误。
// modernc 驱动对 SQLITE_BUSY 报 "database is locked (5)"，对
// SQLITE_BUSY_SNAPSHOT 报 "(517)"，两者同属"写锁不可得"。
func isBusyErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}

// withBusyRetry 对写路径施加有界重试（KM-21 核查发现 #2 的处置：
// "必要时加有界重试并计数（§12）"）。重试与计数都只面向该辅助函数的调用方；
// 其余写路径依赖 busy_timeout=5s + IMMEDIATE 事务。
func withBusyRetry(ctx context.Context, op string, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < busyMaxAttempts; attempt++ {
		if attempt > 0 {
			backoff := busyBaseBackoff << (attempt - 1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
		lastErr = fn()
		if lastErr == nil || !isBusyErr(lastErr) {
			return lastErr
		}
		total := busyRetriesTotal.Add(1)
		slog.Warn("sqlite busy; retrying write",
			"op", op, "attempt", attempt+1, "max_attempts", busyMaxAttempts,
			"busy_retries_total", total)
	}
	return lastErr
}
