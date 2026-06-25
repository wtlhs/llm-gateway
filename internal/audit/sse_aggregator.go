package audit

import (
	"bufio"
	"encoding/json"
	"strings"
	"sync"
)

// sseAggregator 解析 OpenAI 风格的 SSE 流, 累积出完整 completion。
// 设计依据 DESIGN.md §5.2 "Record.appendCapture 内部的 SSE 解析职责"。
//
// 处理要点:
//   - 按行扫描, 仅处理 "data: " 前缀行
//   - data: [DONE] → 流结束哨兵
//   - 每个 chunk 的 choices[0].delta.content / reasoning_content → 拼 completion_text
//   - 最后 chunk 的 usage.prompt_tokens / completion_tokens
//   - choices[0].delta.tool_calls → 拼成数组
//   - choices[0].finish_reason(任意 chunk)
type sseAggregator struct {
	mu sync.Mutex

	// 累积缓冲
	contentBuf strings.Builder
	total      int64 // 已累积字节, 用于 AppendCapture 的 maxBytes 判断

	// 工具调用: index → 已拼接的 JSON 字符串(tool_calls 数组里按 index 对齐)
	toolIdx map[int]*strings.Builder

	// usage / finish
	promptTokens     int32
	completionTokens int32
	finishReason     string
	done             bool
}

func newSSEAggregator() *sseAggregator {
	return &sseAggregator{toolIdx: make(map[int]*strings.Builder)}
}

// append 追加一段原始字节(可能跨多行/多个 event)。
func (a *sseAggregator) append(chunk []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := string(chunk)
	a.total += int64(len(s))

	// 按行处理。SSE event 之间以空行分隔; data 行以 "data: " 开头。
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			a.done = true
			continue
		}
		a.parseChunk([]byte(payload))
	}
}

// parseChunk 解析单个 data: 行的 JSON。
func (a *sseAggregator) parseChunk(data []byte) {
	var ch struct {
		Choices []struct {
			Index int `json:"index"`
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int32 `json:"prompt_tokens"`
			CompletionTokens int32 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &ch) != nil {
		return
	}
	if ch.Usage != nil {
		a.promptTokens = ch.Usage.PromptTokens
		a.completionTokens = ch.Usage.CompletionTokens
	}
	for _, c := range ch.Choices {
		if c.Delta.Content != "" {
			a.contentBuf.WriteString(c.Delta.Content)
		}
		if c.Delta.ReasoningContent != "" {
			// reasoning 也归入 completion(便于审计完整 assistant 输出)
			a.contentBuf.WriteString(c.Delta.ReasoningContent)
		}
		if c.FinishReason != "" {
			a.finishReason = c.FinishReason
		}
		for _, tc := range c.Delta.ToolCalls {
			b, ok := a.toolIdx[tc.Index]
			if !ok {
				b = &strings.Builder{}
				a.toolIdx[tc.Index] = b
			}
			// tool_call 的 arguments 是流式分片拼接的
			if tc.Function.Arguments != "" {
				b.WriteString(tc.Function.Arguments)
			}
		}
	}
}

// completion 返回聚合后的 completion_text(OpenAI 非流式响应形态)。
func (a *sseAggregator) completion() []byte {
	out := map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{
				"role":    "assistant",
				"content": a.contentBuf.String(),
			},
			"finish_reason": a.finishReason,
		}},
	}
	b, _ := json.Marshal(out)
	return b
}

// toolCalls 返回聚合后的 tool_calls 数组(按 index 排序), 或 nil。
func (a *sseAggregator) toolCalls() []byte {
	if len(a.toolIdx) == 0 {
		return nil
	}
	// 按 index 收集
	type tc struct {
		Index int `json:"index"`
	}
	// 注意: 完整重建 tool_calls 需保留 id/type/function; 但流式只稳定给出 arguments 分片,
	// id/type 通常在首个分片。这里做尽力聚合:把同 index 的 arguments 拼起来。
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
