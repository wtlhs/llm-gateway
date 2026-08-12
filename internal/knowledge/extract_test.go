package knowledge

import (
	"strings"
	"testing"
)

// ============ Extract 主流程 ============

func TestExtract_AnthropicFormat(t *testing.T) {
	prompt := `{"model":"glm-5.2","messages":[{"role":"user","content":[{"type":"text","text":"请你检查物流保险配置页面的停用功能是否存在缺陷"}]}]}`
	completion := `{"choices":[{"message":{"role":"assistant","content":"## 检查结果\n发现停用功能存在一个缺陷: 状态为启用时点击停用按钮没有生效, 需要先校验权限再更新数据库状态字段, 修复后需要补充回归测试用例覆盖停用与启用两个场景。"}}]}`
	p := Extract(prompt, completion, "glm-5.2", "zhaoshunyao", "messages")
	if p == nil {
		t.Fatal("expected pair")
	}
	if !strings.Contains(p.Question, "物流保险配置页面") {
		t.Errorf("question = %q", p.Question)
	}
	if !strings.Contains(p.Answer, "停用功能") {
		t.Errorf("answer = %q", p.Answer)
	}
	if len(p.Keywords) == 0 {
		t.Error("keywords empty")
	}
}

func TestExtract_AnthropicFormat_WithCodeAndFile(t *testing.T) {
	prompt := `{"model":"glm-5.2","messages":[{"role":"user","content":[{"type":"text","text":"请检查配置页面停用功能"}]}]}`
	// 用 \x60 代替反引号, 避免 Go 字符串嵌套问题; \\n 为 JSON 内转义换行
	code := "\x60\x60\x60java\\nif (status == 1) { disable(); }\\n\x60\x60\x60"
	completion := "{\"choices\":[{\"message\":{\"role\":\"assistant\",\"content\":\"## 检查结果\\n发现缺陷:\\n" + code + "\\n文件: src/main/java/InsuranceConfig.java 修复该问题需要调整状态判断逻辑并补充对应测试用例\"}}]}"
	p := Extract(prompt, completion, "glm-5.2", "", "messages")
	if p == nil {
		t.Fatal("expected pair")
	}
	if p.CodeBlocks != 1 {
		t.Errorf("code_blocks = %d, want 1", p.CodeBlocks)
	}
	if len(p.FilePaths) == 0 || p.FilePaths[0] != "src/main/java/InsuranceConfig.java" {
		t.Errorf("file_paths = %v", p.FilePaths)
	}
}

func TestExtract_RawWrappedPrompt(t *testing.T) {
	// safeJSON 兜底: {"raw":"<转义JSON>"}
	prompt := `{"raw":"{\"model\":\"glm-5.2\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"帮我对比两个Excel文件的关联键\"}]}]}"}`
	completion := `{"choices":[{"message":{"content":"对比完成: 跟踪号与物流单号是最佳匹配键, 建议按物流单号建立索引并核对供应商金额, 具体匹配逻辑需要结合业务文档进一步确认。"}}]}`
	p := Extract(prompt, completion, "glm-5.2", "", "messages")
	if p == nil {
		t.Fatal("expected pair")
	}
	if !strings.Contains(p.Question, "对比两个Excel文件") {
		t.Errorf("question = %q", p.Question)
	}
}

func TestExtract_OpenAIFormat(t *testing.T) {
	prompt := `{"messages":[{"role":"system","content":"你是助手"},{"role":"user","content":"写一个排序函数"},{"role":"assistant","content":"好的"},{"role":"user","content":"要支持中文排序"}]}`
	completion := `{"choices":[{"message":{"role":"assistant","content":"按拼音排序即可: 使用 locale.strxfrm 对中文做拼音转换后排序, 该方案兼容多音字场景, 需要注意不同操作系统的 locale 差异, 建议在测试环境验证后再上线。"}}]}`
	p := Extract(prompt, completion, "deepseek-v4-flash", "", "chat/completions")
	if p == nil {
		t.Fatal("expected pair")
	}
	// 问题 = 最后一条 user 消息
	if !strings.Contains(p.Question, "中文排序") {
		t.Errorf("question should be last user msg, got %q", p.Question)
	}
	if strings.Contains(p.Question, "写一个排序函数") {
		t.Errorf("question should not be first user msg, got %q", p.Question)
	}
}

// ============ 过滤规则 ============

func TestExtract_FilterTooShortAnswer(t *testing.T) {
	prompt := `{"messages":[{"role":"user","content":"你好"}]}`
	completion := `{"choices":[{"message":{"content":"好的"}}]}`
	if p := Extract(prompt, completion, "m", "", "messages"); p != nil {
		t.Fatalf("expected nil for short answer, got %+v", p)
	}
}

func TestExtract_FilterShortQuestion(t *testing.T) {
	prompt := `{"messages":[{"role":"user","content":"好"}]}`
	completion := `{"choices":[{"message":{"content":"这是一个足够长的回答内容用于通过长度过滤测试，大概五十个字符以上吧"}}]}`
	if p := Extract(prompt, completion, "m", "", "messages"); p != nil {
		t.Fatalf("expected nil for short question, got %+v", p)
	}
}

