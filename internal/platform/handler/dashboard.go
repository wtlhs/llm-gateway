package handler

import (
	"net/http"
	"strconv"
)

// DashboardOverview GET /api/v1/dashboard/overview
// 总览看板: 总量/今日/token汇总/平均延迟/错误数。
func (h *Handler) DashboardOverview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	ov, err := h.store.DashboardOverview(ctx, h.timezone)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeOK(w, ov)
}

// DashboardTrend GET /api/v1/dashboard/trend?days=7
// 按天趋势: 对话量 + token。
func (h *Handler) DashboardTrend(w http.ResponseWriter, r *http.Request) {
	days := 7
	if d := r.URL.Query().Get("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil {
			days = n
		}
	}
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	points, err := h.store.DashboardTrend(ctx, h.timezone, days)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeOK(w, points)
}

// TopModels GET /api/v1/dashboard/top-models?limit=5
func (h *Handler) TopModels(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r, 5)
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	items, err := h.store.TopByDimension(ctx, "model", limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeOK(w, items)
}

// TopCallers GET /api/v1/dashboard/top-callers?limit=5
func (h *Handler) TopCallers(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r, 5)
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	items, err := h.store.TopByDimension(ctx, "caller", limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeOK(w, items)
}

func parseLimit(r *http.Request, def int) int {
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 50 {
			return n
		}
	}
	return def
}
