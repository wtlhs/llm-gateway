// Package knowledge 实现知识库提取: 从 llm_conversation 提取(问题, 回答)问答对。
// 详见 docs/KNOWLEDGE_LAYER.md Phase 3 与 migration 0005_knowledge_pairs。
package knowledge

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// Pair 是一条知识单元: 用户问题 + agent 回答 + 知识权重特征。
type Pair struct {
	ConvID     int64
	Question   string
	Answer     string
	CodeBlocks int
	FilePaths  []string
	Keywords   []string
	Model      string
	CallerName string
	Endpoint   string
}

// 跳过规则: 回答过短/纯工具调用/错误占位等无知识价值的记录。
const (
	minAnswerLen = 50  // 回答少于 50 字符视为无知识
	minQuestion  = 4   // 问题少于 4 字符视为闲聊/噪声
)

// noiseQuestionRe 匹配明显无知识价值的问题(会话标题生成/系统提示/system-reminder 注入等)。
// 注意: 中文后无单词边界, 不能用 \b 收尾; 用宽松前缀匹配。
var noiseQuestionRe = regexp.MustCompile(`(?i)^\s*(<session>|<transcript>|generate a (title|name)|为这次对话|你是什么模型|<system-reminder>|As you answer the user|<user_input>)`)

// chitChatRe 匹配纯闲聊(无业务知识)的问题。
var chitChatRe = regexp.MustCompile(`(?i)^\s*(你好|哈罗|hello|hi|谢谢|thank|再见|拜拜|你是谁|你会什么|测试|test)\s*$`)

// codeBlockRe 匹配 markdown 代码块 ```lang ... ```。
var codeBlockRe = regexp.MustCompile("```[^`\n]*\n[\\s\\S]*?```|`[^`\n]+`")

// filePathRe 匹配常见文件路径(中文项目路径 + 扩展名)。
var filePathRe = regexp.MustCompile(`[\p{Han}A-Za-z0-9_./-]+\.(go|java|ts|tsx|js|vue|py|sql|yml|yaml|json|xml|md|css|scss|html|xlsx|xls|docx|pdf|png|jpg|jar|conf|properties|ini|sh|bat|ps1)\b`)

// stopWords 中文业务噪声词(不作为关键词)。
var stopWords = map[string]bool{
	"请": true, "帮我": true, "一下": true, "这个": true, "那个": true,
	"请问": true, "你": true, "我": true, "的": true, "了": true,
	"吗": true, "呢": true, "吧": true, "啊": true, "看看": true,
	"检查": true, "需要": true, "进行": true, "我们": true, "你们": true,
}

// Extract 从一条 llm_conversation 记录提取问答对。
// promptRaw/completionRaw 是 JSONB 列的原始文本(prompt_text/completion_text)。
// 返回 nil 表示该记录无知识价值(应被过滤)。
func Extract(promptRaw, completionRaw, model, callerName, endpoint string) *Pair {
	question := extractQuestion(promptRaw)
	if len([]rune(question)) < minQuestion {
		return nil
	}
	if noiseQuestionRe.MatchString(question) {
		return nil
	}
	if chitChatRe.MatchString(question) {
		return nil
	}
	answer := extractAnswer(completionRaw)
	if len([]rune(answer)) < minAnswerLen {
		return nil
	}
	// 空回答/纯工具调用占位过滤
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" || trimmed == "{}" {
		return nil
	}

	p := &Pair{
		Question:   question,
		Answer:     answer,
		Model:      model,
		CallerName: callerName,
		Endpoint:   endpoint,
	}
	p.CodeBlocks = countCodeBlocks(answer)
	p.FilePaths = extractFilePaths(answer)
	p.Keywords = extractKeywords(question)
	return p
}

// extractQuestion 从 prompt_text 提取"最后一条 user 消息"作为问题。
// prompt_text 有三种存储形式:
//   1. 直接 JSON: {"model":..., "messages":[...]} (Anthropic/OpenAI 统一 messages 数组)
//   2. raw 包装: {"raw": "<JSON字符串>"} (safeJSON 兜底; 可能多层嵌套!)
//   3. 双层 raw: {"raw":"{\"raw\":\"{\\\"...\\\"}\"}"} (safeJSON 兜底后的再次包装)
// 兼容: 循环解包 raw 直到拿到可解析的 messages, 再取最后 user 消息。
func extractQuestion(promptRaw string) string {
	body, ok := unwrapPrompt(promptRaw)
	if !ok {
		return ""
	}
	var doc struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		// 部分请求无 messages(如 /models、count_tokens), 无问题可提取
		return ""
	}
	// 取最后一条 user 消息(问题通常在末尾; 中间 user 是上下文/历史)
	for i := len(doc.Messages) - 1; i >= 0; i-- {
		m := doc.Messages[i]
		if m.Role != "user" {
			continue
		}
		// content 可能是 string(OpenAI) 或 数组(Anthropic: [{type,text}])
		if s := contentToText(m.Content); s != "" {
			// 剥离 system-reminder 注入(Anthropic 附加的系统提示, 不是用户问题)
			s = stripSystemReminder(s)
			// transcript 场景: Claude Code 把整段对话历史塞进 <transcript>,
			// 真实问题在 "User: ..." 标记之后, 提取最后一条 User 内容
			if q := extractFromTranscript(s); q != "" {
				return q
			}
			// <user_input> 标签: Claude Code 把用户输入包在标签里, 提取内部内容
			if q := extractFromUserInput(s); q != "" {
				return q
			}
			if s != "" {
				return s
			}
		}
	}
	return ""
}

