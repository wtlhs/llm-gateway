package security

import (
	"context"
	"log/slog"
	"strings"

	"github.com/company/llm-gateway/internal/config"
)

// blocklistHandler 包装 slog.Handler, 对命中黑名单的字段值替换为 <redacted>。
// 设计依据 DESIGN.md §9: 防止错误日志打印 prompt/completion 造成二次泄露。
type blocklistHandler struct {
	inner     slog.Handler
	blocklist map[string]struct{}
}

// NewBlocklistHandler 构造。blocklist 为需脱敏的字段名集合。
func NewBlocklistHandler(inner slog.Handler, cfg *config.Config) slog.Handler {
	return &blocklistHandler{inner: inner, blocklist: cfg.SlogBlocklist()}
}

func (h *blocklistHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *blocklistHandler) Handle(ctx context.Context, r slog.Record) error {
	// 复制 record, 对命中字段 redact
	newRec := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if _, hit := h.blocklist[a.Key]; hit {
			a.Value = slog.StringValue("<redacted>")
		}
		// 进一步: 对 deep key(如 request.body)按点号前缀匹配
		newRec.AddAttrs(a)
		return true
	})
	return h.inner.Handle(ctx, newRec)
}

func (h *blocklistHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &blocklistHandler{inner: h.inner.WithAttrs(attrs), blocklist: h.blocklist}
}

func (h *blocklistHandler) WithGroup(name string) slog.Handler {
	return &blocklistHandler{inner: h.inner.WithGroup(name), blocklist: h.blocklist}
}

// keyMatchesBlocklist 处理点号层级 key(如 "request.body" 命中 blocklist "request.body")。
func keyMatchesBlocklist(key string, bl map[string]struct{}) bool {
	if _, ok := bl[key]; ok {
		return true
	}
	// 检查前缀(如 "request.body" 在 bl, 而 key 是 "request.body.content")
	for b := range bl {
		if strings.HasPrefix(key, b) {
			return true
		}
	}
	return false
}
