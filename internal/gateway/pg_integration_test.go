package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/company/llm-gateway/internal/audit"
	"github.com/company/llm-gateway/internal/config"
	"github.com/company/llm-gateway/internal/db"
)

// 最终验证: 真实 Gateway 全链路 + 真实 PG 落库。
// 覆盖 DESIGN.md §8 验收清单核心项: 非流式/流式捕获、request_id 关联、脱敏、token hash。
// 仅当 PG_E2E_URL 设置时运行(连接串不进仓库)。
//
// 运行: PG_E2E_URL="postgresql://..." go test ./internal/gateway/ -run TestPGIntegration -v
func TestPGIntegration(t *testing.T) {
	url := os.Getenv("PG_E2E_URL")
	if url == "" {
		t.Skip("PG_E2E_URL 未设置, 跳过真实 PG 集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := db.NewStore(ctx, url, 5)
	if err != nil {
		t.Fatalf("connect PG: %v", err)
	}
	defer store.Close()

	cfg := testConfig()
	// 清理本次测试残留(固定前缀, 幂等重跑)
	storeCleanupPrefix(ctx, t, store, "pgint-")

	// mock New API: 自生成 request-id 放响应头(模拟真实 New API 行为)
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Oneapi-Request-Id", "newapi-nonstream-001")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"model":"gpt-4o","choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`)
	}))
	t.Cleanup(upSrv.Close)
	cfg.NewAPIBaseURL = upSrv.URL

	callers := audit.NewNoopCallerCache()
	pipeline := audit.NewPipeline(cfg, store, callers)
	pipeline.Start(ctx)
	t.Cleanup(func() { pipeline.Shutdown(context.Background()) })

	transport := NewCaptureTransportExposed(TransportConfig{
		Base: http.DefaultTransport, Pipeline: pipeline, Cfg: cfg,
	})
	proxy := NewProxy(transport, cfg.NewAPIBaseURL, cfg.MaxBodyBytes)

	// 注入可控 gateway_id(transport 会复用请求头里的值)
	gatewayID := "pgint-nonstream-001"
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"call 13812345678"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set(gatewayIDHeader, gatewayID)
	req.Header.Set("Authorization", "Bearer sk-integration-test-key-1234567890")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("proxy status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 等待异步落库 + 从 PG 读回验证
	got := waitAndRead(t, store, gatewayID, 5*time.Second)

	// §8.7: requestId 关联(M1)
	if got.UpstreamRequestID != "newapi-nonstream-001" {
		t.Errorf("upstream_request_id=%q, want newapi-nonstream-001", got.UpstreamRequestID)
	}
	if got.RequestID != gatewayID {
		t.Errorf("request_id=%q, want %q", got.RequestID, gatewayID)
	}
	// §8.8: 脱敏(手机号应被替换)
	if strings.Contains(string(got.PromptText), "13812345678") {
		t.Error("phone not redacted in stored prompt_text")
	}
	if !got.Redacted {
		t.Error("redacted flag should be true (mode=redact)")
	}
	// §9/M3: token 只存 hash
	if got.TokenKeyHash == "" {
		t.Error("token_key_hash should not be empty")
	}
	if got.HTTPStatus != 200 {
		t.Errorf("http_status=%d, want 200", got.HTTPStatus)
	}
	if got.Model != "gpt-4o" {
		t.Errorf("model=%q", got.Model)
	}
	t.Logf("PASS: model=%s upstream=%q redacted=%v tokens=%d/%d prompt_md5_truncated=%v",
		got.Model, got.UpstreamRequestID, got.Redacted, got.PromptTokens, got.CompletionTokens, got.Truncated)

	// 清理本次记录
	storeCleanupOne(ctx, store, gatewayID)
}

// TestPGIntegration_Stream 流式 + 真实 PG: 验证 SSE 聚合后落库的 completion 完整。
func TestPGIntegration_Stream(t *testing.T) {
	url := os.Getenv("PG_E2E_URL")
	if url == "" {
		t.Skip("PG_E2E_URL 未设置")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := db.NewStore(ctx, url, 5)
	if err != nil {
		t.Fatalf("connect PG: %v", err)
	}
	defer store.Close()
	cfg := testConfig()

	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Oneapi-Request-Id", "newapi-stream-001")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		chunks := []string{
			`data: {"choices":[{"delta":{"content":"Hello"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":", world"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}
		for _, c := range chunks {
			io.WriteString(w, c)
			fl.Flush()
		}
	}))
	t.Cleanup(upSrv.Close)
	cfg.NewAPIBaseURL = upSrv.URL

	callers := audit.NewNoopCallerCache()
	pipeline := audit.NewPipeline(cfg, store, callers)
	pipeline.Start(ctx)
	t.Cleanup(func() { pipeline.Shutdown(context.Background()) })

	transport := NewCaptureTransportExposed(TransportConfig{Base: http.DefaultTransport, Pipeline: pipeline, Cfg: cfg})
	proxy := NewProxy(transport, cfg.NewAPIBaseURL, cfg.MaxBodyBytes)

	gatewayID := "pgint-stream-001"
	body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set(gatewayIDHeader, gatewayID)
	req.Header.Set("Authorization", "Bearer sk-stream-test-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Hello") {
		t.Errorf("stream not forwarded: %s", rec.Body.String())
	}

	got := waitAndRead(t, store, gatewayID, 5*time.Second)
	if !got.IsStream {
		t.Error("is_stream should be true")
	}
	// 聚合的 completion 应含 "Hello, world"
	var comp map[string]any
	if err := json.Unmarshal(got.CompletionText, &comp); err != nil {
		t.Fatalf("completion_text not json: %v raw=%s", err, got.CompletionText)
	}
	choices := comp["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	content := msg["content"].(string)
	if content != "Hello, world" {
		t.Errorf("aggregated content=%q, want 'Hello, world'", content)
	}
	if got.PromptTokens != 3 || got.CompletionTokens != 2 {
		t.Errorf("stream tokens=%d/%d", got.PromptTokens, got.CompletionTokens)
	}
	t.Logf("PASS stream: aggregated=%q upstream=%q", content, got.UpstreamRequestID)
	storeCleanupOne(ctx, store, gatewayID)
}

// --- 辅助 ---

// waitAndRead 轮询 PG 直到记录出现或超时。
// 注意: 可空列(caller_tag 等)用指针扫描, 避免 NULL scan 到 string 报错。
func waitAndRead(t *testing.T, store *db.Store, gatewayID string, timeout time.Duration) *db.Conversation {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var (
			c            db.Conversation
			upstream     *string
			callerTag    *string
			tokenHash    *string
			promptTokens *int32
		)
		err := store.Pool().QueryRow(context.Background(),
			`SELECT request_id, upstream_request_id, caller_tag, token_key_hash, model,
			        is_stream, prompt_text, completion_text, http_status, prompt_tokens,
			        completion_tokens, redacted, truncated
			 FROM llm_conversation WHERE request_id=$1`, gatewayID,
		).Scan(&c.RequestID, &upstream, &callerTag, &tokenHash,
			&c.Model, &c.IsStream, &c.PromptText, &c.CompletionText, &c.HTTPStatus,
			&promptTokens, &c.CompletionTokens, &c.Redacted, &c.Truncated)
		if err == nil {
			if upstream != nil { c.UpstreamRequestID = *upstream }
			if callerTag != nil { c.CallerTag = *callerTag }
			if tokenHash != nil { c.TokenKeyHash = *tokenHash }
			if promptTokens != nil { c.PromptTokens = *promptTokens }
			return &c
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("record %s not found in PG after %v", gatewayID, timeout)
	return nil
}

func storeCleanupPrefix(ctx context.Context, t *testing.T, store *db.Store, prefix string) {
	t.Helper()
	store.Pool().Exec(ctx, "DELETE FROM llm_conversation WHERE request_id LIKE $1", prefix+"%")
}
func storeCleanupOne(ctx context.Context, store *db.Store, id string) {
	store.Pool().Exec(ctx, "DELETE FROM llm_conversation WHERE request_id=$1", id)
}

// 防止未使用 import 警告(config 用于 testConfig 内部)
var _ = config.ModeRedact
