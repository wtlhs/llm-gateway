// cmd/platform 是可视化管理平台的后端进程。
// 详见 docs/PLATFORM_DESIGN.md。
// 与网关(cmd/gateway)独立部署, 共享 db.Store 代码但连接池隔离。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // 嵌入 IANA 时区数据(静态二进制无系统 tzdata, 否则 PLATFORM_TIMEZONE 失效)

	"github.com/company/llm-gateway/internal/db"
	"github.com/company/llm-gateway/internal/platform"
)

func main() {
	// 1. 加载配置
	cfg, err := platform.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}
	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	slog.Info("starting platform server",
		"listen", cfg.ListenAddr,
		"timezone", cfg.Timezone,
		"db_pool", cfg.DBMaxOpenConns)

	// 安全配置摘要(启动时一目了然)
	if cfg.AuthToken == "" {
		slog.Warn("⚠ PLATFORM_AUTH_TOKEN 未设置! 仅允许 localhost 访问(生产必须设置 token)")
	} else {
		slog.Info("security: auth token enabled")
	}
	if cfg.AllowIPs != "" {
		slog.Info("security: IP allowlist active", "ips", cfg.AllowIPs)
	} else {
		slog.Warn("security: IP allowlist not configured (open to all IPs)")
	}
	slog.Info("security: rate limit", "per_min", cfg.RateLimitPerMin)
	slog.Info("security: security headers enabled (CSP/X-Frame-Options/HSTS)")
	if cfg.CORSOrigins == "*" {
		slog.Warn("security: CORS=* (生产建议限制为具体域名)")
	}

	// 2. 连接沉淀库(复用 db.NewStore, 独立连接池)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := db.NewStore(ctx, cfg.DBURL, cfg.DBMaxOpenConns)
	if err != nil {
		slog.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	// 3. 装配 HTTP server
	server := platform.New(cfg, store)

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second, // 平台无 SSE, 可设超时
		WriteTimeout:      30 * time.Second,
	}

	go func() {
		slog.Info("platform http server listening", "addr", cfg.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen failed", "err", err)
			cancel()
		}
	}()

	// 4. 优雅停机
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	slog.Info("shutdown signal received")

	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer sCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown error", "err", err)
	}
	slog.Info("platform server stopped")
}
