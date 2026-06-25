package audit

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/company/llm-gateway/internal/config"
	"github.com/company/llm-gateway/internal/db"
	"github.com/company/llm-gateway/internal/metrics"
	"golang.org/x/time/rate"
)

// Pipeline 是 capture → 落库的异步管道。
// 设计依据 DESIGN.md §5.3: 双 channel 分级背压(I4) + M1 时序(响应阶段才 push)。
type Pipeline struct {
	cfg     *config.Config
	store   *db.Store
	callers *CallerCache

	fullCh chan *Record // 完整记录(可丢)
	metaCh chan *Record // 仅元数据(必保)

	// 日志速率限制(防热路径 slog 风暴): 1s 最多 burst 条
	logLimiter *rate.Limiter

	wg     sync.WaitGroup
	closed atomic.Bool
}

// NewPipeline 构造管道(尚未启动 worker)。
func NewPipeline(cfg *config.Config, store *db.Store, callers *CallerCache) *Pipeline {
	return &Pipeline{
		cfg:        cfg,
		store:      store,
		callers:    callers,
		fullCh:     make(chan *Record, cfg.CaptureChannelSize),
		metaCh:     make(chan *Record, cfg.CaptureChannelSize*4),
		logLimiter: rate.NewLimiter(rate.Every(time.Second), 10), // 1s 最多 10 条
	}
}

// Start 启动 worker pool。返回 drain 完成的信号通道(优雅停机用)。
func (p *Pipeline) Start(ctx context.Context) {
	for i := 0; i < p.cfg.WorkerPoolSize; i++ {
		p.wg.Add(1)
		go p.worker(ctx)
	}
	// 仪表: 周期更新 channel 深度
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				metrics.CaptureChannelDepth.WithLabelValues("full").Set(float64(len(p.fullCh)))
				metrics.CaptureChannelDepth.WithLabelValues("meta").Set(float64(len(p.metaCh)))
			}
		}
	}()
}

// Push 在响应阶段(M1)调用: 优先推 full, 满则降级 meta, 都满才彻底丢(I4)。
func (p *Pipeline) Push(rec *Record) {
	// off 模式直接丢弃
	if p.cfg.AuditMode == config.ModeOff {
		NoteOutcome("dropped")
		return
	}

	select {
	case p.fullCh <- rec:
		NoteOutcome("full")
		return
	default:
	}
	select {
	case p.metaCh <- rec.StripContent():
		NoteOutcome("metadata-only")
		p.warnOnce("full channel full, degraded to metadata-only")
	default:
		NoteOutcome("dropped")
		p.warnOnce("both channels full, record dropped")
	}
}

// worker 消费两个 channel。full 优先, meta 兜底。
func (p *Pipeline) worker(ctx context.Context) {
	defer p.wg.Done()
	for {
		// 优先消费 full
		select {
		case rec, ok := <-p.fullCh:
			if !ok {
				// fullCh 关闭后, 排空 meta 再退出
				p.drainMeta(ctx)
				return
			}
			p.persist(ctx, rec)
			continue
		default:
		}
		// 否则等任一 channel
		select {
		case rec, ok := <-p.fullCh:
			if !ok {
				p.drainMeta(ctx)
				return
			}
			p.persist(ctx, rec)
		case rec, ok := <-p.metaCh:
			if !ok {
				return
			}
			p.persist(ctx, rec)
		case <-ctx.Done():
			return
		}
	}
}

// drainMeta 在 fullCh 关闭后排空 metaCh。
func (p *Pipeline) drainMeta(ctx context.Context) {
	for {
		select {
		case rec, ok := <-p.metaCh:
			if !ok {
				return
			}
			p.persist(ctx, rec)
		default:
			return
		}
	}
}

// persist: enrich → redact → truncate → insert(DESIGN.md §5.3)。
func (p *Pipeline) persist(ctx context.Context, rec *Record) {
	p.callers.Enrich(rec)
	Apply(rec, p.cfg.AuditMode)

	start := time.Now()
	err := p.store.Insert(ctx, toDBRecord(rec))
	metrics.DBInsertDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		if errors.Is(err, db.ErrDuplicate) {
			// 幂等命中, 不算错误
			return
		}
		metrics.DBInsertErrors.Inc()
		p.warnOnce("insert failed", "err", err)
	}
}

// warnOnce 速率受限的 warn。
func (p *Pipeline) warnOnce(msg string, args ...any) {
	if p.logLimiter.Allow() {
		slog.Warn(msg, args...)
	}
}

// Shutdown 排空 channel(优雅停机, DESIGN.md §5.8)。
// 关闭 fullCh 让 worker 转为只消费 meta, 然后关闭 metaCh 让 worker 退出。
func (p *Pipeline) Shutdown(ctx context.Context) {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	close(p.fullCh)
	// 等 full 排空(或超时)
	waitDone := make(chan struct{})
	go func() { p.wg.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-ctx.Done():
		slog.Error("pipeline drain timeout, forcing exit")
	}
	// metaCh 由 worker 在 fullCh 关闭后 drainMeta 排空
}

// toDBRecord 把内部 Record 转成 db.Conversation。
func toDBRecord(rec *Record) *db.Conversation {
	return &db.Conversation{
		RequestID:         rec.GatewayID,
		UpstreamRequestID: rec.UpstreamRequestID,
		CallerTag:         rec.CallerTag,
		CallerUserID:      rec.CallerUserID,
		CallerGroup:       rec.CallerGroup,
		TokenKeyHash:      rec.TokenKeyHash,
		Model:             rec.Model,
		Endpoint:          rec.Endpoint,
		IsStream:          rec.IsStream,
		PromptText:        rec.PromptText,
		CompletionText:    rec.CompletionText,
		ToolCalls:         rec.ToolCalls,
		RequestBodyHash:   rec.RequestBodyHash,
		HTTPStatus:        rec.HTTPStatus,
		PromptTokens:      rec.PromptTokens,
		CompletionTokens:  rec.CompletionTokens,
		ErrorMessage:      rec.ErrorMessage,
		ClientIP:          rec.ClientIP,
		Redacted:          rec.Redacted,
		Truncated:         rec.Truncated,
		UpstreamLatencyMs: rec.UpstreamLatencyMs,
		TotalLatencyMs:    rec.TotalLatencyMs,
	}
}

// FullChLen / MetaChLen 暴露给 main 用于停机日志。
func (p *Pipeline) FullChLen() int { return len(p.fullCh) }
func (p *Pipeline) MetaChLen() int { return len(p.metaCh) }
