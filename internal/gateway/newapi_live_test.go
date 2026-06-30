package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/company/llm-gateway/internal/audit"
	"github.com/company/llm-gateway/internal/db"
)

// 真实 New API 联调测试: 通过 https://newapi.wtlhs.com 跑真实 LLM 请求,
// 网关捕获后落库到真实 PG, 再读回验证。
//
// 这是离生产最近的验证: 真实 token、真实模型、真实 SSE 格式、真实 PG。
// 需要 3 个环境变量(都不进仓库):
//   NEWAPI_LIVE_URL   - New API 地址(含 https://)
//   NEWAPI_LIVE_TOKEN - 有效 API token (sk-...)
//   PG_E2E_URL        - 真实 PG 连接串
//
// 运行:
//   NEWAPI_LIVE_URL=... NEWAPI_LIVE_TOKEN=sk-... PG_E2E_URL=... \
//   go test ./internal/gateway/ -run TestNewAPILive -v
func TestNewAPILive_NonStream(t *testing.T) {
	env := loadLiveEnv(t)
	if env == nil {
		t.Skip("NEWAPI_LIVE_URL/TOKEN/PG_E2E_URL 未全部设置")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store, err := db.NewStore(ctx, env.pgURL, 5)
	if err != nil {
		t.Fatalf("connect PG: %v", err)
	}
	defer store.Close()
	store.Pool().Exec(ctx, "DELETE FROM llm_conversation WHERE request_id LIKE $1", "live-%")

	cfg := testConfig()
	cfg.NewAPIBaseURL = env.url
	// testConfig() 默认 ModeRedact, 无需重设

	callers := audit.NewNoopCallerCache()
	pipeline := audit.NewPipeline(cfg, store, callers)
	pipeline.Start(ctx)
	t.Cleanup(func() { pipeline.Shutdown(context.Background()) })

	transport := NewCaptureTransportExposed(TransportConfig{Base: http.DefaultTransport, Pipeline: pipeline, Cfg: cfg})
	proxy := NewProxy(transport, cfg.NewAPIBaseURL, cfg.MaxBodyBytes)

	// 用 Go 的 UTF-8 字符串(避免 curl 的 Windows 编码问题)
	gatewayID := "live-nonstream-001"
	body := `{"model":"MiniMax-M3","messages":[{"role":"user","content":"只回复两个字: 你好"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set(gatewayIDHeader, gatewayID)
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("upstream status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 验证响应本身是合法 OpenAI 格式
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not json: %v", err)
	}
	if resp["model"] != "MiniMax-M3" {
		t.Errorf("response model=%v", resp["model"])
	}

	// 读回 PG 验证
	got := waitAndRead(t, store, gatewayID, 10*time.Second)
	t.Logf("PASS: model=%s upstream=%q status=%d tokens=%d/%d redacted=%v",
		got.Model, got.UpstreamRequestID, got.HTTPStatus, got.PromptTokens, got.CompletionTokens, got.Redacted)

	if got.UpstreamRequestID == "" {
		t.Error("upstream_request_id empty (X-Oneapi-Request-Id not captured)")
	}
	if got.Model != "MiniMax-M3" {
		t.Errorf("stored model=%q", got.Model)
	}
	if got.HTTPStatus != 200 {
		t.Errorf("stored http_status=%d", got.HTTPStatus)
	}
	if got.PromptTokens == 0 {
		t.Error("prompt_tokens=0, usage not extracted")
	}

	// 验证 completion 落库(应含 "你好" 附近内容)
	if len(got.CompletionText) == 0 {
		t.Error("completion_text empty")
	} else {
		t.Logf("completion_text: %s", truncateLog(got.CompletionText, 200))
	}

	store.Pool().Exec(ctx, "DELETE FROM llm_conversation WHERE request_id=$1", gatewayID)
}

// TestNewAPILive_Stream 流式: 验证真实 SSE 格式的聚合(MiniMax-M3 的 delta/reasoning_content)。
func TestNewAPILive_Stream(t *testing.T) {
	env := loadLiveEnv(t)
	if env == nil {
		t.Skip("live env 未设置")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store, err := db.NewStore(ctx, env.pgURL, 5)
	if err != nil {
		t.Fatalf("connect PG: %v", err)
	}
	defer store.Close()

	cfg := testConfig()
	cfg.NewAPIBaseURL = env.url

	callers := audit.NewNoopCallerCache()
	pipeline := audit.NewPipeline(cfg, store, callers)
	pipeline.Start(ctx)
	t.Cleanup(func() { pipeline.Shutdown(context.Background()) })

	transport := NewCaptureTransportExposed(TransportConfig{Base: http.DefaultTransport, Pipeline: pipeline, Cfg: cfg})
	proxy := NewProxy(transport, cfg.NewAPIBaseURL, cfg.MaxBodyBytes)

	gatewayID := "live-stream-001"
	body := `{"model":"MiniMax-M3","stream":true,"messages":[{"role":"user","content":"只回复两个字: 你好"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set(gatewayIDHeader, gatewayID)
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	// 客户端应收到 SSE 流(含 data: 行)
	clientBody := rec.Body.String()
	if !strings.Contains(clientBody, "data:") {
		t.Fatalf("client did not receive SSE stream: %s", truncateStr(clientBody, 200))
	}
	if !strings.Contains(clientBody, "[DONE]") {
		t.Error("client stream missing [DONE]")
	}
	t.Logf("client received %d bytes SSE, contains data: and [DONE]", len(clientBody))

	// 读回 PG: 验证聚合后的 completion 是完整文本
	got := waitAndRead(t, store, gatewayID, 15*time.Second)
	if !got.IsStream {
		t.Error("is_stream should be true")
	}
	var comp map[string]any
	if err := json.Unmarshal(got.CompletionText, &comp); err != nil {
		t.Fatalf("completion_text not json: %v raw=%s", err, truncateLog(got.CompletionText, 200))
	}
	choices, _ := comp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatal("completion has no choices")
	}
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	content, _ := msg["content"].(string)
	t.Logf("PASS stream: aggregated content=%q (len=%d) tokens=%d/%d",
		truncateStr(content, 80), len(content), got.PromptTokens, got.CompletionTokens)
	if len(content) == 0 {
		t.Error("aggregated content empty")
	}

	store.Pool().Exec(ctx, "DELETE FROM llm_conversation WHERE request_id=$1", gatewayID)
}

// --- 辅助 ---

type liveEnv struct {
	url, token, pgURL string
}

func loadLiveEnv(t *testing.T) *liveEnv {
	t.Helper()
	url := os.Getenv("NEWAPI_LIVE_URL")
	token := os.Getenv("NEWAPI_LIVE_TOKEN")
	pg := os.Getenv("PG_E2E_URL")
	if url == "" || token == "" || pg == "" {
		return nil
	}
	return &liveEnv{url: url, token: token, pgURL: pg}
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
func truncateLog(b []byte, n int) string { return truncateStr(string(b), n) }
