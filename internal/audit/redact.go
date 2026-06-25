package audit

import (
	"regexp"

	"github.com/company/llm-gateway/internal/config"
	"github.com/company/llm-gateway/internal/metrics"
)

// 脱敏规则集(DESIGN.md §9 脱敏规则明细)。
// 注意:脱敏只在落库前应用; 透传给 New API 的永远是原始内容。
var redactRules = []struct {
	name   string
	re     *regexp.Regexp
	replace []byte
}{
	{"phone", regexp.MustCompile(`\b1[3-9]\d{9}\b`), []byte("138****1234")},
	// 身份证 18 位(末位 X)
	{"idcard", regexp.MustCompile(`\b\d{17}[\dXx]\b`), []byte("110101****1234")},
	{"email", regexp.MustCompile(`\b[\w.]+@[\w.]+\.\w+\b`), []byte("a***@example.com")},
	{"bankcard", regexp.MustCompile(`\b\d{16,19}\b`), []byte("****4321")},
	{"apikey", regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`), []byte("sk-***")},
}

// Apply 按 LLM_AUDIT_MODE 对 record 应用脱敏。
//   - ModeFull:  不脱敏(仅受控环境)
//   - ModeRedact: 对 prompt_text / completion_text 应用规则, redacted=true
//   - ModeOff:   调用方应在 pipeline 跳过落库, 但此处也保证不会误存
func Apply(rec *Record, mode config.Mode) {
	if mode != config.ModeRedact {
		return
	}
	if len(rec.PromptText) > 0 {
		rec.PromptText = redactBytes(rec.PromptText)
	}
	if len(rec.CompletionText) > 0 {
		rec.CompletionText = redactBytes(rec.CompletionText)
	}
	rec.Redacted = true
}

func redactBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	for _, r := range redactRules {
		count := len(r.re.FindAll(out, -1))
		if count > 0 {
			metrics.RedactApplied.WithLabelValues(r.name).Add(float64(count))
		}
		out = r.re.ReplaceAll(out, r.replace)
	}
	return out
}
