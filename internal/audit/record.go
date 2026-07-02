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

	// 知识资产层引用(docs/KNOWLEDGE_LAYER.md §3.2)
	SystemPromptHash   string // sha256(content), 老数据为空
	SystemPromptSize   int    // system prompt 字节数
	SystemPromptText   string // system prompt 原文(供 pipeline upsert, 不落库到本表)
	SystemPromptAgent  string // 启发式提取的 agent 名(供 pipeline upsert)

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
	mu          sync.Mutex
	agg         *sseAggregator          // OpenAI SSE 聚合器
	anthAgg     *anthropicAggregator    // Anthropic SSE 聚合器(endpoint=messages 时使用)
	isAnthropic bool                    // /v1/messages 端点, 用 Anthropic 格式解析
	startedAt   time.Time
	UpstreamT0  time.Time // transport 填充, 供 handler 计算延迟
	Excluded    bool      // 模型被排除时, handler 跳过捕获但仍透传
}

// NewRecord 在请求阶段构造骨架(gateway_id + 元数据已知, prompt/completion 待填)。
// 设计依据 DESIGN.md §5.1.3:record 创建后, prompt 在 §5.1 body 捕获时填入,
// completion 在响应阶段填入, 然后才 push 到 channel。
// endpoint=messages 时标记 isAnthropic, 后续用 Anthropic 格式解析器。
func NewRecord(gatewayID string, r *http.Request, endpoint string, isStream bool) *Record {
	rec := &Record{
		GatewayID: gatewayID,
		Endpoint:  endpoint,
		IsStream:  isStream,
		ClientIP:  clientIP(r),
		startedAt: time.Now(),
	}
	rec.isAnthropic = isAnthropicEndpoint(endpoint)
	if isStream {
		if rec.isAnthropic {
			rec.anthAgg = newAnthropicAggregator()
		} else {
			rec.agg = newSSEAggregator()
		}
	}
	return rec
}

// isAnthropicEndpoint 判定是否用 Anthropic 格式解析(/v1/messages 端点)。
func isAnthropicEndpoint(endpoint string) bool {
	return endpoint == "messages"
}

// SetPrompt 在请求 body 捕获后(§5.1.3)填充 prompt。
// decoded 是解压后的请求体字节; truncated 表示是否触发 postBodyMaxBytes 截断。
// 同时做 system prompt 分流(知识资产层, docs/KNOWLEDGE_LAYER.md §4.1):
// 从请求体提取 system prompt → 单独存(供 pipeline upsert 到 system_prompts 表),
// prompt_text 只保留 user/assistant/tool 消息。
func (rec *Record) SetPrompt(decoded []byte, truncated bool) {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	// system 分流: 若是合法 JSON 且含 system, 提取并精简请求体
	slimmed, sys := splitSystemPrompt(decoded)
	if sys.Has {
		rec.SystemPromptText = sys.Content
		rec.SystemPromptSize = len(sys.Content)
		rec.SystemPromptHash = sha256Hex([]byte(sys.Content))
		rec.SystemPromptAgent = extractAgentName(sys.Content, rec.CallerTag)
		// request_body_hash 用原始请求体(完整, 含 system), 保证语义不变
		rec.RequestBodyHash = sha256Hex(decoded)
	} else {
		rec.RequestBodyHash = sha256Hex(decoded)
	}

	rec.PromptText = json.RawMessage(safeJSON(slimmed))
	rec.Truncated = truncated
	// 从(精简后的)prompt 提取 model / is_stream
	rec.extractPromptMeta(slimmed)
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
		// 流式判定: 若检测到 stream=true, 延迟创建聚合器(NewRecord 阶段 stream 未知, agg 可能为 nil)
		if probe.Stream && !rec.IsStream {
			rec.IsStream = true
			rec.ensureAggregator()
		}
	}
}

// ensureAggregator 按端点格式创建对应的流式聚合器(延迟创建场景共用)。
func (rec *Record) ensureAggregator() {
	if rec.isAnthropic {
		if rec.anthAgg == nil {
			rec.anthAgg = newAnthropicAggregator()
		}
	} else {
		if rec.agg == nil {
			rec.agg = newSSEAggregator()
		}
	}
}

// AppendCapture 用于 SSE 流式(§5.2):累积 chunk 到聚合器。
// 永不返回 error(尽力捕获), 超过 maxBytes 后静默丢弃后续 chunk。
// 按端点格式分流到 OpenAI 或 Anthropic 聚合器。
func (rec *Record) AppendCapture(chunk []byte, maxBytes int64) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	// 防御: 若聚合器未建(SetPrompt 没提取到 stream 字段的边界), 此处创建
	rec.ensureAggregator()
	if rec.isAnthropic {
		if rec.anthAgg.total > maxBytes {
			rec.Truncated = true
			return
		}
		rec.anthAgg.append(chunk)
	} else {
		if rec.agg.total > maxBytes {
			rec.Truncated = true
			return
		}
		rec.agg.append(chunk)
	}
}

// Finalize 在响应流结束后(§5.2)组装 completion。
// 按端点格式从对应聚合器提取归一化结果。
func (rec *Record) Finalize() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.TotalLatencyMs = int32(time.Since(rec.startedAt).Milliseconds())
	if rec.isAnthropic && rec.anthAgg != nil {
		rec.CompletionText = rec.anthAgg.completion()
		rec.ToolCalls = rec.anthAgg.toolCalls()
		rec.PromptTokens = rec.anthAgg.promptTokens
		rec.CompletionTokens = rec.anthAgg.completionTokens
	} else if rec.agg != nil {
		rec.CompletionText = rec.agg.completion()
		rec.ToolCalls = rec.agg.toolCalls()
		rec.PromptTokens = rec.agg.promptTokens
		rec.CompletionTokens = rec.agg.completionTokens
	}
}

// SetNonStreamCompletion 用于非流式响应(§5.1 WrapResponseBody 读完后调用)。
// 按端点格式分流: Anthropic(/v1/messages)用专属解析器, 其他用 OpenAI 格式。
func (rec *Record) SetNonStreamCompletion(body []byte) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.isAnthropic {
		comp, tools, pTok, cTok, _ := parseAnthropicNonStream(body)
		if comp != nil {
			rec.CompletionText = json.RawMessage(comp)
		} else {
			rec.CompletionText = json.RawMessage(safeJSON(body))
		}
		if tools != nil {
			rec.ToolCalls = json.RawMessage(tools)
		}
		rec.PromptTokens = pTok
		rec.CompletionTokens = cTok
		return
	}
	rec.CompletionText = json.RawMessage(safeJSON(body))
	// 尝试提取 usage(OpenAI 格式)
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
		// 知识资产层引用保留(hash 指向 system_prompts, 即使正文丢弃仍可还原)
		SystemPromptHash: rec.SystemPromptHash,
		SystemPromptSize: rec.SystemPromptSize,
		// SystemPromptText 丢弃(降级副本不重存 system 原文)
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
// 注意: raw 保留全文不二次截断——截断已由 decodeMaybe 的 postMax(MaxBodyBytes)统一控制,
// 这里再截会破坏数据完整性(coding agent 的 system prompt 会被砍, 看不到用户问题)。
// 若原文超长, decodeMaybe 已在上游截断并标记 truncated, 此处只负责包装不合法的残缺 JSON。
func safeJSON(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	if json.Valid(b) {
		return b
	}
	wrapped, _ := json.Marshal(map[string]string{"raw": string(b)})
	return wrapped
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}
