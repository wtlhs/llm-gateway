package knowledge

import (
	"encoding/json"
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

// TestExtract_DoubleRawWrappedPrompt 回归: safeJSON 兜底可能产生双层 raw 包装,
// 循环解包后仍能提取问题(此前只解一层导致 19k 条实质回答无法提取)。
func TestExtract_DoubleRawWrappedPrompt(t *testing.T) {
	inner := `{"model":"glm-5.2","messages":[{"role":"user","content":[{"type":"text","text":"帮我修复登录页面的校验逻辑"}]}]}`
	innerJSON, _ := json.Marshal(inner)
	mid := `{"raw":` + string(innerJSON) + `}`
	outerJSON, _ := json.Marshal(mid)
	outer := `{"raw":` + string(outerJSON) + `}`

	completion := `{"choices":[{"message":{"content":"已修复登录校验问题: 增加了手机号格式验证、验证码时效检查与账号锁定保护, 补充了对应的单元测试用例覆盖全部边界场景, 包括空输入、非法格式与超时验证码的处理路径。"}}]}`
	p := Extract(outer, completion, "glm-5.2", "", "messages")
	if p == nil {
		t.Fatal("expected pair from double-wrapped prompt")
	}
	if !strings.Contains(p.Question, "登录页面") {
		t.Errorf("question = %q, want 登录页面", p.Question)
	}
}

// TestExtract_SystemReminderStripped 回归: Anthropic 注入的 <system-reminder>
// 不是用户问题, 剥离后应提取真实问题文本。
func TestExtract_SystemReminderStripped(t *testing.T) {
	prompt := `{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is 2026-08-07.\n</system-reminder>\n\n打开项目目录并查看README"}]}]}`
	completion := `{"choices":[{"message":{"content":"已打开项目目录, README 内容为项目介绍与本地开发环境配置说明, 包含前后端启动命令、依赖安装步骤与常见问题排查指引, 具体端口分配与配置文件路径也已整理完毕。"}}]}`
	p := Extract(prompt, completion, "m", "", "messages")
	if p == nil {
		t.Fatal("expected pair")
	}
	if strings.Contains(p.Question, "system-reminder") {
		t.Errorf("question should not contain system-reminder: %q", p.Question)
	}
	if !strings.Contains(p.Question, "README") {
		t.Errorf("question should contain real user text README: %q", p.Question)
	}
}

// TestExtract_StreamingRawCompletion 回归: 流式对话的 completion 是
// {"raw":"data: {...}\ndata: {...}"} 包装的 SSE, 必须聚合 delta.content
// (此前 12.9k 条 49% 实质回答因只解析 choices 结构被过滤)。
func TestExtract_StreamingRawCompletion(t *testing.T) {
	prompt := `{"messages":[{"role":"user","content":"帮我优化数据库查询性能"}]}`
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"已优化查询性能: \"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"添加了联合索引覆盖高频查询条件\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"并重写了慢查询逻辑\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"整体响应时间下降约百分之四十\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"后续建议定期分析慢日志持续优化\"}}]}\n" +
		"data: [DONE]\n"
	rawJSON, _ := json.Marshal(sse)
	completion := `{"raw":` + string(rawJSON) + `}`
	p := Extract(prompt, completion, "m", "", "messages")
	if p == nil {
		t.Fatal("expected pair from streaming completion")
	}
	if !strings.Contains(p.Answer, "联合索引") || !strings.Contains(p.Answer, "慢查询") {
		t.Errorf("answer should aggregate SSE deltas: %q", p.Answer)
	}
}

// TestExtract_StreamingRawCompletion_WithThinking 流式含 reasoning_content
// 字段时(如 MiniMax), 只聚合 content, 不混入思考过程。
func TestExtract_StreamingRawCompletion_WithThinking(t *testing.T) {
	prompt := `{"messages":[{"role":"user","content":"解释一下什么是索引"}]}`
	sse := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"让我想想\",\"content\":\"索引是加速查询的数据结构\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"继续想\",\"content\":\"，类似书的目录\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"还在想\",\"content\":\"，能大幅减少扫描行数提升性能\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"接着想\",\"content\":\"，在数据量大的表上效果尤其明显\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"想完了\",\"content\":\"，建议为常用查询列建立合适的索引\"}}]}\n" +
		"data: [DONE]\n"
	rawJSON, _ := json.Marshal(sse)
	completion := `{"raw":` + string(rawJSON) + `}`
	p := Extract(prompt, completion, "m", "", "messages")
	if p == nil {
		t.Fatal("expected pair")
	}
	if strings.Contains(p.Answer, "让我想想") || strings.Contains(p.Answer, "继续想") {
		t.Errorf("answer should not include reasoning_content: %q", p.Answer)
	}
	if !strings.Contains(p.Answer, "索引") {
		t.Errorf("answer should include content: %q", p.Answer)
	}
}

// TestExtract_TranscriptQuestion 回归: Claude Code 把对话历史塞进 <transcript>,
// 真实问题在 "User:" 标记之后, 必须提取最后一条 User 内容(此前 8.8k 条
// transcript 对话因问题提取失败被跳过)。
func TestExtract_TranscriptQuestion(t *testing.T) {
	prompt := `{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is 2026-08-07.\n</system-reminder>\n\n<transcript>\nUser: 帮我检查登录页面的校验逻辑\nAssistant: 好的，我来看看\nUser: 还要检查注册页面的表单验证\nAssistant: 正在检查\nUser: 最后帮我检查忘记密码流程\n</transcript>"}]}]}`
	completion := `{"choices":[{"message":{"content":"已检查完登录、注册和忘记密码三个流程, 发现登录校验缺少防重复提交, 注册表单缺少密码强度验证, 忘记密码缺少验证码时效检查, 已给出修复方案和对应的单元测试建议。"}}]}`
	p := Extract(prompt, completion, "m", "", "messages")
	if p == nil {
		t.Fatal("expected pair from transcript")
	}
	if !strings.Contains(p.Question, "忘记密码") {
		t.Errorf("question should be last User: content, got %q", p.Question)
	}
	if strings.Contains(p.Question, "system-reminder") {
		t.Errorf("question should not contain system-reminder: %q", p.Question)
	}
}

// TestExtract_UserInputTag 回归: Claude Code 把用户问题包在 <user_input> 标签,
// 提取内部内容为问题, 并过滤纯闲聊(你好/hello)。
func TestExtract_UserInputTag(t *testing.T) {
	// 业务问题: 提取 <user_input> 内部内容
	prompt := `{"messages":[{"role":"user","content":[{"type":"text","text":"<user_input>\n管理端默认勾选此客户今日不再提示, 勾选并确认后状态重置\n</user_input>"}]}]}`
	completion := `{"choices":[{"message":{"content":"已分析该需求: 默认勾选与状态重置的机制需要前后端配合, 建议在配置表增加今日不再提示标记字段, 并在确认弹窗后重置该标记, 同时补充对应的接口与页面交互逻辑说明文档。"}}]}`
	p := Extract(prompt, completion, "m", "", "messages")
	if p == nil {
		t.Fatal("expected pair from user_input tag")
	}
	if strings.Contains(p.Question, "<user_input>") {
		t.Errorf("question should not contain tag: %q", p.Question)
	}
	if !strings.Contains(p.Question, "管理端默认勾选") {
		t.Errorf("question should extract tag content: %q", p.Question)
	}

	// 纯闲聊: 应被 chitChatRe 过滤
	prompt2 := `{"messages":[{"role":"user","content":[{"type":"text","text":"<user_input>\n你好\n</user_input>"}]}]}`
	if p2 := Extract(prompt2, completion, "m", "", "messages"); p2 != nil {
		t.Fatalf("expected nil for chit-chat, got %+v", p2)
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
