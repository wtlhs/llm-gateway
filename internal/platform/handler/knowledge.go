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
