package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	mu        sync.Mutex
	records   []*db.Conversation
	sysPrompts []*db.SystemPrompt // 捕获 system prompt upsert 调用
	failWith  error
}

func (m *mockPersister) Insert(ctx context.Context, c *db.Conversation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, c)
	return m.failWith
}

// UpsertSystemPrompt 实现 audit.SystemPromptPersister, 让 pipeline 触发 system 分流。
// 去重模拟: 同 hash 只记第一次。
func (m *mockPersister) UpsertSystemPrompt(ctx context.Context, sp *db.SystemPrompt, callerTag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.sysPrompts {
		if existing.Hash == sp.Hash {
			return nil // 模拟 ON CONFLICT 命中
		}
	}
	m.sysPrompts = append(m.sysPrompts, sp)
	return nil
}

func (m *mockPersister) snapshotSysPrompts() []*db.SystemPrompt {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*db.SystemPrompt, len(m.sysPrompts))
	copy(out, m.sysPrompts)
	return out
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
		CaptureEndpointsCSV: "chat/completions,completions,responses,embeddings,moderations,messages",
		MaxBodyBytes:        65536,
		CompletionMaxBytes:  10485760,
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
	proxy := NewProxy(transport, cfg.NewAPIBaseURL, cfg.CompletionMaxBytes)
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
// 测试 6: 流式请求 → 网关注入 stream_options.include_usage=true 给上游
// 验证: 经网关转发后, 上游收到的 body 必含 include_usage=true,
// 从而上游会在最后一帧下发 usage(修复 token 计量 80% 丢失)。
// ============================================================
func TestE2E_Stream_IncludeUsageInjected(t *testing.T) {
	var upstreamReceivedBody string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		upstreamReceivedBody = string(raw)
		// 上游回一个含 usage 的流(模拟注入生效后上游的真实行为)
		chunks := []string{
			`data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}
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

	// 客户端发的原始 body: 流式但没带 stream_options
	body := `{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-inject-test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}

	// 断言 1: 上游收到的 body 必含 include_usage=true(注入生效)
	var upstreamBody map[string]any
	if err := json.Unmarshal([]byte(upstreamReceivedBody), &upstreamBody); err != nil {
		t.Fatalf("上游收到非法 JSON: %v body=%s", err, upstreamReceivedBody)
	}
	so, ok := upstreamBody["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("上游收到的 body 缺 stream_options: %s", upstreamReceivedBody)
	}
	if so["include_usage"] != true {
		t.Errorf("上游 body 的 include_usage=%v, want true", so["include_usage"])
	}

	// 断言 2: 原字段保留(model/stream/messages)
	if upstreamBody["model"] != "glm-5.2" {
		t.Errorf("注入后 model 丢失: %v", upstreamBody["model"])
	}

	// 断言 3: 落库的 prompt 也含 stream_options(一致性)
	recs := waitForRecord(t, spy, 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	var storedPrompt map[string]any
	if err := json.Unmarshal(recs[0].PromptText, &storedPrompt); err != nil {
		t.Fatalf("落库 prompt 非法 JSON: %v", err)
	}
	if so2, ok := storedPrompt["stream_options"].(map[string]any); !ok || so2["include_usage"] != true {
		t.Errorf("落库 prompt 缺 include_usage=true: %s", recs[0].PromptText)
	}

	// 断言 4: usage 被正确采集(因为上游回的是含 usage 的流)
	if recs[0].PromptTokens != 10 || recs[0].CompletionTokens != 5 {
		t.Errorf("usage tokens = %d/%d, want 10/5", recs[0].PromptTokens, recs[0].CompletionTokens)
	}
}

// ============================================================
// 测试 7: 客户端已带 include_usage=true → 网关幂等, 不重复改写
// ============================================================
func TestE2E_Stream_IncludeUsageIdempotent(t *testing.T) {
	var upstreamReceivedBody string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		upstreamReceivedBody = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, `data: [DONE]`+"\n\n")
	})

	proxy, _, _, _ := newTestGateway(t, upstream)

	// 客户端已带 stream_options.include_usage=true
	body := `{"model":"x","stream":true,"stream_options":{"include_usage":true},"messages":[]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-idempotent")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	// 上游收到的 body 应仍是合法 JSON 且 include_usage 仍为 true(未被破坏)
	var upstreamBody map[string]any
	if err := json.Unmarshal([]byte(upstreamReceivedBody), &upstreamBody); err != nil {
		t.Fatalf("上游收到非法 JSON(幂等改写破坏了 body): %v body=%s", err, upstreamReceivedBody)
	}
	so := upstreamBody["stream_options"].(map[string]any)
	if so["include_usage"] != true {
		t.Errorf("幂等后 include_usage=%v, want true", so["include_usage"])
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

// ============================================================
// 测试 8: Anthropic 非流式 /v1/messages → 正确解析 + usage 映射
// 验证: endpoint=messages 时用 Anthropic 解析器, content/usage 正确提取
// ============================================================
func TestE2E_Anthropic_NonStream(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Oneapi-Request-Id", "anth-ns-1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{
			"id":"msg_1","type":"message","role":"assistant","model":"claude-3",
			"content":[{"type":"text","text":"Hello from Claude"}],
			"usage":{"input_tokens":42,"output_tokens":7},
			"stop_reason":"end_turn"
		}`)
	})

	proxy, _, spy, _ := newTestGateway(t, upstream)

	// Anthropic 请求: system 在顶层, messages 里无 system role
	body := `{"model":"claude-3","max_tokens":100,"system":"You are helpful","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-anth-test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	// 客户端应收到原始 Anthropic 响应(透传不改)
	clientResp := rec.Body.String()
	if !strings.Contains(clientResp, "Hello from Claude") {
		t.Errorf("response not forwarded: %s", clientResp)
	}

	recs := waitForRecord(t, spy, 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if got.Endpoint != "messages" {
		t.Errorf("endpoint=%q, want messages", got.Endpoint)
	}
	// usage 映射: input_tokens→prompt_tokens, output_tokens→completion_tokens
	if got.PromptTokens != 42 || got.CompletionTokens != 7 {
		t.Errorf("tokens=%d/%d, want 42/7", got.PromptTokens, got.CompletionTokens)
	}
	// completion 归一化为 OpenAI 形态
	var comp map[string]any
	if err := json.Unmarshal(got.CompletionText, &comp); err != nil {
		t.Fatalf("completion not json: %v", err)
	}
	content := comp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if content != "Hello from Claude" {
		t.Errorf("content=%q, want 'Hello from Claude'", content)
	}
}

// ============================================================
// 测试 9: Anthropic 流式 /v1/messages → SSE 聚合 + usage 映射
// ============================================================
func TestE2E_Anthropic_Stream(t *testing.T) {
	chunks := []string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":50,"output_tokens":1}}}` + "\n\n",
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Streamed"}}` + "\n\n",
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" response"}}` + "\n\n",
		`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n",
		`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}` + "\n\n",
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n",
	}
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	body := `{"model":"claude-3","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-anth-stream")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	// 客户端应收到原始 SSE(透传)
	clientBody := rec.Body.String()
	if !strings.Contains(clientBody, "Streamed") {
		t.Errorf("stream not forwarded: %s", clientBody)
	}

	recs := waitForRecord(t, spy, 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if !got.IsStream {
		t.Error("expected is_stream=true")
	}
	// usage 映射
	if got.PromptTokens != 50 || got.CompletionTokens != 12 {
		t.Errorf("tokens=%d/%d, want 50/12", got.PromptTokens, got.CompletionTokens)
	}
	// 聚合内容
	var comp map[string]any
	json.Unmarshal(got.CompletionText, &comp)
	content := comp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if content != "Streamed response" {
		t.Errorf("content=%q, want 'Streamed response'", content)
	}
}

// ============================================================
// 测试 10: 大请求体(200KB)不被截断(MaxBodyBytes=1MB 回归)
// 验证: 之前 64KB 截断导致 76% 数据损坏, 修复后大请求完整落库
// ============================================================
func TestE2E_LargeBody_NotTruncated(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})

	// 构造大请求体: 200KB 的 system prompt
	bigContent := strings.Repeat("Go语言是一种静态强类型编程语言。", 5000) // ~200KB
	bigBody := fmt.Sprintf(`{"model":"glm-5.2","messages":[{"role":"system","content":%q},{"role":"user","content":"总结"}]}`, bigContent)

	// 用大 MaxBodyBytes 的专用配置
	upSrv := httptest.NewServer(upstream)
	defer upSrv.Close()
	cfg := testConfig()
	cfg.NewAPIBaseURL = upSrv.URL
	cfg.MaxBodyBytes = 1048576 // 1MB

	spy := &mockPersister{}
	callers := audit.NewNoopCallerCache()
	pipeline := audit.NewPipeline(cfg, spy, callers)
	pipeline.Start(context.Background())
	defer pipeline.Shutdown(context.Background())
	transport := NewCaptureTransportExposed(TransportConfig{Base: http.DefaultTransport, Pipeline: pipeline, Cfg: cfg})
	proxy := NewProxy(transport, cfg.NewAPIBaseURL, cfg.MaxBodyBytes)

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(bigBody))
	req.Header.Set("Authorization", "Bearer sk-big-body")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	recs := waitForRecord(t, spy, 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	// 关键: 不应被截断
	if got.Truncated {
		t.Error("200KB body should NOT be truncated with MaxBodyBytes=1MB")
	}
	// prompt 应是合法 JSON(非 {"raw":...} 降级)
	var prompt map[string]any
	if err := json.Unmarshal(got.PromptText, &prompt); err != nil {
		t.Fatalf("prompt not valid json (downgraded?): %v", err)
	}
	// system 分流后, messages 只剩 user(之前截断 bug 导致看不到 user)
	msgs := prompt["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (system split out), got %d", len(msgs))
	}
	if msgs[0].(map[string]any)["role"] != "user" {
		t.Errorf("message role=%v, want user", msgs[0].(map[string]any)["role"])
	}
	// system 被分流到资产层
	if got.SystemPromptHash == "" {
		t.Error("expected system_prompt_hash non-empty (system split out)")
	}
}

// ============================================================
// 测试 11: system prompt 分流 → 资产层去重 + prompt_text 精简
// 验证: 含 system 的请求, system 被抽到 sysPrompts, prompt_text 不含 system
// ============================================================
func TestE2E_SystemSplit_LayeredStorage(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})

	proxy, _, spy, _ := newTestGateway(t, upstream)

	sysContent := "You are a coding agent that writes Go code."
	body := fmt.Sprintf(`{"model":"glm-5.2","messages":[{"role":"system","content":%q},{"role":"user","content":"write hello world"}]}`, sysContent)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-split-test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	recs := waitForRecord(t, spy, 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]

	// 1. system 被分流: prompt_text 不含 system role
	var prompt map[string]any
	json.Unmarshal(got.PromptText, &prompt)
	msgs := prompt["messages"].([]any)
	for _, m := range msgs {
		if m.(map[string]any)["role"] == "system" {
			t.Error("system message should be split out of prompt_text")
		}
	}
	// user 保留
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Error("prompt_text should contain only user message")
	}

	// 2. system_prompt_hash 非空, 指向资产层
	if got.SystemPromptHash == "" {
		t.Error("system_prompt_hash should be non-empty")
	}

	// 3. 资产层有对应的 system prompt
	sysPrompts := spy.snapshotSysPrompts()
	if len(sysPrompts) != 1 {
		t.Fatalf("expected 1 system prompt, got %d", len(sysPrompts))
	}
	sp := sysPrompts[0]
	if sp.Content != sysContent {
		t.Errorf("system content=%q, want %q", sp.Content, sysContent)
	}
	if sp.Hash != got.SystemPromptHash {
		t.Error("system prompt hash mismatch between conversation and sysPrompts")
	}
}

// ============================================================
// 测试 12: 同一 system prompt 多次请求 → 资产层去重(只存 1 份)
// ============================================================
func TestE2E_SystemSplit_Dedup(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	})

	proxy, _, spy, _ := newTestGateway(t, upstream)

	sysContent := "Shared system prompt for dedup test."
	// 发 3 次相同 system 的请求
	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"model":"x","messages":[{"role":"system","content":%q},{"role":"user","content":"q%d"}]}`, sysContent, i)
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer sk-dedup")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
	}

	recs := waitForRecord(t, spy, 3)
	if len(recs) != 3 {
		t.Fatalf("expected 3 records, got %d", len(recs))
	}

	// 3 条对话的 system_prompt_hash 应相同(同一 system)
	hash := recs[0].SystemPromptHash
	if hash == "" {
		t.Fatal("system_prompt_hash empty")
	}
	for i, r := range recs {
		if r.SystemPromptHash != hash {
			t.Errorf("record %d hash=%q, want %q (dedup)", i, r.SystemPromptHash, hash)
		}
	}

	// 资产层只存 1 份(mock 的 UpsertSystemPrompt 模拟 ON CONFLICT 去重)
	sysPrompts := spy.snapshotSysPrompts()
	if len(sysPrompts) != 1 {
		t.Errorf("expected 1 deduped system prompt, got %d", len(sysPrompts))
	}
}

