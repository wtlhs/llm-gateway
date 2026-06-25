package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSSEAggregator_DoneSentinel 验证 [DONE] 哨兵识别。
func TestSSEAggregator_DoneSentinel(t *testing.T) {
	a := newSSEAggregator()
	a.append([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
	a.append([]byte("data: [DONE]\n\n"))

	if !a.done {
		t.Fatal("expected done=true after [DONE]")
	}
	if !strings.Contains(string(a.completion()), `"hi"`) {
		t.Fatalf("completion missing content: %s", a.completion())
	}
}

// TestSSEAggregator_StreamingDeltas 验证多 chunk delta 拼接 + usage + finish_reason。
func TestSSEAggregator_StreamingDeltas(t *testing.T) {
	a := newSSEAggregator()
	chunks := []string{
		`data: {"choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"choices":[{"delta":{"content":", "}}]}`,
		`data: {"choices":[{"delta":{"content":"world!"},"finish_reason":"stop"}]}`,
		`data: {"usage":{"prompt_tokens":5,"completion_tokens":3}}`,
		`data: [DONE]`,
	}
	for _, c := range chunks {
		a.append([]byte(c + "\n\n"))
	}

	var got struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(a.completion(), &got); err != nil {
		t.Fatalf("completion not valid json: %v", err)
	}
	if len(got.Choices) != 1 || got.Choices[0].Message.Content != "Hello, world!" {
		t.Fatalf("aggregated content wrong: %+v", got)
	}
	if got.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason wrong: %s", got.Choices[0].FinishReason)
	}
	if a.promptTokens != 5 || a.completionTokens != 3 {
		t.Fatalf("usage wrong: prompt=%d completion=%d", a.promptTokens, a.completionTokens)
	}
}

// TestSSEAggregator_ReasoningContent 验证 reasoning_content 也归入 completion。
func TestSSEAggregator_ReasoningContent(t *testing.T) {
	a := newSSEAggregator()
	a.append([]byte(`data: {"choices":[{"delta":{"reasoning_content":"thinking..."}}]}` + "\n"))
	a.append([]byte(`data: [DONE]` + "\n"))
	if !strings.Contains(string(a.completion()), "thinking...") {
		t.Fatal("reasoning content not captured")
	}
}

// TestSafeJSON_NonJSON 验证非 JSON body 被安全包裹。
func TestSafeJSON_NonJSON(t *testing.T) {
	out := safeJSON([]byte("not json at all"))
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("safeJSON output not valid: %v (out=%s)", err, out)
	}
	if _, ok := m["raw"]; !ok {
		t.Fatal("expected wrapped under 'raw'")
	}
}

// TestSafeJSON_Empty 空字节应为 {}。
func TestSafeJSON_Empty(t *testing.T) {
	out := safeJSON(nil)
	if string(out) != "{}" {
		t.Fatalf("expected {}, got %s", out)
	}
}
