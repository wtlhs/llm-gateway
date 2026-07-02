// Package handler 实现平台 REST API 的 handler。
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/company/llm-gateway/internal/db"
)

// Handler 聚合所有 API handler, 共享 store 和配置。
type Handler struct {
	store        *db.Store
	timezone     string
	queryTimeout time.Duration
}

// New 构造 Handler。
func New(store *db.Store, timezone string, queryTimeout time.Duration) *Handler {
	return &Handler{store: store, timezone: timezone, queryTimeout: queryTimeout}
}

// withTimeout 给每个查询加超时(防慢查询拖垮 PG, 见 PLATFORM_DESIGN §5.4)。
func (h *Handler) withTimeout(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), h.queryTimeout)
}

// --- 响应辅助 ---

type apiResp struct {
	Code int `json:"code"`
	Data any `json:"data"`
}

type listResp struct {
	Code  int   `json:"code"`
	Total int64 `json:"total"`
	Page  int   `json:"page"`
	Size  int   `json:"size"`
	List  any   `json:"list"`
}

func writeOK(w http.ResponseWriter, v any) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(apiResp{Code: 0, Data: v})
}

func writeList(w http.ResponseWriter, list any, total int64, page, size int) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(listResp{Code: 0, Total: total, Page: page, Size: size, List: list})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"code": code, "error": msg})
}
