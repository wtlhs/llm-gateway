// Package cleanup 实现按 TTL 清理过期记录(多实例安全, advisory lock)。
// 设计依据 DESIGN.md §5.5。
package cleanup

import (
	"context"
	"log/slog"
	"time"

	"github.com/company/llm-gateway/internal/db"
)

// TTLLockID advisory lock 的固定 id(多实例约定同一值, 保证互斥)。
const TTLLockID int64 = 91020250625 // 任意固定常数

// Run 启动 TTL 清理 goroutine。每小时尝试一次; 多实例下 advisory lock 保证只有一个执行。
func Run(ctx context.Context, store *db.Store, ttlDays int) {
	interval := time.Hour
	t := time.NewTicker(interval)
	defer t.Stop()

	runOnce := func() {
		ok, err := store.TryAcquireTTLLock(ctx, TTLLockID)
		if err != nil {
			slog.Error("ttl lock acquire failed", "err", err)
			return
		}
		if !ok {
			slog.Debug("ttl cleanup skipped: another instance holds the lock")
			return
		}
		defer store.ReleaseTTLLock(ctx, TTLLockID)

		cutoff := time.Now().AddDate(0, 0, -ttlDays)
		n, err := store.DeleteOlderThan(ctx, cutoff)
		if err != nil {
			slog.Error("ttl delete failed", "err", err)
			return
		}
		if n > 0 {
			slog.Info("ttl cleanup done", "deleted", n, "cutoff", cutoff.Format(time.RFC3339))
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runOnce()
		}
	}
}
