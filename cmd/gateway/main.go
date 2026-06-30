// llm-gateway 主程序: 企业级上下文仓库 Phase 1 网关。
// 组装顺序见 DESIGN.md §5.8 graceful shutdown。
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/company/llm-gateway/internal/audit"
	"github.com/company/llm-gateway/internal/cleanup"
	"github.com/company/llm-gateway/internal/config"
	"github.com/company/llm-gateway/internal/db"
	"github.com/company/llm-gateway/internal/gateway"
	"github.com/company/llm-gateway/internal/metrics"
	"github.com/company/llm-gateway/internal/query"
	"github.com/company/llm-gateway/internal/security"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	cfgPath := flag.String("env", "", "可选 .env 文件路径(默认读环境变量)")
	flag.Parse()
	_ = cfgPath // Phase1: 仅环境变量, 占位

	// 1. 加载 + 校验配置(S6)
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	// 2. 装配 slog(带字段黑名单, §9)
	setupLogger(cfg)

	slog.Info("starting llm-gateway",
		"listen", cfg.ListenAddr, "upstream", cfg.NewAPIBaseURL,
		"mode", cfg.AuditMode, "ttl_days", cfg.TTLDays)

	// 3. 根 context + 信号
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 4. 双库连接
	store, err := db.NewStore(ctx, cfg.ContextDBURL, cfg.DBMaxOpenConns)
	if err != nil {
		slog.Error("context db connect failed", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	// 5. caller cache(反查 New API token 表)。
	// 降级设计: New API 库连接失败不应让网关起不来 —— caller 标识是增强,
	// 核心的代理+沉淀职责必须可用。连接失败时降级为 noop cache(caller_tag 留空)。
	var callers *audit.CallerCache
	tokenReader, terr := db.NewTokenReader(ctx, cfg.NewAPIB_URL)
	if terr != nil {
		slog.Warn("newapi db connect failed, caller enrichment degraded to noop (gateway still functional)",
			"err", terr, "hint", "检查 NEWAPI_DB_URL 的库名是否正确")
		callers = audit.NewNoopCallerCache()
	} else {
		defer tokenReader.Close()
		callers = audit.NewCallerCache(tokenReader, cfg.CallerCacheRefresh)
		callers.Start(ctx, func(rows []db.TokenRow) {
			// 回填历史记录(§5.4): cache 刷新后补 caller
			for _, r := range rows {
				// 注意: 此处 r.Key 短暂存在, 仅算 hash 后丢弃(M3)
				h := audit.SHA256Hex(r.Key)
				if n, err := store.BackfillCallerByTokenHash(ctx, h, r.Name, r.UserID, r.Group); err == nil && n > 0 {
					slog.Debug("backfilled caller", "hash", h, "n", n)
				}
			}
		})
	}

	// 6. pipeline(双 channel 落库)
	pipeline := audit.NewPipeline(cfg, store, callers)
	pipeline.Start(ctx)

	// 7. transport + proxy
	base := newBaseTransport(cfg)
	transport := gateway.NewCaptureTransportExposed(gateway.TransportConfig{
		Base:     base,
		Pipeline: pipeline,
		Cfg:      cfg,
	})
	proxy := gateway.NewProxy(transport, cfg.NewAPIBaseURL, cfg.MaxBodyBytes)

	// 8. 管理端点
	mux := gateway.NewMux(
		proxy,
		promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{Registry: metrics.Registry}),
		query.StatsHandler(store),
		security.AuthMiddleware(cfg.AdminAuthToken),
	)

	// 9. TTL 清理
	go cleanup.Run(ctx, store, cfg.TTLDays)

	// 10. 启动 HTTP server
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// 不设 ReadTimeout/WriteTimeout: SSE 流式可能长时间持续
	}
	go func() {
		slog.Info("http server listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen failed", "err", err)
			cancel()
		}
	}()

	// 11. graceful shutdown(§5.8)
	<-ctx.Done()
	slog.Info("shutdown signal received, draining...")

	shutdownCtx, sCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer sCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown error", "err", err)
	}

	drainCtx, dCancel := context.WithTimeout(context.Background(), cfg.DrainTimeout)
	defer dCancel()
	pipeline.Shutdown(drainCtx)
	slog.Info("drain complete",
		"full_ch_residual", pipeline.FullChLen(), "meta_ch_residual", pipeline.MetaChLen())
	slog.Info("bye")
}

func newBaseTransport(cfg *config.Config) http.RoundTripper {
	t := &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	if cfg.NewAPITLS {
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return t
}

func setupLogger(cfg *config.Config) {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	base := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	filtered := security.NewBlocklistHandler(base, cfg)
	slog.SetDefault(slog.New(filtered))
}
