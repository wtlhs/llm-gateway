// Package hooks 提供 Phase 2/3 扩展点。Phase 1 全部 no-op。
// 设计依据 DESIGN.md §7 / §5.3 C4: 接入点在 audit pipeline 的 persist 末尾。
package hooks

import "github.com/company/llm-gateway/internal/db"

// OnConversationComplete 在一条记录落库后被调用。
// Phase 2: 文本抽取 → pgvector 向量化 → 高频问题聚类 → 实体识别。
// Phase 1: 空实现, 保持热路径纯净。
//
// 注意: 此函数在 worker goroutine 内同步调用, Phase 2 实现应异步化(投递到独立队列)
// 以免阻塞落库。
func OnConversationComplete(_ *db.Conversation) error {
	return nil
}

// OnRequestRewrite 在请求转发前被调用(预留 Phase 3)。
// Phase 3: 按意图召回上下文 → 注入 system prompt(网关层, 用户无感)。
// Phase 1: 空实现。
// 返回 error 时网关跳过注入(旁路), 不阻塞请求(DESIGN.md §8.4 旁路开关)。
func OnRequestRewrite(_ any) error {
	return nil
}
