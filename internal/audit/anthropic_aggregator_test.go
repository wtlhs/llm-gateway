package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAnthropicAggregator_ErrorEvent 验证 error 事件被解析到 completion 中。
func TestAnthropicAggregator_ErrorEvent(t *testing.T) {
	a := newAnthropicAggregator()
	a.append([]byte(`event: error` + "\n" + `data: {"type":"error","error":{"type":"rate_limit_error","message":"rate limit"}}` + "\n\n"))

	var out map[string]any
	if err := json.Unmarshal(a.completion(), &out); err != nil {
		t.Fatalf("completion not json: %v", err)
	}
	got, _ := out["error"].(string)
	want := "rate limit (type: rate_limit_error)"
	if got != want {
		t.Errorf("error=%q, want %q", got, want)
	}
}

// TestAnthropicAggregator_TextDelta 流式文本累积 + usage 映射。
func TestAnthropicAggregator_TextDelta(t *testing.T) {
	a := newAnthropicAggregator()
	stream := strings.Join([]string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"usage":{"input_tokens":25,"output_tokens":1}}}` + "\n\n",
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}` + "\n\n",
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", world"}}` + "\n\n",
		`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n",
		`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}` + "\n\n",
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n",
	}, "")

	a.append([]byte(stream))

	// usage 映射
	if a.promptTokens != 25 {
		t.Errorf("promptTokens=%d, want 25", a.promptTokens)
	}
	if a.completionTokens != 10 {
		t.Errorf("completionTokens=%d, want 10", a.completionTokens)
	}
	if a.finishReason != "stop" {
		t.Errorf("finishReason=%q, want stop", a.finishReason)
	}

	// completion 归一化为 OpenAI 形态
	comp := a.completion()
	var out map[string]any
	if err := json.Unmarshal(comp, &out); err != nil {
		t.Fatalf("completion not json: %v", err)
	}
	choices := out["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	content := msg["content"].(string)
	if content != "Hello, world" {
		t.Errorf("content=%q, want 'Hello, world'", content)
	}
	fr := choices[0].(map[string]any)["finish_reason"]
	if fr != "stop" {
		t.Errorf("finish_reason=%v, want stop", fr)
	}
}

// TestAnthropicAggregator_ThinkingDelta 思考链累积(应拼入 content)。
func TestAnthropicAggregator_ThinkingDelta(t *testing.T) {
	a := newAnthropicAggregator()
	stream := strings.Join([]string{
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think..."}}` + "\n\n",
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Answer"}}` + "\n\n",
		`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n",
	}, "")
	a.append([]byte(stream))

	var out map[string]any
	json.Unmarshal(a.completion(), &out)
	choices := out["choices"].([]any)
	content := choices[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	// thinking 应在 content 前
	if !strings.HasPrefix(content, "Let me think...") {
		t.Errorf("thinking not in content: %q", content)
	}
	if !strings.Contains(content, "Answer") {
		t.Errorf("text not in content: %q", content)
	}
}

// TestAnthropicAggregator_ToolUse 工具调用入参分片拼接(input_json_delta)。
func TestAnthropicAggregator_ToolUse(t *testing.T) {
	a := newAnthropicAggregator()
	stream := strings.Join([]string{
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather"}}` + "\n\n",
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"loc"}}` + "\n\n",
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"ation\":\"SF\"}"}}` + "\n\n",
		`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n",
		`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}` + "\n\n",
	}, "")
	a.append([]byte(stream))

	// finish_reason 映射 tool_use → tool_calls
	if a.finishReason != "tool_calls" {
		t.Errorf("finishReason=%q, want tool_calls", a.finishReason)
	}

	// tool_calls 入参拼接完整
	tools := a.toolCalls()
	if tools == nil {
		t.Fatal("expected tool_calls")
	}
	var tcArr []map[string]any
	json.Unmarshal(tools, &tcArr)
	args := tcArr[0]["function"].(map[string]any)["arguments"].(string)
	if args != `{"location":"SF"}` {
		t.Errorf("tool args=%q, want {\"location\":\"SF\"}", args)
	}
}

// TestAnthropicAggregator_MaxTokens stop_reason=max_tokens → length。
func TestAnthropicAggregator_MaxTokens(t *testing.T) {
	a := newAnthropicAggregator()
	a.append([]byte(`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":100}}` + "\n\n"))
	if a.finishReason != "length" {
		t.Errorf("finishReason=%q, want length", a.finishReason)
	}
}

// TestParseAnthropicNonStream 非流式响应解析: text + usage + stop_reason。
func TestParseAnthropicNonStream(t *testing.T) {
	body := []byte(`{
		"id":"msg_1","type":"message","role":"assistant","model":"claude-3",
		"content":[{"type":"text","text":"Hello there"}],
		"usage":{"input_tokens":30,"output_tokens":8},
		"stop_reason":"end_turn"
	}`)
	comp, tools, pTok, cTok, fr := parseAnthropicNonStream(body)
	if pTok != 30 || cTok != 8 {
		t.Errorf("tokens=%d/%d, want 30/8", pTok, cTok)
	}
	if fr != "stop" {
		t.Errorf("finish=%q, want stop", fr)
	}
	if tools != nil {
		t.Error("expected nil tool_calls")
	}
	var out map[string]any
	json.Unmarshal(comp, &out)
	content := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if content != "Hello there" {
		t.Errorf("content=%q", content)
	}
}

// TestParseAnthropicNonStream_ToolUse 非流式含工具调用。
func TestParseAnthropicNonStream_ToolUse(t *testing.T) {
	body := []byte(`{
		"content":[
			{"type":"text","text":"Let me check"},
			{"type":"tool_use","id":"toolu_1","name":"search","input":{"q":"go"}}
		],
		"usage":{"input_tokens":10,"output_tokens":15},
		"stop_reason":"tool_use"
	}`)
	comp, tools, pTok, cTok, fr := parseAnthropicNonStream(body)
	if fr != "tool_calls" {
		t.Errorf("finish=%q, want tool_calls", fr)
	}
	if pTok != 10 || cTok != 15 {
		t.Errorf("tokens=%d/%d", pTok, cTok)
	}
	if tools == nil {
		t.Fatal("expected tool_calls")
	}
	var out map[string]any
	json.Unmarshal(comp, &out)
	content := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if content != "Let me check" {
		t.Errorf("content=%q", content)
	}
}

// TestParseAnthropicNonStream_Invalid 非法 JSON → 零值返回不 panic。
func TestParseAnthropicNonStream_Invalid(t *testing.T) {
	comp, _, pTok, cTok, _ := parseAnthropicNonStream([]byte(`{invalid`))
	if comp != nil || pTok != 0 || cTok != 0 {
		t.Error("expected nil/zero for invalid input")
	}
}
