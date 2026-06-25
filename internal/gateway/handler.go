package gateway

import (
	"net/http"
	"strings"
)

// IsLLMEndpoint 判定 path 是否为需要代理的 /v1/* LLM 端点(含 realtime)。
func IsLLMEndpoint(path string) bool {
	return strings.HasPrefix(path, "/v1/")
}

// NewMux 构造主路由。
// 顺序: /metrics, /ctx/* (鉴权后), /v1/realtime (stub), 其余 /v1/* → proxy。
func NewMux(proxy *Proxy, metricsHandler, statsHandler http.Handler, adminAuth func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()

	// 管理端点: 需鉴权(C2)
	if metricsHandler != nil {
		mux.Handle("/metrics", adminAuth(metricsHandler))
	}
	if statsHandler != nil {
		mux.Handle("/ctx/", adminAuth(statsHandler))
		mux.Handle("/ctx", adminAuth(statsHandler))
	}

	// WS realtime: stub
	mux.HandleFunc("/v1/realtime", realtimePassthrough)

	// 其余 /v1/* → 透明代理
	mux.Handle("/v1/", proxy)

	return mux
}
