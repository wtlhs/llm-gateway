// Package audit 实现对话捕获记录的组装、降级、反查、脱敏与落库。
// 核心数据流见 DESIGN.md §5.1~§5.4。
package audit

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/company/llm-gateway/internal/metrics"
)

// Record 是一次对话的完整记录(请求阶段建立骨架, 响应阶段填充 completion 后推送)。
// 对应 db.Conversation。字段语义见 DESIGN.md §5.1.1 / §4.2。
type Record struct {
	// 关联键
	GatewayID          string // 网关自生成, 注入 X-Ctx-Gateway-Id; → request_id 列
	UpstreamRequestID  string // 从 New API 响应头读取; → upstream_request_id 列

	// caller(请求阶段由 enrichCaller 填充)
	TokenKeyHash   string // sha256(sk-xxx), 始终记录
	CallerTag      string // token.Name(令牌名)
	CallerUserName string // 真实用户名(users.username)
	CallerUserID   int32
	CallerGroup    string

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
// decoded 是解压后的请求体字节; tail 是截断时保留的尾部(可能含 model/stream, 用于探测);
// truncated 表示是否触发 postBodyMaxBytes 截断。
// 同时做 system prompt 分流(知识资产层, docs/KNOWLEDGE_LAYER.md §4.1):
// 从请求体提取 system prompt → 单独存(供 pipeline upsert 到 system_prompts 表),
// prompt_text 只保留 user/assistant/tool 消息。
func (rec *Record) SetPrompt(decoded, tail []byte, truncated bool) {
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
	// 截断场景: model/stream 可能在请求体尾部(system 之前/之后), 用 head+tail 拼接探测
	probe := slimmed
	if truncated && len(tail) > 0 {
		probe = append(append([]byte{}, slimmed...), tail...)
	}
	rec.extractPromptMeta(probe)
}

// 顶层 model/stream 字段正则: 用于请求体被 postMax(MaxBodyBytes) 截断后的容错探测。
// model/stream 位于 JSON 头部, 截断后仍完整, 而整体 json.Unmarshal 会因残缺 JSON 失败。
var (
	modelFieldRe  = regexp.MustCompile(`"model"\s*:\s*"([^"]+)"`)
	streamFieldRe = regexp.MustCompile(`"stream"\s*:\s*(true|false)`)
)

// extractPromptMeta 从请求体里尽力提取 model 字段(用于落库 + 限流旁路)。
// 修复(截断误判): 大上下文请求(Anthropic /v1/messages 常 > MaxBodyBytes)被截断后
// json.Unmarshal 整体失败 → stream 探测不到 → is_stream=false → 网关误走非流式路径,
// 流式响应被缓冲, 客户端等不到数据超时断开, New API 侧表现为 client_gone。
// 此处 Unmarshal 失败时回退到正则探测(字段在 JSON 头部, 截断不破坏)。
func (rec *Record) extractPromptMeta(body []byte) {
	var probe struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if json.Unmarshal(body, &probe) != nil {
		if m := modelFieldRe.FindSubmatch(body); m != nil {
			probe.Model = string(m[1])
		}
		if s := streamFieldRe.FindSubmatch(body); s != nil {
			probe.Stream = string(s[1]) == "true"
		}
	}
	if rec.Model == "" {
		rec.Model = probe.Model
	}
	// 流式判定: 若检测到 stream=true, 延迟创建聚合器(NewRecord 阶段 stream 未知, agg 可能为 nil)
	if probe.Stream && !rec.IsStream {
		rec.IsStream = true
		rec.ensureAggregator()
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
// 同时把流内 error 事件(SSE 中的 error)回填到 ErrorMessage(若尚未由 SetError/
// SetStreamError 设置), 使流内错误也能进 error_message 列、被 dashboard 错误计数识别。
func (rec *Record) Finalize() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.TotalLatencyMs = int32(time.Since(rec.startedAt).Milliseconds())
	if rec.isAnthropic && rec.anthAgg != nil {
		rec.CompletionText = rec.anthAgg.completion()
		rec.ToolCalls = rec.anthAgg.toolCalls()
		rec.PromptTokens = rec.anthAgg.promptTokens
		rec.CompletionTokens = rec.anthAgg.completionTokens
		if rec.ErrorMessage == "" && rec.anthAgg.errorMessage != "" {
			rec.ErrorMessage = truncStr(rec.anthAgg.errorMessage, 4096)
		}
	} else if rec.agg != nil {
		rec.CompletionText = rec.agg.completion()
		rec.ToolCalls = rec.agg.toolCalls()
		rec.PromptTokens = rec.agg.promptTokens
		rec.CompletionTokens = rec.agg.completionTokens
		if rec.ErrorMessage == "" && rec.agg.errorMessage != "" {
			rec.ErrorMessage = truncStr(rec.agg.errorMessage, 4096)
		}
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

// SetStreamError 记录流式响应中断时的诊断信息。
// reason 为中断原因(如 "client gone" / "context canceled" / "upstream read error"),
// lastChunk 为上游最后一段原始数据, 可能包含厂商/上游返回的 SSE 错误事件。
func (rec *Record) SetStreamError(status int, reason string, lastChunk []byte) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.HTTPStatus = status
	msg := reason
	if len(lastChunk) > 0 {
		msg += " | last_upstream_chunk: " + truncStr(string(lastChunk), 4096)
	}
	rec.ErrorMessage = truncStr(msg, 4096)
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
		CallerUserName:    rec.CallerUserName,
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
// 额外: json.Valid 只校验语法, 不校验 UTF-8 合法性——含非法 UTF-8 字节的 JSON 会通过
// json.Valid 但被 PostgreSQL JSONB 拒绝(SQLSTATE 22P02), 因此必须同时校验 utf8.Valid。
func safeJSON(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	if json.Valid(b) && utf8.Valid(b) {
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
