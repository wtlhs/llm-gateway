package audit

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/company/llm-gateway/internal/db"
	"github.com/company/llm-gateway/internal/metrics"
)

// CallerInfo 反查得到的 caller 身份。
type CallerInfo struct {
	Tag    string // token.Name
	UserID int32  // token.UserId
	Group  string // token.Group
}

// CallerCache 以 sha256(token_key) 为 key 缓存 caller 映射(M3: 不缓存原文 key)。
// 启动期从 new-api 库只读拉全量, 周期刷新。依据 DESIGN.md §5.4。
type CallerCache struct {
	reader  *db.TokenReader
	refresh time.Duration

	m     sync.RWMutex
	data  map[string]CallerInfo // key = sha256(rawKey)
	ready bool
}

// NewCallerCache 构造缓存(尚未加载, 调用 Start 启动刷新)。
func NewCallerCache(reader *db.TokenReader, refresh time.Duration) *CallerCache {
	return &CallerCache{
		reader:  reader,
		refresh: refresh,
		data:    make(map[string]CallerInfo),
	}
}

// Start 启动后台刷新 goroutine。首次加载完成后 ready=true。
// 返回的 backfillCh 在每次成功刷新后, 把本次 token map 推给调用方,
// 用于触发 db.Store.BackfillCallerByTokenHash 回填历史记录(§5.4)。
func (c *CallerCache) Start(ctx context.Context, onRefresh func(rows []db.TokenRow)) {
	// 立即加载一次, 避免启动窗口期 miss 率过高
	c.refreshOnce(ctx, onRefresh)

	t := time.NewTicker(c.refresh)
	go func() {
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.refreshOnce(ctx, onRefresh)
			}
		}
	}()
}

func (c *CallerCache) refreshOnce(ctx context.Context, onRefresh func([]db.TokenRow)) {
	rows, err := c.reader.LoadAll(ctx)
	if err != nil {
		metrics.CallerLookup.WithLabelValues("error").Inc()
		slog.Error("caller cache refresh failed", "err", err)
		return
	}

	// 构建新 map: key = sha256(rawKey)。注意: rawKey(rows[i].Key) 仅在此处短暂存在,
	// 不进 map, 不长期持有(M3)。
	next := make(map[string]CallerInfo, len(rows))
	for _, r := range rows {
		h := sha256Hex([]byte(r.Key))
		next[h] = CallerInfo{Tag: r.Name, UserID: r.UserID, Group: r.Group}
	}

	c.m.Lock()
	c.data = next
	c.ready = true
	c.m.Unlock()

	metrics.CallerCacheRefreshSuccess.SetToCurrentTime()
	slog.Info("caller cache refreshed", "tokens", len(next))

	if onRefresh != nil {
		onRefresh(rows) // 触发回填
	}
}

// Ready 返回首次加载是否完成。
func (c *CallerCache) Ready() bool {
	c.m.RLock()
	defer c.m.RUnlock()
	return c.ready
}

// LookupByHash 按 token_key_hash 查 caller。
func (c *CallerCache) LookupByHash(hash string) (CallerInfo, bool) {
	c.m.RLock()
	defer c.m.RUnlock()
	info, ok := c.data[hash]
	return info, ok
}

// Enrich 反查并填充 record(§5.4 enrichCaller 健壮性策略)。
// 返回值用于后续 pipeline 调用。
func (c *CallerCache) Enrich(rec *Record) {
	if rec.TokenKeyHash == "" {
		metrics.CallerLookup.WithLabelValues("miss").Inc()
		return
	}
	if info, ok := c.LookupByHash(rec.TokenKeyHash); ok {
		metrics.CallerLookup.WithLabelValues("hit").Inc()
		rec.CallerTag = info.Tag
		rec.CallerUserID = info.UserID
		rec.CallerGroup = info.Group
	} else {
		metrics.CallerLookup.WithLabelValues("miss").Inc()
		// caller_tag 留空, token_key_hash 已存 → 可回填
	}
}
