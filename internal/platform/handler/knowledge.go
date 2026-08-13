package handler

import (
	"net/http"
	"strconv"

	"github.com/company/llm-gateway/internal/db"
)

// ListSystemPrompts GET /api/v1/knowledge/configs
// 知识资产层(配置库)列表, 按 use_count 排序。
func (h *Handler) ListSystemPrompts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))

	ctx, cancel := h.withTimeout(r)
	defer cancel()
	list, total, err := h.store.ListSystemPrompts(ctx, page, size)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	if list == nil {
		list = []db.SystemPromptSummary{}
	}
	writeList(w, list, total, page, size)
}

// GetSystemPrompt GET /api/v1/knowledge/configs/{hash}
// 单条 system prompt 完整内容。
func (h *Handler) GetSystemPrompt(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if hash == "" {
		writeErr(w, http.StatusBadRequest, "hash required")
		return
	}

	ctx, cancel := h.withTimeout(r)
	defer cancel()
	content, agentName, useCount, err := h.store.GetSystemPrompt(ctx, hash)
	if err != nil {
		writeErr(w, http.StatusNotFound, "system prompt not found")
		return
	}
	writeOK(w, map[string]any{
		"hash":       hash,
		"agent_name": agentName,
		"use_count":  useCount,
		"content":    content,
	})
}

// SearchKnowledgePairs GET /api/v1/knowledge/search?q=...
// 知识问答对检索(pg_trgm 相似度 + keywords 匹配, 加权排序)。
func (h *Handler) SearchKnowledgePairs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := q.Get("q")
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))

	ctx, cancel := h.withTimeout(r)
	defer cancel()
	list, total, err := h.store.SearchKnowledgePairs(ctx, query, page, size)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "search failed: "+err.Error())
		return
	}
	if list == nil {
		list = []db.KnowledgePair{}
	}
	writeList(w, list, total, page, size)
}

// KnowledgePairStats GET /api/v1/knowledge/pair-stats
// 知识问答对总览统计。
func (h *Handler) KnowledgePairStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	st, err := h.store.GetKnowledgePairStats(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stats failed: "+err.Error())
		return
	}
	writeOK(w, st)
}
