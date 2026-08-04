package audit

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSetStreamError_ReasonOnly 验证仅原因时的落库字段。
func TestSetStreamError_ReasonOnly(t *testing.T) {
	rec := &Record{}
	rec.SetStreamError(499, "client gone: write tcp broken pipe", nil)
	if rec.HTTPStatus != 499 {
		t.Errorf("http_status=%d, want 499", rec.HTTPStatus)
	}
	if !strings.HasPrefix(rec.ErrorMessage, "client gone:") {
		t.Errorf("error_message=%q", rec.ErrorMessage)
	}
}

// TestSetStreamError_WithLastChunk 验证最后一段上游数据被追加到 error_message。
func TestSetStreamError_WithLastChunk(t *testing.T) {
	rec := &Record{}
	last := []byte(`data: {"error":{"message":"model overloaded"}}`)
	rec.SetStreamError(502, "upstream read error: broken pipe", last)
	if !strings.Contains(rec.ErrorMessage, "upstream read error:") {
		t.Errorf("missing reason: %q", rec.ErrorMessage)
	}
	if !strings.Contains(rec.ErrorMessage, "model overloaded") {
		t.Errorf("missing last chunk: %q", rec.ErrorMessage)
	}
}

// TestSetStreamError_Truncation 验证 error_message 总体被截断到 4096 字节。
func TestSetStreamError_Truncation(t *testing.T) {
	rec := &Record{}
	big := strings.Repeat("x", 5000)
	rec.SetStreamError(502, "reason", []byte(big))
	if len(rec.ErrorMessage) > 4096+len("...[truncated]") {
		t.Errorf("error_message too long: %d", len(rec.ErrorMessage))
	}
	if !strings.HasSuffix(rec.ErrorMessage, "...[truncated]") {
		t.Errorf("expected truncation suffix: %q", rec.ErrorMessage)
	}
}

// TestExtractPromptMeta_TruncatedBody 回归: 请求体被 postMax 截断后仍能探测 model/stream。
// 背景: 大上下文请求(Anthropic /v1/messages > MaxBodyBytes)截断后 json.Unmarshal 整体失败,
// 曾导致 is_stream 误判 false → 网关走非流式路径缓冲 SSE → 客户端超时断开 → New API client_gone。
func TestExtractPromptMeta_TruncatedBody(t *testing.T) {
	rec := &Record{}
	// 截断的合法前缀: model/stream 完整, 但 JSON 残缺(截断点位于 messages 内)
	truncated := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true,"x":123`)
	rec.extractPromptMeta(truncated)
	if rec.Model != "glm-5.2" {
		t.Errorf("model=%q, want glm-5.2", rec.Model)
	}
	if !rec.IsStream {
		t.Error("is_stream should be true (stream field present in truncated head)")
	}
	if rec.agg == nil {
		t.Error("aggregator should be created when stream detected")
	}
}

// TestExtractPromptMeta_TruncatedBody_NonStream 截断且 stream=false 时不误判为流式。
func TestExtractPromptMeta_TruncatedBody_NonStream(t *testing.T) {
	rec := &Record{}
	truncated := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":false,"x":123`)
	rec.extractPromptMeta(truncated)
	if rec.Model != "glm-5.2" {
		t.Errorf("model=%q, want glm-5.2", rec.Model)
	}
	if rec.IsStream {
		t.Error("is_stream should stay false")
	}
}

// TestExtractPromptMeta_CompleteBody 完整 JSON 走原 Unmarshal 路径, 行为不回退。
func TestExtractPromptMeta_CompleteBody(t *testing.T) {
	rec := &Record{}
	full := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	rec.extractPromptMeta(full)
	if rec.Model != "gpt-4o" || !rec.IsStream {
		t.Errorf("model=%q stream=%v", rec.Model, rec.IsStream)
	}
}

// TestSetPrompt_TailProbe 回归: 请求体被截断且 model/stream 位于尾部
// (Claude Code /v1/messages 格式, context_management 前置), head+tail 拼接探测
// 必须识别流式, 否则走非流式路径缓冲 SSE → 客户端超时 → New API client_gone。
func TestSetPrompt_TailProbe(t *testing.T) {
	rec := &Record{}
	rec.isAnthropic = true // endpoint=messages
	head := []byte(`{"context_management":{"edits":[]},"max_tokens":64000,"messages":[{"role":"user","content":"`)
	filler := strings.Repeat("x", 50000)
	tail := []byte(`"}],"model":"glm-5.2","stream":true}`)
	decoded := append(append(append([]byte{}, head...), []byte(filler)...), tail...)

	// 模拟网关: 截断为前 1000 字节, tail 为后 1000 字节
	rec.SetPrompt(decoded[:1000], decoded[len(decoded)-1000:], true)
	if rec.Model != "glm-5.2" {
		t.Errorf("model=%q, want glm-5.2 (tail probe)", rec.Model)
	}
	if !rec.IsStream {
		t.Error("is_stream should be true (stream in tail)")
	}
	if rec.anthAgg == nil {
		t.Error("anthropic aggregator should be created (endpoint=messages)")
	}
}

// TestSetPrompt_TailProbe_NonStream 尾部 stream=false 不误判为流式。
func TestSetPrompt_TailProbe_NonStream(t *testing.T) {
	rec := &Record{}
	rec.isAnthropic = true // endpoint=messages
	head := []byte(`{"context_management":{"edits":[]},"max_tokens":64000,"messages":[{"role":"user","content":"`)
	filler := strings.Repeat("x", 50000)
	tail := []byte(`"}],"model":"glm-5.2","stream":false}`)
	decoded := append(append(append([]byte{}, head...), []byte(filler)...), tail...)

	rec.SetPrompt(decoded[:1000], decoded[len(decoded)-1000:], true)
	if rec.Model != "glm-5.2" {
		t.Errorf("model=%q, want glm-5.2", rec.Model)
	}
	if rec.IsStream {
		t.Error("is_stream should stay false (stream=false in tail)")
	}
}

// TestSafeJSON_InvalidUTF8 回归: json.Valid 对含非法 UTF-8 字节的 JSON 返回 true,
// 但 PostgreSQL JSONB 会拒绝(SQLSTATE 22P02)导致 insert failed。
// safeJSON 必须同时校验 utf8.Valid, 非法字节应包成 {"raw": "..."} 而非原样返回。
func TestSafeJSON_InvalidUTF8(t *testing.T) {
	// 语法合法但含非法 UTF-8 字节(0xff 0xfe)
	bad := []byte{'"', 'a', 0xff, 0xfe, '"'}
	if !json.Valid(bad) {
		t.Fatal("precondition: json.Valid should be true for syntax-valid input")
	}
	out := safeJSON(bad)
	if !json.Valid(out) || !utf8.Valid(out) {
		t.Fatalf("safeJSON output must be valid UTF-8 JSON, got %q", out)
	}
	if string(out) == string(bad) {
		t.Fatalf("safeJSON should wrap invalid-UTF8 input, returned raw: %q", out)
	}
	// 常规合法 JSON 不回退
	ok := []byte(`{"model":"gpt-4o"}`)
	if got := safeJSON(ok); string(got) != string(ok) {
		t.Fatalf("safeJSON should pass through valid input, got %q", got)
	}
}
