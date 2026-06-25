// Package audit 实现对话捕获记录的组装、降级、反查、脱敏与落库。
// 核心数据流见 DESIGN.md §5.1~§5.4。
package audit

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/company/llm-gateway/internal/metrics"
)

// Record 是一次对话的完整记录(请求阶段建立骨架, 响应阶段填充 completion 后推送)。
// 对应 db.Conversation。字段语义见 DESIGN.md §5.1.1 / §4.2。
type Record struct {
	// 关联键
	GatewayID          string // 网关自生成, 注入 X-Ctx-Gateway-Id; → request_id 列
	UpstreamRequestID  string // 从 New API 响应头读取; → upstream_request_id 列

	// caller(请求阶段由 enrichCaller 填充)
	TokenKeyHash string // sha256(sk-xxx), 始终记录
	CallerTag    string
	CallerUserID int32
	CallerGroup  string

	// 调用上下文
	Model    string
	Endpoint string
	IsStream bool

	// 内容
	PromptText     json.RawMessage
	CompletionText json.RawMessage
	ToolCalls      json.RawMessage
	RequestBodyHash string

	// 状态
	HTTPStatus       int
	PromptTokens     int32
	CompletionTokens int32
	ErrorMessage     string
	ClientIP         string
	Redacted         bool
	Truncated        bool

	// 性能观测
	UpstreamLatencyMs int32
	TotalLatencyMs    int32

	// 内部状态(不落库)
	mu         sync.Mutex
	agg        *sseAggregator
	startedAt  time.Time
	UpstreamT0 time.Time // transport 填充, 供 handler 计算延迟
	Excluded   bool      // 模型被排除时, handler 跳过捕获但仍透传
}

// NewRecord 在请求阶段构造骨架(gateway_id + 元数据已知, prompt/completion 待填)。
// 设计依据 DESIGN.md §5.1.3:record 创建后, prompt 在 §5.1 body 捕获时填入,
// completion 在响应阶段填入, 然后才 push 到 channel。
func NewRecord(gatewayID string, r *http.Request, endpoint string, isStream bool) *Record {
	rec := &Record{
		GatewayID: gatewayID,
		Endpoint:  endpoint,
		IsStream:  isStream,
		ClientIP:  clientIP(r),
		startedAt: time.Now(),
	}
	if isStream {
		rec.agg = newSSEAggregator()
	}
	return rec
}

// SetPrompt 在请求 body 捕获后(§5.1.3)填充 prompt。
// decoded 是解压后的请求体字节; truncated 表示是否触发 postBodyMaxBytes 截断。
func (rec *Record) SetPrompt(decoded []byte, truncated bool) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.PromptText = json.RawMessage(safeJSON(decoded))
	rec.Truncated = truncated
	rec.RequestBodyHash = sha256Hex(decoded)
	// 顺便从 prompt 中提取 model / is_stream(若尚未确定)
	rec.extractPromptMeta(decoded)
}

// extractPromptMeta 从请求体里尽力提取 model 字段(用于落库 + 限流旁路)。
func (rec *Record) extractPromptMeta(body []byte) {
	var probe struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if json.Unmarshal(body, &probe) == nil {
		if rec.Model == "" {
			rec.Model = probe.Model
		}
		rec.IsStream = probe.Stream
	}
}

// AppendCapture 用于 SSE 流式(§5.2):累积 chunk 到聚合器。
// 永不返回 error(尽力捕获), 超过 maxBytes 后静默丢弃后续 chunk。
func (rec *Record) AppendCapture(chunk []byte, maxBytes int64) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.agg == nil {
		return // 非流式不应调用此方法
	}
	if rec.agg.total > maxBytes {
		rec.Truncated = true
		return
	}
	rec.agg.append(chunk)
}

// Finalize 在响应流结束后(§5.2)组装 completion。
func (rec *Record) Finalize() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.TotalLatencyMs = int32(time.Since(rec.startedAt).Milliseconds())
	if rec.agg != nil {
		rec.CompletionText = rec.agg.completion()
		rec.ToolCalls = rec.agg.toolCalls()
		rec.PromptTokens = rec.agg.promptTokens
		rec.CompletionTokens = rec.agg.completionTokens
	}
}

// SetNonStreamCompletion 用于非流式响应(§5.1 WrapResponseBody 读完后调用)。
func (rec *Record) SetNonStreamCompletion(body []byte) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.CompletionText = json.RawMessage(safeJSON(body))
	// 尝试提取 usage
	var resp struct {
		Usage struct {
			PromptTokens     int32 `json:"prompt_tokens"`
			CompletionTokens int32 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) == nil {
		rec.PromptTokens = resp.Usage.PromptTokens
		rec.CompletionTokens = resp.Usage.CompletionTokens
	}
}

// SetError 填充错误状态(HTTP >= 400 时)。
func (rec *Record) SetError(status int, body []byte) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.HTTPStatus = status
	if status >= 400 {
		rec.ErrorMessage = truncStr(string(body), 4096)
	}
}

// ModelSafe 返回 model 名(空则 "unknown"), 用于 metrics label。
func (rec *Record) ModelSafe() string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.Model == "" {
		return "unknown"
	}
	return rec.Model
}

// StripContent 生成仅含元数据的精简副本(I4 分级背压降级: full channel 满时使用)。
// 保留调用链可追溯, 丢弃 prompt/completion 原文。
func (rec *Record) StripContent() *Record {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return &Record{
		GatewayID:         rec.GatewayID,
		UpstreamRequestID: rec.UpstreamRequestID,
		TokenKeyHash:      rec.TokenKeyHash,
		CallerTag:         rec.CallerTag,
		CallerUserID:      rec.CallerUserID,
		CallerGroup:       rec.CallerGroup,
		Model:             rec.Model,
		Endpoint:          rec.Endpoint,
		IsStream:          rec.IsStream,
		// PromptText / CompletionText 留空 → 落库 NULL
		RequestBodyHash:   rec.RequestBodyHash,
		HTTPStatus:        rec.HTTPStatus,
		PromptTokens:      rec.PromptTokens,
		CompletionTokens:  rec.CompletionTokens,
		ErrorMessage:      rec.ErrorMessage,
		ClientIP:          rec.ClientIP,
		Redacted:          rec.Redacted,
		Truncated:         rec.Truncated,
		UpstreamLatencyMs: rec.UpstreamLatencyMs,
		TotalLatencyMs:    rec.TotalLatencyMs,
	}
}

// NoteOutcome 记录捕获结局(I4)。
func NoteOutcome(outcome string) {
	metrics.CaptureOutcome.WithLabelValues(outcome).Inc()
}

// --- 辅助函数 ---

func clientIP(r *http.Request) string {
	// 优先 X-Forwarded-For 首段, 否则 RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if i := strings.LastIndex(r.RemoteAddr, ":"); i > 0 {
		return r.RemoteAddr[:i]
	}
	return r.RemoteAddr
}

// safeJSON 保证落库的是合法 JSON; 非法则包成 {"raw": "..."}。
func safeJSON(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	if json.Valid(b) {
		return b
	}
	wrapped, _ := json.Marshal(map[string]string{"raw": truncStr(string(b), 32768)})
	return wrapped
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}
