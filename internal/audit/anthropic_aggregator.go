package audit

import (
	"bufio"
	"encoding/json"
	"strings"
	"sync"
)

// anthropicAggregator 解析 Anthropic Messages API (/v1/messages) 的 SSE 流。
// 与 OpenAI 格式截然不同:
//   - 双行结构: "event: <type>\ndata: {json}"
//   - 事件流: message_start → content_block_start → content_block_delta(多次)
//     → content_block_stop → message_delta(含最终 usage + stop_reason) → message_stop
//   - delta.type: text_delta(正文)/ thinking_delta(思考)/ input_json_delta(工具入参分片)/ signature_delta
//   - usage: input_tokens(message_start) + output_tokens(message_delta)
//
// 输出归一化为 OpenAI 形态, 落库后无需区分两种格式。
type anthropicAggregator struct {
	mu sync.Mutex

	contentBuf  strings.Builder // text_delta 累积
	thinkingBuf strings.Builder // thinking_delta 累积(归入 content 便于审计)
	total       int64            // 已累积字节, 用于 maxBytes 判断

	// 工具调用: index → 入参 JSON 分片拼接(input_json_delta.partial_json)
	toolIdx map[int]*strings.Builder

	// usage / finish / error
	promptTokens     int32 // 来自 message_start.usage.input_tokens
	completionTokens int32 // 来自 message_delta.usage.output_tokens
	finishReason     string // 来自 message_delta.delta.stop_reason
	stopReason       string // 原始 stop_reason(end_turn/max_tokens/tool_use/...)
	errorMessage     string // error 事件内容
}

func newAnthropicAggregator() *anthropicAggregator {
	return &anthropicAggregator{toolIdx: make(map[int]*strings.Builder)}
}

// append 追加一段原始字节(可能跨多行/多个 event)。
func (a *anthropicAggregator) append(chunk []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := string(chunk)
	a.total += int64(len(s))

	// Anthropic SSE: event 行 + data 行成对出现。
	// 按行扫描, 记录当前 event 类型, 遇到 data 行时带上 event 类型解析。
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	currentEvent := ""
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		a.parseEvent(currentEvent, []byte(payload))
	}
}

// parseEvent 按 event 类型解析 data JSON。
func (a *anthropicAggregator) parseEvent(event string, data []byte) {
	switch event {
	case "message_start":
		a.parseMessageStart(data)
	case "content_block_start":
		a.parseContentBlockStart(data)
	case "content_block_delta":
		a.parseContentBlockDelta(data)
	case "message_delta":
		a.parseMessageDelta(data)
	case "message_stop":
		// 流结束, 无额外数据
	case "error":
		// 错误事件, 记录到 errorMessage 但不影响已累积内容
		var errEvt struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &errEvt) == nil {
			a.errorMessage = errEvt.Error.Message
			if errEvt.Error.Type != "" {
				a.errorMessage += " (type: " + errEvt.Error.Type + ")"
			}
		} else {
			a.errorMessage = string(data)
		}
	}
}

