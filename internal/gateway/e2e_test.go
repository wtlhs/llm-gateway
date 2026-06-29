package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/company/llm-gateway/internal/audit"
	"github.com/company/llm-gateway/internal/config"
	"github.com/company/llm-gateway/internal/db"
)

// mockPersister 捕获所有 Insert 调用, 供断言。
type mockPersister struct {
	mu       sync.Mutex
	records  []*db.Conversation
	failWith error
}

func (m *mockPersister) Insert(ctx context.Context, c *db.Conversation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, c)
	return m.failWith
}

func (m *mockPersister) snapshot() []*db.Conversation {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*db.Conversation, len(m.records))
	copy(out, m.records)
	return out
}

// testConfig 构造一份合法的 Config(通过 Validate)。
func testConfig() *config.Config {
	return &config.Config{
		ListenAddr:          ":0",
		NewAPIBaseURL:       "http://upstream.test",
		ContextDBURL:        "postgres://test/test",
		NewAPIB_URL:         "postgres://test/test",
		AuditMode:           config.ModeRedact,
		CaptureEndpointsCSV: "chat/completions,completions,responses,embeddings,moderations",
		MaxBodyBytes:        65536,
		PreBodyMaxBytes:     33554432,
		TTLDays:             90,
		BreakerFailures:     5,
		RatePerCaller:       1000,
		RateBurst:           1000,
		RateAnon:            100,
		CaptureChannelSize:  64,
		DBMaxOpenConns:      25,
		WorkerPoolSize:      8,
		ShutdownTimeout:     5 * time.Second,
		DrainTimeout:        5 * time.Second,
		CallerCacheRefresh:  time.Hour,
		LogLevel:            "error",
	}
}

// newTestGateway 装配一条完整链路: 真实 Gateway + mock 上游 + mock persister。
// 返回 proxy / mock上游 / spy。调用方负责关闭 mock上游。
func newTestGateway(t *testing.T, upstream http.Handler) (*Proxy, *httptest.Server, *mockPersister, *audit.Pipeline) {
	t.Helper()
	upSrv := httptest.NewServer(upstream)
	t.Cleanup(upSrv.Close)

	cfg := testConfig()
	cfg.NewAPIBaseURL = upSrv.URL

	spy := &mockPersister{}
	callers := audit.NewNoopCallerCache()
	pipeline := audit.NewPipeline(cfg, spy, callers)
	pipeline.Start(context.Background())
	t.Cleanup(func() { pipeline.Shutdown(context.Background()) })

	transport := NewCaptureTransportExposed(TransportConfig{
		Base:     http.DefaultTransport,
		Pipeline: pipeline,
		Cfg:      cfg,
	})
	proxy := NewProxy(transport, cfg.NewAPIBaseURL, cfg.MaxBodyBytes)
	return proxy, upSrv, spy, pipeline
}

// waitForRecord 轮询直到 spy 收到 N 条记录或超时(异步落库, 需等待)。
func waitForRecord(t *testing.T, spy *mockPersister, n int) []*db.Conversation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := spy.snapshot(); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return spy.snapshot()
}

// ============================================================
// 测试 1: 非流式请求 → 透传 + 捕获 prompt+completion
// ============================================================
func TestE2E_NonStream_Captured(t *testing.T) {
	// mock 上游: 回固定 chat completion
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 模拟 New API 行为: 自生成 request-id 放响应头
		w.Header().Set("X-Oneapi-Request-Id", "newapi-req-123")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"hello back"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	})

	proxy, _, spy, _ := newTestGateway(t, upstream)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-test12345678901234567890")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	proxy.ServeHTTP(rec, req)

	// 断言 1: 响应透传正确
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var respJSON map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &respJSON); err != nil {
		t.Fatalf("response not json: %v body=%s", err, rec.Body.String())
	}
	if got := respJSON["model"]; got != "gpt-4o" {
		t.Errorf("response model = %v, want gpt-4o", got)
	}
	// 断言 2: 响应头透传
	if got := rec.Header().Get("X-Oneapi-Request-Id"); got != "newapi-req-123" {
		t.Errorf("upstream request id header not forwarded: got %q", got)
	}

	// 断言 3: record 落库(异步, 等待)
	recs := waitForRecord(t, spy, 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	// M1: request_id 是网关自生成, upstream_request_id 是 New API 的
	if got.UpstreamRequestID != "newapi-req-123" {
		t.Errorf("upstream_request_id = %q, want newapi-req-123", got.UpstreamRequestID)
	}
	if got.RequestID == "" {
		t.Error("request_id (gateway_id) should not be empty")
	}
	if got.Model != "gpt-4o" {
		t.Errorf("model = %q", got.Model)
	}
	if got.HTTPStatus != 200 {
		t.Errorf("http_status = %d", got.HTTPStatus)
	}
	if got.PromptTokens != 5 || got.CompletionTokens != 2 {
		t.Errorf("tokens = prompt:%d completion:%d, want 5/2", got.PromptTokens, got.CompletionTokens)
	}
	// M3: token 只存 hash 不存明文
	if got.TokenKeyHash == "" {
		t.Error("token_key_hash empty")
	}
	// 脱敏模式: redacted=true
	if !got.Redacted {
		t.Error("expected redacted=true (mode=redact)")
	}
	// prompt 含原文(脱敏后)
	var promptJSON map[string]any
	if err := json.Unmarshal(got.PromptText, &promptJSON); err != nil {
		t.Errorf("prompt_text not json: %v", err)
	}
}

