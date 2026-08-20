// Package training 提供大模型后训练语料构建: 从 llm_conversation 记录组装
// (对话历史 + 本次回复) 的 OpenAI messages 格式样本。
//
// 设计要点(2026-08-19):
//   - 每条 llm_conversation = 一个请求轮次: prompt_text.messages 是该轮完整历史,
//     completion_text + tool_calls 是该轮模型回复 → 每条记录即一个完整训练样本,
//     无需跨记录聚合
//   - prompt_text 可能为 {"raw":...} 包装且内容含脱敏非法值(****4321 / \a***@example.com),
//     用 audit.RepairJSONScan 动态修复(不回填表, 避免 1MB JSONB 更新膨胀)
//   - 脱敏残留(训练污染源)替换为 <REDACTED> 占位符; system-reminder 注入剥离
package training

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/company/llm-gateway/internal/audit"
)

// Message 训练样本消息(OpenAI messages 格式子集)。
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall 工具调用(无 id, 网关 tool_calls 列只有 index/function)。
type ToolCall struct {
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Sample 一条训练样本。
type Sample struct {
	Messages []Message `json:"messages"`
}

// 脱敏残留正则(仅匹配脱敏特征, 不误伤 markdown 加粗 **bold**):
//   - 3+ 星号 + 数字尾(****4321)
//   - 星号串 + @域名(a***@example.com / ***@example.com)
var (
	redactedNumRe = regexp.MustCompile(`\*{3,}[0-9]+\b`)
	redactedMailRe = regexp.MustCompile(`[A-Za-z0-9_]*\*{2,}@[A-Za-z0-9.-]+`)
)

// CleanRedacted 将脱敏残留替换为 <REDACTED> 占位符(训练污染源)。
func CleanRedacted(s string) string {
	s = redactedNumRe.ReplaceAllString(s, "<REDACTED>")
	s = redactedMailRe.ReplaceAllString(s, "<REDACTED>")
	return s
}

// HasRedaction 判断文本是否含脱敏残留。
func HasRedaction(s string) bool {
	return redactedNumRe.MatchString(s) || redactedMailRe.MatchString(s)
}

// ParsePrompt 解析 prompt_text 为消息列表(兼容 raw 多层包装 + 非法 JSON 修复)。
// 返回 (messages, 顶层 tools 定义, ok)。
func ParsePrompt(promptRaw string) ([]Message, bool) {
	cur := promptRaw
	for i := 0; i < 5; i++ {
		var doc struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(cur), &doc); err == nil && len(doc.Messages) > 0 {
			return normalizeMessages(doc.Messages), true
		}
		var wrap struct {
			Raw string `json:"raw"`
		}
		if err := json.Unmarshal([]byte(cur), &wrap); err == nil && wrap.Raw != "" {
			cur = wrap.Raw
			continue
		}
		// 修复非法 JSON(脱敏值/非法转义)后重试
		if fixed := audit.RepairJSONScan([]byte(cur)); len(fixed) > 0 {
			var doc2 struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(fixed, &doc2); err == nil && len(doc2.Messages) > 0 {
				return normalizeMessages(doc2.Messages), true
			}
		}
		return nil, false
	}
	return nil, false
}

// normalizeMessages 把原始 message 结构归一化 + 清洗:
//   - content 支持 OpenAI 字符串 / Anthropic 数组
//   - 剥离 user 消息中的 system-reminder 注入
//   - 脱敏残留替换 <REDACTED>
func normalizeMessages(raw []struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}) []Message {
	out := make([]Message, 0, len(raw))
	for _, m := range raw {
		content := contentToText(m.Content)
		if m.Role == "user" {
			content = stripSystemReminder(content)
		}
		content = CleanRedacted(content)
		if strings.TrimSpace(content) == "" {
			// 空内容消息保留 role 占位(工具轮 content 可能为空), 但跳过纯空 user
			if m.Role == "user" {
				continue
			}
		}
		out = append(out, Message{Role: m.Role, Content: content})
	}
	return out
}

// contentToText 将 message.content 转为纯文本。
// Anthropic: [{"type":"text","text":"..."}] 数组; OpenAI: 字符串。
func contentToText(c json.RawMessage) string {
	c = []byte(strings.TrimSpace(string(c)))
	if len(c) == 0 {
		return ""
	}
	if c[0] == '"' {
		var s string
		if json.Unmarshal(c, &s) == nil {
			return s
		}
		return ""
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(c, &parts); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "" || p.Type == "text" {
			if p.Text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(p.Text)
			}
		}
	}
	return sb.String()
}