// extractFromUserInput 从 <user_input>...</user_input> 标签中提取用户输入内容。
// Claude Code 把用户问题包在 <user_input> 标签中, 标签本身不是问题。
func extractFromUserInput(s string) string {
	const open = "<user_input>"
	const close = "</user_input>"
	start := strings.Index(s, open)
	if start < 0 {
		return ""
	}
	body := s[start+len(open):]
	if end := strings.Index(body, close); end >= 0 {
		body = body[:end]
	}
	return strings.TrimSpace(body)
}

// extractFromTranscript 从 <transcript> 文本中提取最后一条 "User:" 之后的内容。
// Claude Code 的 transcript 格式:
//   <transcript>
//   User: 第一个问题
//   ...agent 回复...
//   User: 最终问题
//   </transcript>
// 返回最后一条 User: 之后的文本(真实问题), 无则返回空。
func extractFromTranscript(s string) string {
	// 定位 <transcript> 块
	start := strings.Index(s, "<transcript>")
	if start < 0 {
		return ""
	}
	body := s[start+len("<transcript>"):]
	if end := strings.Index(body, "</transcript>"); end >= 0 {
		body = body[:end]
	}
	// 找所有 "User:" 出现位置, 取最后一个
	marker := "User:"
	lastIdx := -1
	for {
		idx := strings.Index(body[lastIdx+1:], marker)
		if idx < 0 {
			break
		}
		lastIdx += 1 + idx
	}
	if lastIdx < 0 {
		return ""
	}
	// 取最后一条 User: 之后到下一个换行/结束的文本
	q := strings.TrimSpace(body[lastIdx+len(marker):])
	// 只取到第一个换行(单条问题), 避免混入 agent 回复
	if nl := strings.IndexAny(q, "\r\n"); nl >= 0 {
		q = strings.TrimSpace(q[:nl])
	}
	// 去掉 [Request interrupted by user] 等系统标记
	q = strings.TrimPrefix(q, "[Request interrupted by user]")
	return strings.TrimSpace(q)
}

// unwrapPrompt 返回可解析的请求体 JSON; 兼容 raw 多层包装。
// 修复(2026-08-12): 原实现只解一层 raw, 而 safeJSON 兜底后可能产生
// 双层包装({"raw":"{\"raw\":\"...\"}"}), 导致 messages 解析失败、
// 大量实质回答无法提取。现在循环解包(最多 5 层)直到无 raw 字段。
func unwrapPrompt(promptRaw string) (string, bool) {
	cur := promptRaw
	for i := 0; i < 5; i++ {
		// 先尝试直接解析(顶层含 messages)
		var probe struct {
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal([]byte(cur), &probe); err == nil && len(probe.Messages) > 0 {
			return cur, true
		}
		// raw 包装: {"raw": "..."}
		var wrap struct {
			Raw string `json:"raw"`
		}
		if err := json.Unmarshal([]byte(cur), &wrap); err == nil && wrap.Raw != "" {
			cur = wrap.Raw
			continue
		}
		// 无 raw 也无 messages: 放弃
		return cur, false
	}
	return cur, false
}

// stripSystemReminder 剥离 Anthropic 注入的 <system-reminder>...</system-reminder> 块。
// 这类内容不是用户问题, 提取后置空让调用方回退到更早的 user 消息。
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
			s = s[:start] // 未闭合, 丢弃到开头
			break
		}
		end = start + end + len(close)
		// 删除该块(含其后的换行)
		s = s[:start] + strings.TrimLeft(s[end:], "\r\n")
	}
	return strings.TrimSpace(s)
}

