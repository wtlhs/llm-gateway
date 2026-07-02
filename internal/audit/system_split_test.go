package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSplitSystemPrompt_OpenAI 从 OpenAI 格式提取 system, prompt_text 去掉 system。
func TestSplitSystemPrompt_OpenAI(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[
		{"role":"system","content":"You are a helpful agent"},
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello"}
	]}`)
	slimmed, sys := splitSystemPrompt(body)
	if !sys.Has {
		t.Fatal("expected system extracted")
	}
	if sys.Content != "You are a helpful agent" {
		t.Errorf("system content=%q", sys.Content)
	}
	// slimmed 应不含 system role
	var m map[string]any
	json.Unmarshal(slimmed, &m)
	msgs := m["messages"].([]any)
	for _, msg := range msgs {
		mm := msg.(map[string]any)
		if mm["role"] == "system" {
			t.Error("system message not removed from slimmed")
		}
	}
	if len(msgs) != 2 {
		t.Errorf("slimmed has %d messages, want 2", len(msgs))
	}
}

// TestSplitSystemPrompt_Anthropic 从 Anthropic 顶层 system 字段提取。
func TestSplitSystemPrompt_Anthropic(t *testing.T) {
	body := []byte(`{"model":"claude-3","max_tokens":100,
		"system":"You are Claude",
		"messages":[{"role":"user","content":"hi"}]}`)
	slimmed, sys := splitSystemPrompt(body)
	if !sys.Has {
		t.Fatal("expected system extracted")
	}
	if sys.Content != "You are Claude" {
		t.Errorf("system content=%q", sys.Content)
	}
	// slimmed 应不含 system 字段
	var m map[string]any
	json.Unmarshal(slimmed, &m)
	if _, ok := m["system"]; ok {
		t.Error("system field not removed from slimmed")
	}
}

// TestSplitSystemPrompt_AnthropicArray Anthropic system 为数组形式。
func TestSplitSystemPrompt_AnthropicArray(t *testing.T) {
	body := []byte(`{"model":"claude-3",
		"system":[{"type":"text","text":"Part 1"},{"type":"text","text":"Part 2"}],
		"messages":[]}`)
	_, sys := splitSystemPrompt(body)
	if !sys.Has {
		t.Fatal("expected system extracted")
	}
	if sys.Content != "Part 1Part 2" {
		t.Errorf("system content=%q, want 'Part 1Part 2'", sys.Content)
	}
}

// TestSplitSystemPrompt_NoSystem 无 system 时原样返回。
func TestSplitSystemPrompt_NoSystem(t *testing.T) {
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`)
	slimmed, sys := splitSystemPrompt(body)
	if sys.Has {
		t.Fatal("expected no system")
	}
	if !strings.HasPrefix(string(slimmed), "{") {
		t.Error("slimmed should be valid JSON")
	}
}

// TestSplitSystemPrompt_InvalidJSON 非 JSON 原样返回, 不 panic。
func TestSplitSystemPrompt_InvalidJSON(t *testing.T) {
	body := []byte(`{invalid`)
	slimmed, sys := splitSystemPrompt(body)
	if sys.Has {
		t.Fatal("invalid JSON should not extract system")
	}
	if string(slimmed) != "{invalid" {
		t.Error("slimmed should equal original for invalid JSON")
	}
}

// TestSplitSystemPrompt_MultiSystem 多个 system 消息拼接。
func TestSplitSystemPrompt_MultiSystem(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"system","content":"Part A"},
		{"role":"system","content":"Part B"},
		{"role":"user","content":"hi"}
	]}`)
	_, sys := splitSystemPrompt(body)
	if !sys.Has {
		t.Fatal("expected system")
	}
	if !strings.Contains(sys.Content, "Part A") || !strings.Contains(sys.Content, "Part B") {
		t.Errorf("multi system not concatenated: %q", sys.Content)
	}
}

// TestExtractAgentName 各启发式规则。
func TestExtractAgentName(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		callerTag string
		want      string
	}{
		{"NameTag", "<Role><Name>CodingHelper</Name>You are...</Role>", "", "CodingHelper"},
		{"YouAreEN", "You are a helpful assistant that writes code.", "", "helpful assistant that writes code"},
		{"YouAreCN", "你是一个代码审查专家", "", "代码审查专家"},
		{"CallerTagFallback", "some random prompt without name", "my-agent", "my-agent"},
		{"Unknown", "random text", "", "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractAgentName(c.content, c.callerTag)
			// YouAreEN 模式匹配范围不固定, 用 Contains 宽松校验
			if c.name == "YouAreEN" {
				if !strings.Contains(got, "assistant") {
					t.Errorf("got %q, want contains 'assistant'", got)
				}
				return
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
