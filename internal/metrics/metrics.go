// Package metrics 集中定义 Prometheus 指标。
// 指标清单与 DESIGN.md §5.7 一致(M2/C5/C6 修订后版本)。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// registry 独立注册表, 避免与可能存在的其他 prometheus 采集器冲突。
	Registry = prometheus.NewRegistry()

	reg = promauto.With(Registry)
)

// 常量桶: HTTP 请求延迟典型 10ms~30s。
var latencyBuckets = prometheus.ExponentialBuckets(0.005, 2, 14) // 5ms ~ 40s

// --- 流量与延迟 ---
var (
	RequestTotal = reg.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_request_total",
		Help: "Total proxied requests.",
	}, []string{"endpoint", "model", "bucket", "status"})

	RequestDuration = reg.NewHistogram(prometheus.HistogramOpts{
		Name:                            "gateway_request_duration_seconds",
		Help:                            "End-to-end request duration (incl. Phase3 injection when enabled).",
		Buckets:                         latencyBuckets,
		NativeHistogramZeroThreshold:    0.001,
	})

	UpstreamDuration = reg.NewHistogram(prometheus.HistogramOpts{
		Name:    "gateway_upstream_duration_seconds",
		Help:    "Upstream (New API) round-trip duration.",
		Buckets: latencyBuckets,
	})

	UpstreamFirstByte = reg.NewHistogram(prometheus.HistogramOpts{
		Name:    "gateway_upstream_first_byte_seconds",
		Help:    "SSE first token latency.",
		Buckets: latencyBuckets,
	})
)

// --- 捕获与落库 (I4 分级 + C6) ---
var (
	// outcome: full | metadata-only | dropped
	CaptureOutcome = reg.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_capture_outcome_total",
		Help: "Capture pipeline outcome per record.",
	}, []string{"outcome"})

	CaptureChannelDepth = reg.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_capture_channel_depth",
		Help: "Current capture channel depth.",
	}, []string{"channel"}) // full | meta

	DBInsertDuration = reg.NewHistogram(prometheus.HistogramOpts{
		Name:    "gateway_db_insert_duration_seconds",
		Help:    "Insert conversation duration.",
		Buckets: prometheus.ExponentialBuckets(0.0005, 2, 12), // 0.5ms ~ 2s
	})

	DBInsertErrors = reg.NewCounter(prometheus.CounterOpts{
		Name: "gateway_db_insert_errors_total",
		Help: "Insert errors.",
	})

	DecodeFailed = reg.NewCounter(prometheus.CounterOpts{
		Name: "gateway_decode_failed_total",
		Help: "Request body decompress failures (C6).",
	})
)

// --- 流式健康 (M2) ---
var (
	StreamInterrupted = reg.NewCounter(prometheus.CounterOpts{
		Name: "gateway_stream_interrupted_total",
		Help: "SSE stream interrupted mid-flight (upstream side).",
	})

	StreamClientGone = reg.NewCounter(prometheus.CounterOpts{
		Name: "gateway_stream_client_gone_total",
		Help: "SSE client disconnected.",
	})
)

// --- caller 反查 (I3) ---
var (
	CallerLookup = reg.NewCounterVec(prometheus.CounterOpts{
		Name: "caller_lookup_total",
		Help: "Caller cache lookups.",
	}, []string{"result"}) // hit | miss | error

	CallerCacheRefreshSuccess = reg.NewGauge(prometheus.GaugeOpts{
		Name: "caller_cache_last_refresh_success_timestamp",
		Help: "Unix timestamp of last successful caller cache refresh.",
	})
)

// --- 过载保护 ---
var (
	BreakerState = reg.NewGauge(prometheus.GaugeOpts{
		Name: "gateway_breaker_state",
		Help: "Circuit breaker state: 0=closed, 1=half-open, 2=open.",
	})

	RateLimitRejected = reg.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_ratelimit_rejected_total",
		Help: "Rate-limited rejections.",
	}, []string{"bucket"}) // known | anon
)

// --- 安全 (§9) ---
var (
	RedactApplied = reg.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_redact_applied_total",
		Help: "Redactions applied per rule.",
	}, []string{"rule"})

	AuthRejected = reg.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_auth_rejected_total",
		Help: "Management endpoint auth rejections.",
	}, []string{"endpoint"}) // metrics | stats
)
