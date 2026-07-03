package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/company/llm-gateway/internal/db"
)

// ListConversations GET /api/v1/conversations
// 分页 + 筛选对话列表。
func (h *Handler) ListConversations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))

	f := db.ConversationFilter{
		Page:     page,
		Size:     size,
		Model:    q.Get("model"),
		Caller:   q.Get("caller"),
		Endpoint: q.Get("endpoint"),
	}
	if s := q.Get("stream"); s != "" {
		b := s == "true" || s == "1"
		f.IsStream = &b
	}
	if s := q.Get("status"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			f.Status = n / 100 // 200→2, 404→4
		}
	}
	// 时间范围(ISO8601)
	if s := q.Get("from"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			f.TimeFrom = t
		}
	}
	if s := q.Get("to"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			f.TimeTo = t
		}
	}

	ctx, cancel := h.withTimeout(r)
	defer cancel()
	list, total, err := h.store.ListConversations(ctx, f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	if list == nil {
		list = []db.ConversationSummary{} // 空数组而非 null
	}
	writeList(w, list, total, f.Page, f.Size)
}

// GetConversation GET /api/v1/conversations/{id}
// 单条对话完整内容。
func (h *Handler) GetConversation(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}

	ctx, cancel := h.withTimeout(r)
	defer cancel()
	conv, err := h.store.GetConversation(ctx, id)
	if err != nil {
		// 区分"不存在"和"查询错误", 便于排查
		if strings.Contains(err.Error(), "no rows") {
			writeErr(w, http.StatusNotFound, "conversation not found")
		} else {
			writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		}
		return
	}

	// prompt_text / completion_text 是 RawMessage, 转成可读 JSON 结构返回
	resp := struct {
		ID               int64           `json:"id"`
		Model            string          `json:"model"`
		Endpoint         string          `json:"endpoint"`
		CallerTag        string          `json:"caller_tag"`
		IsStream         bool            `json:"is_stream"`
		HTTPStatus       int             `json:"http_status"`
		PromptText       json.RawMessage `json:"prompt_text"`
		CompletionText   json.RawMessage `json:"completion_text"`
		ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
		PromptTokens     int32           `json:"prompt_tokens"`
		CompletionTokens int32           `json:"completion_tokens"`
		ErrorMessage     string          `json:"error_message,omitempty"`
		UpstreamLatency  int32           `json:"upstream_latency_ms"`
		SystemPromptHash string          `json:"system_prompt_hash,omitempty"`
		ClientIP         string          `json:"client_ip,omitempty"`
		CreatedAt        time.Time       `json:"created_at"`
	}{
		ID: conv.ID, Model: conv.Model, Endpoint: conv.Endpoint,
		CallerTag: conv.CallerTag, IsStream: conv.IsStream, HTTPStatus: conv.HTTPStatus,
		PromptText: conv.PromptText, CompletionText: conv.CompletionText,
		ToolCalls: conv.ToolCalls, PromptTokens: conv.PromptTokens,
		CompletionTokens: conv.CompletionTokens, ErrorMessage: conv.ErrorMessage,
		UpstreamLatency: conv.UpstreamLatencyMs, SystemPromptHash: conv.SystemPromptHash,
		ClientIP: conv.ClientIP, CreatedAt: conv.CreatedAt,
	}
	writeOK(w, resp)
}
