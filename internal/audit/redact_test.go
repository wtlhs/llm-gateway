package audit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/company/llm-gateway/internal/config"
)

// TestApply_RedactPhoneEmail 验证脱敏规则生效(§9)。
func TestApply_RedactPhoneEmail(t *testing.T) {
	body := []byte(`{"messages":[{"content":"call me 13812345678 or a@b.com"}]}`)
	rec := &Record{PromptText: body}

	Apply(rec, config.ModeRedact)

	if !rec.Redacted {
		t.Fatal("expected redacted=true")
	}
	s := string(rec.PromptText)
	if strings.Contains(s, "13812345678") {
		t.Fatalf("phone not redacted: %s", s)
	}
	if strings.Contains(s, "a@b.com") {
		t.Fatalf("email not redacted: %s", s)
	}
	// 脱敏后仍是合法 JSON
	if !json.Valid(rec.PromptText) {
		t.Fatalf("redacted output not valid json: %s", rec.PromptText)
	}
}

// TestApply_ModeFullSkips full 模式不脱敏。
func TestApply_ModeFullSkips(t *testing.T) {
	orig := []byte(`{"x":"13812345678"}`)
	rec := &Record{PromptText: orig}
	Apply(rec, config.ModeFull)
	if rec.Redacted {
		t.Fatal("full mode should not redact")
	}
	if string(rec.PromptText) != string(orig) {
		t.Fatal("full mode altered content")
	}
}

// TestApply_APIKey 验证 sk- key 脱敏。
func TestApply_APIKey(t *testing.T) {
	body := []byte(`{"key":"sk-abcdefghijklmnopqrstuvwxyz1234567890"}`)
	rec := &Record{PromptText: body}
	Apply(rec, config.ModeRedact)
	if strings.Contains(string(rec.PromptText), "sk-abcdefghijklmnopqrstuvwxyz1234567890") {
		t.Fatal("api key not redacted")
	}
}

// TestApply_RedactDoesNotBreakJSON 回归: 脱敏是字节级正则替换, bankcard 规则
// `\b\d{16,19}\b` 可能误匹配 JSON 数字字面量(如 16 位 max_tokens), 替换成
// `****4321` 会破坏 JSON 结构(PG JSONB 拒绝, SQLSTATE 22P02)。
// 脱敏后必须重新过 safeJSON, 保证落库永远是合法 JSON。
func TestApply_RedactDoesNotBreakJSON(t *testing.T) {
	// JSON 数字字面量恰好 16 位(触发 bankcard 规则) + 含控制字符的字符串值
	body := []byte(`{"max_tokens":1234567890123456,"note":"Co-Authored-By: Claude Opus 4.8 (1M context) \a"}`)
	rec := &Record{PromptText: body}
	Apply(rec, config.ModeRedact)
	if !json.Valid(rec.PromptText) {
		t.Fatalf("redacted output must stay valid JSON, got: %q", rec.PromptText)
	}
	// 包装模式下数据不丢(仍可找到原内容片段)
	if len(rec.PromptText) == 0 {
		t.Fatal("prompt text should not be empty")
	}
}

// TestApply_CompletionRedactSafe 验证 completion_text 脱敏后同样保持合法 JSON。
func TestApply_CompletionRedactSafe(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"bank 12345678901234567890 ok"}}]}`)
	rec := &Record{CompletionText: body}
	Apply(rec, config.ModeRedact)
	if !json.Valid(rec.CompletionText) {
		t.Fatalf("redacted completion must stay valid JSON, got: %q", rec.CompletionText)
	}
}
