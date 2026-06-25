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
