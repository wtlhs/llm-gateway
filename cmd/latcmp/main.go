// 真实 New API 延迟对比: 直连 vs 经网关。
package main

import (
	"context"
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

// 配置通过环境变量(不进仓库)。
var (
	newAPIURL = envOr("NEWAPI_LIVE_URL", "https://newapi.wtlhs.com")
	tok       = os.Getenv("NEWAPI_LIVE_TOKEN")
	model     = envOr("NEWAPI_LIVE_MODEL", "MiniMax-M3")
	N         = 5
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
	pipeline := audit.NewPipeline(cfg, noopStore{}, audit.NewNoopCallerCache())
	pipeline.Start(ctx)
	defer pipeline.Shutdown(ctx)
	transport := gw.NewCaptureTransportExposed(gw.TransportConfig{
		Base: &http.Transport{MaxIdleConnsPerHost: 50}, Pipeline: pipeline, Cfg: cfg,
	})
	proxy := gw.NewProxy(transport, cfg.NewAPIBaseURL, cfg.MaxBodyBytes)
	proxySrv := httptest.NewServer(http.HandlerFunc(proxy.ServeHTTP))
	defer proxySrv.Close()

	nsBody := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"只回复: 你好"}]}`, model)
	sBody := fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"只回复: 你好"}]}`, model)

	report("非流式 完整往返",
		measure(newAPIURL+"/v1/chat/completions", nsBody),
		measure(proxySrv.URL+"/v1/chat/completions", nsBody))
	report("流式 首字节 TTFT(用户体感)",
		measureFB(newAPIURL+"/v1/chat/completions", sBody),
		measureFB(proxySrv.URL+"/v1/chat/completions", sBody))
	report("流式 完整往返",
		measure(newAPIURL+"/v1/chat/completions", sBody),
		measure(proxySrv.URL+"/v1/chat/completions", sBody))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func measure(url, body string) []time.Duration {
	var out []time.Duration
	c := &http.Client{Timeout: 60 * time.Second}
	for i := 0; i < N; i++ {
		req, _ := http.NewRequest("POST", url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		t0 := time.Now()
		resp, err := c.Do(req)
		if err != nil { continue }
		io.Copy(io.Discard, resp.Body); resp.Body.Close()
		out = append(out, time.Since(t0))
	}
	return out
}
func measureFB(url, body string) []time.Duration {
	var out []time.Duration
	c := &http.Client{Timeout: 60 * time.Second}
	for i := 0; i < N; i++ {
		req, _ := http.NewRequest("POST", url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		t0 := time.Now()
		resp, err := c.Do(req)
		if err != nil { continue }
		n, _ := io.CopyN(io.Discard, resp.Body, 1)
		if n == 1 { out = append(out, time.Since(t0)) }
		io.Copy(io.Discard, resp.Body); resp.Body.Close()
	}
	return out
}

func report(title string, d, g []time.Duration) {
	fmt.Printf("\n=== %s ===\n", title)
	if len(d) == 0 || len(g) == 0 { fmt.Println("  数据不足"); return }
	di, gi := avg(d), avg(g)
	fmt.Printf("  直连  平均 %v  [%v ~ %v]\n", di, mn(d), mx(d))
	fmt.Printf("  网关  平均 %v  [%v ~ %v]\n", gi, mn(g), mx(g))
	fmt.Printf("  ► 增量 %v (占比 %.2f%%)\n", gi-di, pct(gi-di, di))
}

func avg(d []time.Duration) time.Duration { var s time.Duration; for _, x := range d { s += x }; return s / time.Duration(len(d)) }
func mn(d []time.Duration) time.Duration { m := d[0]; for _, x := range d { if x < m { m = x } }; return m }
func mx(d []time.Duration) time.Duration { m := d[0]; for _, x := range d { if x > m { m = x } }; return m }
func pct(inc, base time.Duration) float64 { if base == 0 { return 0 }; return float64(inc) / float64(base) * 100 }

type noopStore struct{}
func (noopStore) Insert(context.Context, *db.Conversation) error { return nil }