// parseMessageStart 提取 input_tokens(model 在 message_start 里也有, 但 model 由 prompt 侧已提取)。
// usage 兜底: 部分上游(如 GLM Anthropic 兼容层) input_tokens 恒为 0, 但可能把真实值
// 放在 usage.cache_creation.input_tokens / usage.cache_read.input_tokens(Anthropic 规范字段),
// 此时求和作为 prompt_tokens。若两者都缺失/为 0, 由 Finalize 的本地估算兜底。
func (a *anthropicAggregator) parseMessageStart(data []byte) {
	var msg struct {
		Message struct {
			Usage struct {
				InputTokens    int32 `json:"input_tokens"`
				CacheCreation  *struct {
					InputTokens int32 `json:"input_tokens"`
				} `json:"cache_creation"`
				CacheRead *struct {
					InputTokens int32 `json:"input_tokens"`
				} `json:"cache_read"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &msg) != nil {
		return
	}
	a.promptTokens = msg.Message.Usage.InputTokens
	if a.promptTokens == 0 && (msg.Message.Usage.CacheCreation != nil || msg.Message.Usage.CacheRead != nil) {
		sum := int32(0)
		if msg.Message.Usage.CacheCreation != nil {
			sum += msg.Message.Usage.CacheCreation.InputTokens
		}
		if msg.Message.Usage.CacheRead != nil {
			sum += msg.Message.Usage.CacheRead.InputTokens
		}
		if sum > 0 {
			a.promptTokens = sum
		}
	}
}

// parseContentBlockStart 初始化 tool_use 块的入参缓冲(若该块是 tool_use)。
func (a *anthropicAggregator) parseContentBlockStart(data []byte) {
	var blk struct {
		Index        int    `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
	}
	if json.Unmarshal(data, &blk) == nil && blk.ContentBlock.Type == "tool_use" {
		if _, ok := a.toolIdx[blk.Index]; !ok {
			a.toolIdx[blk.Index] = &strings.Builder{}
		}
	}
}

// parseContentBlockDelta 按 delta.type 分流累积。
func (a *anthropicAggregator) parseContentBlockDelta(data []byte) {
	var blk struct {
		Index int `json:"index"`
		Delta struct {
			Type      string `json:"type"`
			Text      string `json:"text"`        // text_delta
			Thinking  string `json:"thinking"`    // thinking_delta
			PartialJSON string `json:"partial_json"` // input_json_delta
			Signature string `json:"signature"`    // signature_delta
		} `json:"delta"`
	}
	if json.Unmarshal(data, &blk) != nil {
		return
	}
	switch blk.Delta.Type {
	case "text_delta":
		a.contentBuf.WriteString(blk.Delta.Text)
	case "thinking_delta":
		a.thinkingBuf.WriteString(blk.Delta.Thinking)
	case "input_json_delta":
		// 工具入参分片, 按 index 拼接
		if b, ok := a.toolIdx[blk.Index]; ok {
			b.WriteString(blk.Delta.PartialJSON)
		}
	case "signature_delta":
		// 签名通常一次性给出, 不归入 completion(回传时需原样带, 但审计不存)
	}
}

// parseMessageDelta 提取最终 output_tokens + stop_reason。
func (a *anthropicAggregator) parseMessageDelta(data []byte) {
	var msg struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Usage struct {
			OutputTokens int32 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &msg) == nil {
		a.completionTokens = msg.Usage.OutputTokens
		if msg.Delta.StopReason != "" {
			a.stopReason = msg.Delta.StopReason
			a.finishReason = mapStopReason(msg.Delta.StopReason)
		}
	}
}

// mapStopReason 将 Anthropic stop_reason 映射到 OpenAI finish_reason 等价物。
func mapStopReason(sr string) string {
	switch sr {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "stop_sequence":
		return "stop"
	default:
		return sr // pause_turn/refused 等少见值原样保留
	}
}

// completion 返回归一化为 OpenAI 形态的 completion_text。
// thinking 内容拼在 content 前(便于审计完整 assistant 输出, 与 OpenAI 聚合器处理 reasoning_content 一致)。
func (a *anthropicAggregator) completion() []byte {
	content := a.thinkingBuf.String() + a.contentBuf.String()
	out := map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": a.finishReason,
		}},
	}
	if a.errorMessage != "" {
		out["error"] = a.errorMessage
	}
	b, _ := json.Marshal(out)
	return b
}

// toolCalls 返回聚合后的 tool_calls 数组(按 index), 或 nil。
func (a *anthropicAggregator) toolCalls() []byte {
	if len(a.toolIdx) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(a.toolIdx))
	for idx, b := range a.toolIdx {
		out = append(out, map[string]any{
			"index": idx,
			"function": map[string]any{
				"arguments": b.String(),
			},
		})
	}
	res, _ := json.Marshal(out)
	return res
}

// parseAnthropicNonStream 解析 Anthropic 非流式响应, 填充 completion 归一化结构 + usage。
// 返回 (completionJSON, toolCallsJSON, promptTokens, completionTokens, finishReason)。
func parseAnthropicNonStream(body []byte) ([]byte, []byte, int32, int32, string) {
	var resp struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int32 `json:"input_tokens"`
			OutputTokens int32 `json:"output_tokens"`
		} `json:"usage"`
		StopReason string `json:"stop_reason"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return nil, nil, 0, 0, ""
	}

	// 拼接 text + thinking(thinking 在前)
	var textBuf, thinkingBuf strings.Builder
	var toolCalls []map[string]any
	for _, c := range resp.Content {
		switch c.Type {
		case "text":
			textBuf.WriteString(c.Text)
		case "thinking":
			thinkingBuf.WriteString(c.Thinking)
		case "tool_use":
			toolCalls = append(toolCalls, map[string]any{
				"id":   c.ID,
				"type": "function",
				"function": map[string]any{
					"name":      c.Name,
					"arguments": string(c.Input),
				},
			})
		}
	}

	content := thinkingBuf.String() + textBuf.String()
	finish := mapStopReason(resp.StopReason)
	out := map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": finish,
		}},
	}
	compJSON, _ := json.Marshal(out)

	var toolJSON []byte
	if len(toolCalls) > 0 {
		toolJSON, _ = json.Marshal(toolCalls)
	}
	return compJSON, toolJSON, resp.Usage.InputTokens, resp.Usage.OutputTokens, finish
}