func TestExtract_FilterEmptyCompletion(t *testing.T) {
	prompt := `{"messages":[{"role":"user","content":"测试问题内容"}]}`
	completion := `{"choices":[{"message":{"content":""}}]}`
	if p := Extract(prompt, completion, "m", "", "messages"); p != nil {
		t.Fatalf("expected nil for empty completion, got %+v", p)
	}
}

func TestExtract_FilterErrorLike(t *testing.T) {
	prompt := `{"messages":[{"role":"user","content":"测试"}]}`
	// 错误占位(长度够但无知识)
	completion := `{"choices":[{"message":{"content":"{}"}}]}`
	if p := Extract(prompt, completion, "m", "", "messages"); p != nil {
		t.Fatalf("expected nil for {} completion, got %+v", p)
	}
}

func TestExtract_FilterNoiseQuestion(t *testing.T) {
	// 会话标题生成等系统级问题无知识价值
	prompt := `{"messages":[{"role":"user","content":"<session> 为本次对话生成一个标题"}]}`
	completion := `{"choices":[{"message":{"content":"这是一个足够长的回答内容用于通过长度过滤测试，大概五十个字符以上吧，这里继续补充更多内容确保长度足够。"}}]}`
	if p := Extract(prompt, completion, "m", "", "messages"); p != nil {
		t.Fatalf("expected nil for session-title question, got %+v", p)
	}
}

// ============ 解析细节 ============

func TestExtract_AnthropicCompletionFallback(t *testing.T) {
	prompt := `{"messages":[{"role":"user","content":"解释一下架构"}]}`
	// Anthropic 原生格式兜底
	completion := `{"content":[{"type":"text","text":"分层架构: SCM负责事实源, BI负责经营表达与风险发现, 两者通过接口解耦, 新增看板优先由BI承接, 涉及正式业务写入必须回到SCM事实源。"}]}`
	p := Extract(prompt, completion, "m", "", "messages")
	if p == nil {
		t.Fatal("expected pair")
	}
	if !strings.Contains(p.Answer, "SCM负责事实源") {
		t.Errorf("answer = %q", p.Answer)
	}
}

func TestExtract_NoMessages(t *testing.T) {
	prompt := `{"model":"glm-5.2"}`
	completion := `{"choices":[{"message":{"content":"无问题可提取的请求"}}]}`
	if p := Extract(prompt, completion, "m", "", "messages"); p != nil {
		t.Fatalf("expected nil when no messages, got %+v", p)
	}
}

func TestExtract_MultiSegmentContent(t *testing.T) {
	prompt := `{"messages":[{"role":"user","content":[{"type":"text","text":"第一段"},{"type":"image","source":{"type":"base64"}},{"type":"text","text":"第二段问题"}]}]}`
	completion := `{"choices":[{"message":{"content":"这是一个足够长的回答，用于通过最小长度过滤，内容完整无缺，涵盖了多段内容的拼接逻辑验证，确保提取器正确处理文本段与图片段混合的场景。"}}]}`
	p := Extract(prompt, completion, "m", "", "messages")
	if p == nil {
		t.Fatal("expected pair")
	}
	// 只拼接文本段, 跳过 image
	if !strings.Contains(p.Question, "第一段") || !strings.Contains(p.Question, "第二段问题") {
		t.Errorf("question = %q, want text segments joined", p.Question)
	}
	if strings.Contains(p.Question, "base64") {
		t.Errorf("question should skip image segment: %q", p.Question)
	}
}

// ============ 辅助函数 ============

func TestExtract_Keywords(t *testing.T) {
	kws := extractKeywords("请帮我检查物流保险配置页面的停用启用功能")
	if len(kws) == 0 {
		t.Fatal("expected keywords")
	}
	joined := strings.Join(kws, "|")
	// 2-gram 滑动切词, 核心业务词必须出现(停用/启用/物流/保险/配置)
	for _, want := range []string{"物流", "保险", "配置", "停用", "启用"} {
		if !strings.Contains(joined, want) {
			t.Errorf("keywords missing %q: %v", want, kws)
		}
	}
	// 英文关键词(如果有)保留
	kws2 := extractKeywords("请修复 LoginPage 的登录逻辑 bug")
	joined2 := strings.Join(kws2, "|")
	if !strings.Contains(strings.ToLower(joined2), "loginpage") {
		t.Errorf("english keyword missing: %v", kws2)
	}
}

func TestExtract_FilePaths(t *testing.T) {
	s := "修改了 src/components/Table.vue 和 backend/app/controllers/OrderController.java"
	paths := extractFilePaths(s)
	if len(paths) != 2 {
		t.Fatalf("paths = %v", paths)
	}
}

func TestExtract_CodeBlocks(t *testing.T) {
	open := "\x60\x60\x60"
	s := "方案一:\n" + open + "go\nfmt.Println(\"hi\")\n" + open + "\n方案二:\n" + open + "sql\nSELECT 1;\n" + open
	if n := countCodeBlocks(s); n != 2 {
		t.Errorf("code blocks = %d, want 2", n)
	}
}
