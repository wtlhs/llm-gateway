// 对比 prompt caching: 直连 vs 经网关, 验证网关不破坏缓存命中。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/company/llm-gateway/internal/audit"
	"github.com/company/llm-gateway/internal/config"
	"github.com/company/llm-gateway/internal/db"
	gw "github.com/company/llm-gateway/internal/gateway"
)

// 配置通过环境变量(不进仓库)。空则用默认值便于快速试跑。
var (
	newAPIURL = envOr("NEWAPI_LIVE_URL", "https://newapi.wtlhs.com")
	tok       = envOr("NEWAPI_LIVE_TOKEN", "")
	model     = envOr("NEWAPI_LIVE_MODEL", "MiniMax-M3")
)

func main() {
	if tok == "" {
		fmt.Fprintln(os.Stderr, "请设置 NEWAPI_LIVE_TOKEN 环境变量")
		os.Exit(1)
	}
	ctx := context.Background()
	cfg := &config.Config{
		NewAPIBaseURL: newAPIURL, AuditMode: config.ModeRedact,
		CaptureEndpointsCSV: "chat/completions", MaxBodyBytes: 65536, PreBodyMaxBytes: 33554432,
		TTLDays: 90, BreakerFailures: 100, RatePerCaller: 100000, RateBurst: 100000, RateAnon: 100000,
		CaptureChannelSize: 4096, DBMaxOpenConns: 10, WorkerPoolSize: 8,
		ShutdownTimeout: 5 * time.Second, DrainTimeout: 5 * time.Second,
		CallerCacheRefresh: time.Hour, LogLevel: "error",
	}
	pipeline := audit.NewPipeline(cfg, noop{}, audit.NewNoopCallerCache())
	pipeline.Start(ctx)
	defer pipeline.Shutdown(ctx)
	transport := gw.NewCaptureTransportExposed(gw.TransportConfig{
		Base: &http.Transport{MaxIdleConnsPerHost: 50}, Pipeline: pipeline, Cfg: cfg,
	})
	proxy := gw.NewProxy(transport, cfg.NewAPIBaseURL, cfg.MaxBodyBytes)
	proxySrv := httptest.NewServer(http.HandlerFunc(proxy.ServeHTTP))
	defer proxySrv.Close()

	sys := strings.Repeat("你是一个专业的技术文档助手，擅长解释复杂概念。", 40)
	user := "什么是区块链"

	// 先直连预热缓存(让上游建立缓存)
	fmt.Println("=== 预热: 直连 2 次 ===")
	for i := 1; i <= 2; i++ {
		c, p := send(newAPIURL+"/v1/chat/completions", sys, user)
		fmt.Printf("  直连第%d次: cached=%d/%d (%.0f%%)\n", i, c, p, rate(c, p))
	}

	// 经网关: 看缓存是否还命中(关键!)
	fmt.Println("\n=== 经网关 4 次(关键: 缓存是否被网关破坏) ===")
	for i := 1; i <= 4; i++ {
		c, p := send(proxySrv.URL+"/v1/chat/completions", sys, user)
		fmt.Printf("  网关第%d次: cached=%d/%d (%.0f%%)\n", i, c, p, rate(c, p))
	}

	// 再回直连对照
	fmt.Println("\n=== 对照: 回直连 2 次 ===")
	for i := 1; i <= 2; i++ {
		c, p := send(newAPIURL+"/v1/chat/completions", sys, user)
		fmt.Printf("  直连第%d次: cached=%d/%d (%.0f%%)\n", i, c, p, rate(c, p))
	}
}

func send(url, sys, user string) (cached, prompt int) {
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"system","content":%q},{"role":"user","content":%q}]}`, model, sys, user)
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil { return -1, -1 }
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	var r struct {
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			PromptTokensDetails struct{ CachedTokens int `json:"cached_tokens"` } `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	json.Unmarshal(rb, &r)
	return r.Usage.PromptTokensDetails.CachedTokens, r.Usage.PromptTokens
}
func rate(c, p int) float64 { if p == 0 { return 0 }; return float64(c) / float64(p) * 100 }

type noop struct{}
func (noop) Insert(context.Context, *db.Conversation) error { return nil }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
