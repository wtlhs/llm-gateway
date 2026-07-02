package handler

import (
	"encoding/json"
	"net/http"

	"github.com/company/llm-gateway/internal/db"
)

// DBStats GET /api/v1/ops/db-stats
// 数据库表统计(运维监控)。
func (h *Handler) DBStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	stats, err := h.store.GetDBStats(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeOK(w, stats)
}

// DataQuality GET /api/v1/ops/data-quality
// 数据完整性统计(采集质量检测)。
func (h *Handler) DataQuality(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	q, err := h.store.GetDataQuality(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeOK(w, q)
}

// LatencyDist GET /api/v1/ops/latency
// 延迟分布(按区间)。
func (h *Handler) LatencyDist(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	buckets, err := h.store.LatencyDistribution(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeOK(w, buckets)
}

// HourlyTrend GET /api/v1/dashboard/hourly
// 24 小时分布(发现高峰时段)。
func (h *Handler) HourlyTrend(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	points, err := h.store.HourlyTrend(ctx, h.timezone)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeOK(w, points)
}

// EndpointDist GET /api/v1/dashboard/endpoints
// 端点分布。
func (h *Handler) EndpointDist(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	stats, err := h.store.EndpointDistribution(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeOK(w, stats)
}

// ModelStats GET /api/v1/dashboard/model-stats
// 按 model 统计 token 效率 + 延迟。
func (h *Handler) ModelStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	stats, err := h.store.ModelTokenStats(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeOK(w, stats)
}

// KnowledgeStats GET /api/v1/knowledge/stats
// 知识资产层汇总。
func (h *Handler) KnowledgeStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	stats, err := h.store.GetKnowledgeStats(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeOK(w, stats)
}

// ExportConversations GET /api/v1/conversations/export
// 导出对话(JSON 数组, 最多 1000 条)。
func (h *Handler) ExportConversations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	q := r.URL.Query()
	f := db.ConversationFilter{
		Model:  q.Get("model"),
		Caller: q.Get("caller"),
	}
	list, err := h.store.ExportConversations(ctx, f, 1000)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "export failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="conversations.json"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"code":0,"count":`))
	w.Write([]byte(itoa(len(list))))
	w.Write([]byte(`,"list":`))
	encodeJSON(w, list)
	w.Write([]byte(`}`))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func encodeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		w.Write([]byte("[]"))
		return
	}
	w.Write(b)
}
