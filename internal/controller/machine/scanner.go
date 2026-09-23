package machine

import (
	"context"
	"log/slog"
	"time"
)

// RunOfflineScanner 以固定周期执行心跳超时扫描（§4.2 职责 2），直到 ctx 取消。
// 由 main 装配（cmd 只做装配，扫描逻辑在本控制器）。
func RunOfflineScanner(ctx context.Context, c *Controller, every, offlineAfter time.Duration, log *slog.Logger) {
	if every <= 0 {
		every = 5 * time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	log.Info("offline scanner started", "interval", every.String(), "offline_after", offlineAfter.String())
	for {
		select {
		case <-ctx.Done():
			log.Info("offline scanner stopped")
			return
		case now := <-ticker.C:
			if err := c.ScanOffline(ctx, offlineAfter, now.UTC()); err != nil {
				log.Error("offline scan failed", "err", err)
			}
		}
	}
}