// ============================================================
// 测试 2: 流式请求 → SSE 逐字透传 + 聚合落库
// ============================================================
func TestE2E_Stream_AggregatedAndForwarded(t *testing.T) {
	chunks := []string{
		`data: {"choices":[{"delta":{"content":"Hello"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":", world"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}` + "\n\n",
		`data: [DONE]` + "\n\n",
	}
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Oneapi-Request-Id", "newapi-stream-1")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		for _, c := range chunks {
			io.WriteString(w, c)
			if flusher != nil {
				flusher.Flush()
			}
		}
	})

	proxy, _, spy, _ := newTestGateway(t, upstream)

	body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-testabcdef")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	// 客户端应收到所有 chunk 原文(K1: 零延迟透传, 不改字节)
	clientBody := rec.Body.String()
	if !strings.Contains(clientBody, "Hello") || !strings.Contains(clientBody, "world") {
		t.Errorf("stream content not fully forwarded: %s", clientBody)
	}
	if !strings.Contains(clientBody, "[DONE]") {
		t.Error("missing [DONE] sentinel in forwarded stream")
	}

	// 异步落库: 等待聚合后的 1 条记录
	recs := waitForRecord(t, spy, 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 aggregated record, got %d", len(recs))
	}
	got := recs[0]
	if !got.IsStream {
		t.Error("expected is_stream=true")
	}
	// 聚合的 completion 应含 "Hello, world"
	var comp map[string]any
	if err := json.Unmarshal(got.CompletionText, &comp); err != nil {
		t.Fatalf("completion_text not json: %v", err)
	}
	choices, _ := comp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatal("completion has no choices")
	}
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	content, _ := msg["content"].(string)
	if content != "Hello, world" {
		t.Errorf("aggregated content = %q, want 'Hello, world'", content)
	}
	if got.PromptTokens != 3 || got.CompletionTokens != 2 {
		t.Errorf("stream usage tokens wrong: %d/%d", got.PromptTokens, got.CompletionTokens)
	}
}

// ============================================================
// 测试 3: 白名单外端点(images)→ 纯透传不捕获
// ============================================================
func TestE2E_NonWhitelisted_NotCaptured(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"created":1,"data":[{"url":"http://img"}]}`)
	})

	proxy, _, spy, _ := newTestGateway(t, upstream)

	// images/generations 是 C 类, 不在白名单
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"prompt":"a cat"}`))
	req.Header.Set("Authorization", "Bearer sk-x")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	// 不应产生 record
	time.Sleep(200 * time.Millisecond)
	if len(spy.snapshot()) != 0 {
		t.Errorf("non-whitelisted endpoint should not be captured, got %d records", len(spy.snapshot()))
	}
}

// ============================================================
// 测试 4: 上游错误 → 透传错误 + 记录 error_message
// ============================================================
func TestE2E_UpstreamError_Captured(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Oneapi-Request-Id", "newapi-err-1")
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"model not found","type":"invalid_request_error"}}`)
	})

	proxy, _, spy, _ := newTestGateway(t, upstream)

	body := `{"model":"nonexistent","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	recs := waitForRecord(t, spy, 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 error record, got %d", len(recs))
	}
	got := recs[0]
	if got.HTTPStatus != 400 {
		t.Errorf("http_status = %d", got.HTTPStatus)
	}
	if !strings.Contains(got.ErrorMessage, "model not found") {
		t.Errorf("error_message = %q, want contains 'model not found'", got.ErrorMessage)
	}
}

// ============================================================
// 测试 5: gzip 请求体 → 网关解压捕获 + 原字节透传
// ============================================================
func TestE2E_GzipRequest_Captured(t *testing.T) {
	var receivedBody string
	var receivedEncoding string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedEncoding = r.Header.Get("Content-Encoding")
		raw, _ := io.ReadAll(r.Body)
		receivedBody = string(raw) // 上游收到的应是原始压缩字节
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	})

	proxy, _, spy, _ := newTestGateway(t, upstream)

	// 构造 gzip 请求体
	orig := `{"model":"gpt-4o","messages":[{"role":"user","content":"compressed hi"}]}`
	var gz bytes.Buffer
	gw := newGzipWriter(&gz)
	gw.Write([]byte(orig))
	gw.Close()
	// 备份原始压缩字节(body 被读取后 gz 会清空, 必须先复制)
	gzBackup := append([]byte(nil), gz.Bytes()...)

	req := httptest.NewRequest("POST", "/v1/chat/completions", &gz)
	req.Header.Set("Authorization", "Bearer sk-gz")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	// K3: 上游收到原始压缩字节(Content-Encoding 透传)
	if receivedEncoding != "gzip" {
		t.Errorf("upstream content-encoding = %q, want gzip", receivedEncoding)
	}
	if !bytes.Equal([]byte(receivedBody), gzBackup) {
		t.Errorf("upstream did not receive original compressed bytes: got %d bytes, want %d", len(receivedBody), len(gzBackup))
	}
	// 捕获的 prompt 应是解压后的明文
	recs := waitForRecord(t, spy, 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	var prompt map[string]any
	if err := json.Unmarshal(recs[0].PromptText, &prompt); err != nil {
		t.Errorf("prompt not decoded json: %v", err)
	}
	msgs, _ := prompt["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("decoded prompt has no messages")
	}
	content := msgs[0].(map[string]any)["content"].(string)
	if content != "compressed hi" {
		t.Errorf("decoded content = %q, want 'compressed hi'", content)
	}
}
