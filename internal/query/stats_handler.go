// Package query 提供管理端点的查询 API。
package query

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/company/llm-gateway/internal/db"
)

// StatsHandler 暴露 GET /ctx/stats。
// Phase 1 最小实现: 返回总行数。Phase 2 扩展 by_model/by_caller/dropped_rate。
func StatsHandler(store *db.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		total, err := store.Count(r.Context())
		if err != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"window":  "all-time",
			"total":   strconv.FormatInt(total, 10),
			"version": "phase1",
			"note":    "Phase 1 minimal stats. by_model/by_caller/dropped_rate in Phase 2.",
		})
	})
}