// ============================================================
// 测试 13: 无 system 的请求 → 不触发分流, system_prompt_hash 为空
// ============================================================
func TestE2E_SystemSplit_NoSystem(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	})

	proxy, _, spy, _ := newTestGateway(t, upstream)

	body := `{"model":"x","messages":[{"role":"user","content":"plain question"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-nosys")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	recs := waitForRecord(t, spy, 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if got.SystemPromptHash != "" {
		t.Error("system_prompt_hash should be empty for no-system request")
	}
	if len(spy.snapshotSysPrompts()) != 0 {
		t.Error("no system prompt should be upserted")
	}
}

// ============================================================
// 测试: 流式请求 + 上游返回错误响应(非 SSE) → error_message 落库厂商具体报错
// 覆盖 stream:true 但上游直接回 429/500 + JSON 错误体的场景。
// 修复前: 该场景走 sseCaptureLoop, 厂商错误 JSON 被 aggregator 当 SSE 扫描,
//   因无 "data:" 前缀被全部跳过, error_message 为 NULL, 仅 http_status 正确。
// 修复后: proxy.go 流式分支对 >=400 走 SetError, error_message 含厂商报错。
// ============================================================
func TestE2E_Stream_UpstreamError_BodyCaptured(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 上游返回错误: 不是 SSE 流, 而是 JSON 错误体(厂商限流/模型不存在等典型形态)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limit exceeded","type":"requests","code":"429"}}`)
	})

	proxy, _, spy, _ := newTestGateway(t, upstream)

	body := `{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-stream-err")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	// 客户端应收到原样 429 + 错误体(透传)
	if rec.Code != 429 {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "rate limit exceeded") {
		t.Errorf("error body not forwarded to client: %s", rec.Body.String())
	}

	recs := waitForRecord(t, spy, 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if !got.IsStream {
		t.Error("expected is_stream=true (由请求体决定, 与上游响应格式无关)")
	}
	if got.HTTPStatus != 429 {
		t.Errorf("http_status = %d, want 429", got.HTTPStatus)
	}
	// 关键断言: 厂商具体报错进 error_message
	if !strings.Contains(got.ErrorMessage, "rate limit exceeded") {
		t.Errorf("error_message = %q, want contains 'rate limit exceeded'", got.ErrorMessage)
	}
}

// ============================================================
// 测试: 流式请求 + 流内 SSE error 事件 → error_message 落库
// 覆盖上游正常开始 SSE 流但中途下发 error 事件的场景。
// 修复前: aggregator 提取了 errorMessage 但 Finalize 没回填到 rec.ErrorMessage。
// 修复后: Finalize 把流内 error 事件回填到 error_message。
// ============================================================
func TestE2E_Stream_SSEErrorEvent_Captured(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		// 先正常吐一段内容
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		if fl != nil {
			fl.Flush()
		}
		// 再发 error 事件(厂商模型过载等)
		io.WriteString(w, "data: {\"error\":{\"message\":\"model overloaded\",\"type\":\"server_error\"}}\n\n")
		if fl != nil {
			fl.Flush()
		}
		io.WriteString(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	})

	proxy, _, spy, _ := newTestGateway(t, upstream)

	body := `{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-sse-err")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	recs := waitForRecord(t, spy, 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if !got.IsStream {
		t.Error("expected is_stream=true")
	}
	// 关键断言: 流内 error 事件回填到 error_message
	if !strings.Contains(got.ErrorMessage, "model overloaded") {
		t.Errorf("error_message = %q, want contains 'model overloaded'", got.ErrorMessage)
	}
}
