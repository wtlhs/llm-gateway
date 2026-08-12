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
var noiseQuestionRe = regexp.MustCompile(`(?i)^\s*(<session>|<transcript>|generate a (title|name)|为这次对话|你是什么模型|<system-reminder>|As you answer the user)`)

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
// prompt_text 有两种存储形式:
//   1. 直接 JSON: {"model":..., "messages":[...]} (Anthropic/OpenAI 统一 messages 数组)
//   2. raw 包装: {"raw": "<JSON字符串>"} (safeJSON 兜底)
// 兼容两种: 先尝试直接解析, 失败则解包 raw 再解析。
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
			return s
		}
	}
	return ""
}

// unwrapPrompt 返回可解析的请求体 JSON; 兼容 raw 包装。
func unwrapPrompt(promptRaw string) (string, bool) {
	raw := []byte(promptRaw)
	// 先尝试直接解析(顶层含 messages)
	var probe struct {
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil && len(probe.Messages) > 0 {
		return promptRaw, true
	}
	// raw 包装: {"raw": "..."}
	var wrap struct {
		Raw string `json:"raw"`
	}
	if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Raw != "" {
		return wrap.Raw, true
	}
	return promptRaw, false
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
// 网关统一归一化为 OpenAI 格式: {"choices":[{"message":{"content":"..."}}]}
// (messages 端点也是该结构, 见 0001_init 设计; 部分老数据可能直接是
//  Anthropic 格式 {"content":[{"type":"text","text":"..."}]}, 兜底解析)。
func extractAnswer(completionRaw string) string {
	raw := []byte(completionRaw)
	if len(raw) == 0 {
		return ""
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
