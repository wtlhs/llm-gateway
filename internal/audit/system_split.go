package audit

import (
	"encoding/json"
	"regexp"
	"strings"
)

// SystemExtract 是 splitSystemPrompt 的结果。
type SystemExtract struct {
	Content string // 提取出的 system prompt 原文(可能多个 system 消息拼接)
	Has     bool   // 是否含 system 内容
}

// splitSystemPrompt 从请求体里提取 system prompt, 并返回去掉 system 后的精简请求体。
// 支持两种格式:
//   - OpenAI: messages 数组里 role=system 的消息
//   - Anthropic: 顶层 system 字段(字符串或数组)
//
// 提取后:
//   - OpenAI: 从 messages 移除 system 消息, 其余保留
//   - Anthropic: 删除顶层 system 字段, 其余保留
//
// slimmed 是精简后的合法 JSON; 若解析失败或无 system, slimmed == original。
// 详见 docs/KNOWLEDGE_LAYER.md §4.1。
func splitSystemPrompt(body []byte) (slimmed []byte, sys SystemExtract) {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return body, SystemExtract{} // 非 JSON, 原样返回
	}

	// 优先处理 Anthropic 顶层 system
	if sysRaw, ok := m["system"]; ok {
		sysStr := systemFieldToString(sysRaw)
		if sysStr != "" {
			delete(m, "system")
			slimmed, _ = json.Marshal(m)
			return slimmed, SystemExtract{Content: sysStr, Has: true}
		}
	}

	// OpenAI: messages 数组里 role=system
	msgs, ok := m["messages"].([]any)
	if !ok {
		return body, SystemExtract{}
	}

	var kept []any
	var sysParts []string
	for _, msg := range msgs {
		mm, ok := msg.(map[string]any)
		if !ok {
			kept = append(kept, msg)
			continue
		}
		if role, _ := mm["role"].(string); role == "system" {
			sysParts = append(sysParts, contentFieldToString(mm["content"]))
			continue // 移除 system
		}
		kept = append(kept, msg)
	}

	if len(sysParts) == 0 {
		return body, SystemExtract{} // 无 system
	}

	m["messages"] = kept
	slimmed, _ = json.Marshal(m)
	return slimmed, SystemExtract{Content: strings.Join(sysParts, "\n\n"), Has: true}
}

// systemFieldToString 把 Anthropic 的 system 字段(字符串或 [{type,text}] 数组)转成纯文本。
func systemFieldToString(sys any) string {
	switch v := sys.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, item := range v {
			if mm, ok := item.(map[string]any); ok {
				if t, ok := mm["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}
	return ""
}

// contentFieldToString 把 message.content(字符串或 [{type,text}] 数组)转纯文本。
func contentFieldToString(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, item := range v {
			if mm, ok := item.(map[string]any); ok {
				if t, ok := mm["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}
	return ""
}

// 启发式提取 agent 名(docs/KNOWLEDGE_LAYER.md §3.3)。
// 优先级: <Name>X</Name> > 已知 agent 名单(You are X) > "你是X"/"You are a X" > caller_tag > "unknown"
var (
	reNameTag  = regexp.MustCompile(`(?s)<Name>\s*(.+?)\s*</Name>`)
	reYouAreEN = regexp.MustCompile(`(?i)You are\s+(?:a|an|the)\s+([A-Za-z0-9_\- ]+?)[\.,\n]`)
	reYouAreCN = regexp.MustCompile(`你是(?:一个|一名|一位)?\s*([^\s,，。.!！\n]{2,20})`)
	reRoleTag  = regexp.MustCompile(`(?s)<Role>.*?名称[:：]\s*(.+?)[\n<]`)

	// 已知 agent 名单(大小写不敏感, 词边界): 覆盖主流 coding agent。
	// 注意: 交替顺序按"更长短语优先", 避免 ZCode Explore 被 ZCode 抢先匹配。
	// 匹配 "You are <name>"(无冠词直接跟名字) —— 旧正则要求 a/an/the 冠词,
	// 导致 opencode / ZCode / Claude 等无冠词写法全部落空, 归为 unknown。
	reAgentKnown = regexp.MustCompile(`(?i)\bYou are\s+(ZCode Explore|ZCode|opencode|Trae IDE|TraeCode|Gemini CLI|Qwen Code|Windsurf|Roo Code|Amazon Q|Cursor|Copilot|Cline|Aider|Codex|Claude|Cody|Continue|Kilo|Mentat|OpenHands|SWE-agent)\b`)
)

// extractAgentName 从 system prompt 内容启发式提取 agent 名。尽力而为, 失败返回 "unknown"。
func extractAgentName(content, callerTag string) string {
	// 1. <Name>X</Name>
	if m := reNameTag.FindStringSubmatch(content); len(m) > 1 {
		return cleanAgentName(m[1])
	}
	// 2. 已知 agent 名单(优先于通用冠词规则: 无冠词写法 "You are opencode," 等)
	if m := reAgentKnown.FindStringSubmatch(content); len(m) > 1 {
		return cleanAgentName(m[1])
	}
	// 3. "You are a X"
	if m := reYouAreEN.FindStringSubmatch(content); len(m) > 1 {
		return cleanAgentName(m[1])
	}
	// 4. "你是X"(中文)
	if m := reYouAreCN.FindStringSubmatch(content); len(m) > 1 {
		return cleanAgentName(m[1])
	}
	// 5. caller_tag 兜底(token 名往往含 agent 标识)
	if callerTag != "" && callerTag != "unknown" {
		return callerTag
	}
	return "unknown"
}

func cleanAgentName(s string) string {
	s = strings.TrimSpace(s)
	// 去尾部标点和冗余
	s = strings.TrimRight(s, ".,，。;；")
	if len(s) > 128 {
		s = s[:128]
	}
	if s == "" {
		return "unknown"
	}
	return s
}