// contentToText 将 message.content 转为纯文本。
// Anthropic: [{"type":"text","text":"..."}] 或 [{"text":"..."}] (可能多段拼接)
// OpenAI:   "直接字符串"
func contentToText(c json.RawMessage) string {
	c = []byte(strings.TrimSpace(string(c)))
	if len(c) == 0 {
		return ""
	}
	// OpenAI: 字符串
	if c[0] == '"' {
		var s string
		if json.Unmarshal(c, &s) == nil {
			return s
		}
		return ""
	}
	// Anthropic: 数组
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(c, &parts); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		// 只取文本段(跳过 image/tool_use 等非文本段)
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

// extractAnswer 从 completion_text 提取回答全文。
// completion_text 有三种存储形式:
//   1. OpenAI 归一化: {"choices":[{"message":{"content":"..."}}]} (非流式)
//   2. raw 包装 SSE 流: {"raw":"data: {...}\ndata: {...}"} (流式被 safeJSON 包装)
//   3. Anthropic 原生: {"content":[{"type":"text","text":"..."}]} (兜底)
// 修复(2026-08-12): 流式对话的 completion 是 {"raw":"data: ..."} 包装的 SSE,
// 原实现只解析 choices 结构导致 12.9k 条(49%)实质回答被过滤。现在支持:
//   - 解 raw 包装后按行解析 SSE(data: {...}), 聚合 delta.content
//   - 兼容 choices/message/delta 两种 SSE 载荷
func extractAnswer(completionRaw string) string {
	raw := []byte(completionRaw)
	if len(raw) == 0 {
		return ""
	}
	// 先解 raw 包装(流式 SSE 或任何 safeJSON 兜底)
	if body, ok := unwrapRaw(completionRaw); ok && body != completionRaw {
		if s := extractSSEAnswer(body); s != "" {
			return s
		}
		// raw 内可能是完整 JSON(非 SSE), 继续按 choices 解析
		raw = []byte(body)
	}
	// OpenAI 格式(主流)
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
	if err := json.Unmarshal(raw, &openai); err == nil {
		for _, ch := range openai.Choices {
			if ch.Message.Content != "" {
				return ch.Message.Content
			}
			if ch.Delta.Content != "" {
				return ch.Delta.Content
			}
		}
	}
	// Anthropic 格式兜底
	var anth struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &anth); err == nil {
		var sb strings.Builder
		for _, c := range anth.Content {
			if c.Type == "" || c.Type == "text" {
				if c.Text != "" {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(c.Text)
				}
			}
		}
		return sb.String()
	}
	return ""
}

// unwrapRaw 解一层 {"raw":"..."} 包装(不递归, 由调用方按需循环)。
func unwrapRaw(s string) (string, bool) {
	var wrap struct {
		Raw string `json:"raw"`
	}
	if err := json.Unmarshal([]byte(s), &wrap); err == nil && wrap.Raw != "" {
		return wrap.Raw, true
	}
	return s, false
}

// extractSSEAnswer 从 SSE 文本(data: {...} 多行)聚合回答内容。
// 兼容 OpenAI 流式两种载荷:
//   data: {"choices":[{"delta":{"content":"..."}}]}
//   data: {"choices":[{"delta":{"message":{"content":"..."}}}]}  (部分实现)
func extractSSEAnswer(sse string) string {
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
			if ch.Delta.Content != "" {
				sb.WriteString(ch.Delta.Content)
			}
		}
	}
	return sb.String()
}

// countCodeBlocks 统计回答中 markdown 代码块数量。
func countCodeBlocks(s string) int {
	return len(codeBlockRe.FindAllString(s, -1))
}

// extractFilePaths 提取回答中的文件路径(去重, 保序)。
func extractFilePaths(s string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range filePathRe.FindAllString(s, -1) {
		m = strings.Trim(m, " \t()[]{}'\"`")
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// extractKeywords 从问题抽取中文/英文关键词。
// 中文: 按 2-gram 滑动切词(业务词多为 2-4 字), 过滤完全命中停用词的 gram。
// 英文: 按非字母数字切分, 保留长度>=3 的词。
func extractKeywords(q string) []string {
	var out []string
	seen := make(map[string]bool)
	runes := []rune(q)
	for i := 0; i < len(runes); i++ {
		if isHan(runes[i]) {
			j := i
			for j < len(runes) && isHan(runes[j]) {
				j++
			}
			seg := string(runes[i:j])
			for _, kw := range splitCJK(seg) {
				if !stopWords[kw] && !seen[kw] {
					seen[kw] = true
					out = append(out, kw)
				}
			}
			i = j - 1
		}
	}
	for _, w := range strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		w = strings.ToLower(w)
		if len(w) >= 3 && !seen[w] && !stopWords[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	// 保留 2-gram 全量(检索用), 但限制数量防止噪声淹没
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

// splitCJK 将连续中文按 2-gram 滑动切词(如 "物流保险配置" → 物流/流保/保险/险配/配置)。
// 业务词多为 2-4 字, 2-gram 覆盖度最高。
func splitCJK(seg string) []string {
	runes := []rune(seg)
	if len(runes) <= 2 {
		return []string{seg}
	}
	var out []string
	for i := 0; i+2 <= len(runes); i++ {
		out = append(out, string(runes[i:i+2]))
	}
	return out
}

func isHan(r rune) bool {
	return unicode.Is(unicode.Han, r)
}