// ParseCompletion 解析 completion_text 为回复内容 + 工具调用。
// 兼容: OpenAI choices/message 或 delta(SSE 聚合)、raw 包装、Anthropic content 数组。
func ParseCompletion(completionRaw, toolCallsRaw string) (content string, calls []ToolCall, ok bool) {
	body := completionRaw
	// 解 raw 包装(SSE 流等)
	for i := 0; i < 5; i++ {
		var wrap struct {
			Raw string `json:"raw"`
		}
		if err := json.Unmarshal([]byte(body), &wrap); err == nil && wrap.Raw != "" {
			body = wrap.Raw
			continue
		}
		break
	}

	// SSE 行聚合(data: {...} 的 delta.content)
	if strings.Contains(body, "\ndata:") || strings.HasPrefix(body, "data:") {
		if s := aggregateSSE(body); s != "" {
			content = s
		}
	}

	// OpenAI 结构
	var openai struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &openai); err == nil {
		for _, ch := range openai.Choices {
			if ch.Message.Content != "" {
				content = ch.Message.Content
				break
			}
			if ch.Delta.Content != "" {
				content = ch.Delta.Content
				break
			}
		}
	}

	// Anthropic 兜底
	if content == "" {
		var anth struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal([]byte(body), &anth); err == nil {
			var sb strings.Builder
			for _, c := range anth.Content {
				if c.Type == "" || c.Type == "text" {
					sb.WriteString(c.Text)
				}
			}
			content = sb.String()
		}
	}

	content = CleanRedacted(content)

	// 工具调用(tool_calls 列)
	if strings.TrimSpace(toolCallsRaw) != "" && strings.TrimSpace(toolCallsRaw) != "[]" {
		var rawCalls []struct {
			Index    int `json:"index"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		}
		if err := json.Unmarshal([]byte(toolCallsRaw), &rawCalls); err == nil {
			for _, rc := range rawCalls {
				var tc ToolCall
				tc.Type = "function"
				tc.Function.Name = rc.Function.Name
				tc.Function.Arguments = rc.Function.Arguments
				calls = append(calls, tc)
			}
		}
	}

	if strings.TrimSpace(content) == "" && len(calls) == 0 {
		return "", nil, false
	}
	return content, calls, true
}

// aggregateSSE 从 SSE 文本聚合 delta.content(OpenAI 流式)。
func aggregateSSE(sse string) string {
	var sb strings.Builder
	for _, line := range strings.Split(sse, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var evt struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			continue
		}
		for _, ch := range evt.Choices {
			sb.WriteString(ch.Delta.Content)
		}
	}
	return sb.String()
}

// stripSystemReminder 剥离 Anthropic 注入的 <system-reminder>...</system-reminder> 块。
func stripSystemReminder(s string) string {
	const open = "<system-reminder>"
	const close = "</system-reminder>"
	for {
		start := strings.Index(s, open)
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], close)
		if end < 0 {
			s = s[:start]
			break
		}
		end = start + end + len(close)
		s = s[:start] + strings.TrimLeft(s[end:], "\r\n")
	}
	return strings.TrimSpace(s)
}

// BuildSample 组装一条训练样本: [system?] + 历史 messages + 本次 assistant 回复。
// systemContent 为空则省略 system 消息。
func BuildSample(promptRaw, completionRaw, toolCallsRaw, systemContent string) (*Sample, bool) {
	messages, ok := ParsePrompt(promptRaw)
	if !ok || len(messages) == 0 {
		return nil, false
	}
	content, calls, ok2 := ParseCompletion(completionRaw, toolCallsRaw)
	if !ok2 {
		return nil, false
	}

	out := make([]Message, 0, len(messages)+2)
	if sc := strings.TrimSpace(systemContent); sc != "" {
		out = append(out, Message{Role: "system", Content: CleanRedacted(sc)})
	}
	out = append(out, messages...)
	out = append(out, Message{Role: "assistant", Content: content, ToolCalls: calls})
	// PII 二次清洗(合规, 2026-08-20): 网关 redact 漏洞 + system/tool 内容来源,
	// 输出前强制清洗所有消息文本与工具参数
	for i := range out {
		out[i].Content = CleanPII(out[i].Content)
		for j := range out[i].ToolCalls {
			out[i].ToolCalls[j].Function.Arguments = CleanPII(out[i].ToolCalls[j].Function.Arguments)
		}
	}
	return &Sample{Messages: out}, true
}
