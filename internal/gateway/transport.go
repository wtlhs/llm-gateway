package gateway

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/company/llm-gateway/internal/audit"
	"github.com/company/llm-gateway/internal/config"
	"github.com/company/llm-gateway/internal/metrics"
	"github.com/sony/gobreaker"
)

// gatewayIDHeader 网关注入的追踪头(M1)。透传给 New API 便于反向联调。
const gatewayIDHeader = "X-Ctx-Gateway-Id"

// captureTransport 集成 限流→捕获还原→熔断转发, 并返回 record 给 handler。
// 注意: 不实现 http.RoundTripper 接口, 而是 Forward 方法直接返回 (resp, record, err),
// 这样流式/非流式的响应体捕获可由 handler 据实决策(见 proxy.go)。
type captureTransport struct {
	base      http.RoundTripper
	breaker   *gobreaker.CircuitBreaker
	limiters  *TokenLimiterPool
	auth      *AuthCache
	cfg       *config.Config
	endpoints map[string]struct{}
	excludes  map[string]struct{}
	push      func(*audit.Record)
}

// TransportConfig 装配所需依赖。
type TransportConfig struct {
	Base     http.RoundTripper
	Pipeline *audit.Pipeline
	Cfg      *config.Config
}

// NewCaptureTransportExposed 构造 captureTransport(导出供 main 调用)。
func NewCaptureTransportExposed(tc TransportConfig) *captureTransport {
	return newCaptureTransport(tc)
}

// newCaptureTransport 构造。
func newCaptureTransport(tc TransportConfig) *captureTransport {
	return &captureTransport{
		base:      tc.Base,
		breaker:   newBreaker(tc.Cfg.BreakerFailures),
		limiters:  NewTokenLimiterPool(tc.Cfg.RatePerCaller, tc.Cfg.RateBurst, tc.Cfg.RateAnon),
		auth:      NewAuthCache(1024),
		cfg:       tc.Cfg,
		endpoints: tc.Cfg.CaptureEndpoints(),
		excludes:  tc.Cfg.ExcludeModels(),
		push:      tc.Pipeline.Push,
	}
}

// Forward 执行一次转发。返回上游响应、关联的 record(可能为 nil, 如非捕获端点)、错误。
// 流程见 DESIGN.md §5.1.3。
func (t *captureTransport) Forward(r *http.Request) (*http.Response, *audit.Record, error) {
	endpoint := endpointOf(r.URL.Path)
	isCapture := t.isCaptureEndpoint(endpoint)
	tokenHash := t.auth.HashOf(r)

	// 0. 限流(按 caller; 在熔断之外)
	if !t.limiters.Allow(tokenHash) {
		return nil, nil, ErrRateLimited
	}

	// 1. 注入 gateway_id + 建 record 骨架(M1)
	gwID := r.Header.Get(gatewayIDHeader)
	if gwID == "" {
		gwID = newGatewayID(r)
		r.Header.Set(gatewayIDHeader, gwID)
	}

	var rec *audit.Record
	if isCapture {
		rec = audit.NewRecord(gwID, r, endpoint, false)
		rec.TokenKeyHash = tokenHash
	}

	// 2. 请求 body 捕获 + 还原(仅白名单端点; K2/K3/C3)
	if isCapture && r.Body != nil && r.ContentLength != 0 {
		snap, err := snapshotBody(r, t.cfg.PreBodyMaxBytes, t.cfg.MaxBodyBytes)
		if err != nil {
			if rec != nil {
				rec.Truncated = true
			}
		} else if len(snap.decoded) > 0 {
			rec.SetPrompt(snap.decoded, snap.truncated) // 内部修正 IsStream + Model
		}
	}

	// 3. 熔断转发(M2: 只覆盖建连+首字节)
	upstreamT0 := time.Now()
	v, err := t.breaker.Execute(func() (any, error) {
		return t.base.RoundTrip(r)
	})
	if err != nil {
		metrics.RequestTotal.WithLabelValues(endpoint, modelOr(rec, "unknown"), callerLabel(tokenHash), "error").Inc()
		return nil, rec, err
	}
	resp := v.(*http.Response)

	metrics.RequestTotal.WithLabelValues(endpoint, modelOr(rec, ""), callerLabel(tokenHash), statusLabel(resp.StatusCode)).Inc()

	// 4. 响应阶段: 读 upstream id 填入 record(M1)
	if rec != nil {
		rec.UpstreamRequestID = resp.Header.Get("X-Oneapi-Request-Id")
		rec.UpstreamT0 = upstreamT0
		if t.isExcluded(rec.Model) {
			rec.Excluded = true
		}
	}
	return resp, rec, nil
}

// pushRecord 推送 record 到 pipeline(handler 在响应体处理完后调用)。
func (t *captureTransport) pushRecord(rec *audit.Record) {
	if t.push != nil && rec != nil && !rec.Excluded {
		t.push(rec)
	}
}

// --- 判定辅助 ---

func endpointOf(path string) string {
	const prefix = "/v1/"
	if strings.HasPrefix(path, prefix) {
		return strings.TrimPrefix(path, prefix)
	}
	return strings.TrimPrefix(path, "/")
}

func (t *captureTransport) isCaptureEndpoint(ep string) bool {
	_, ok := t.endpoints[ep]
	return ok
}

func (t *captureTransport) isExcluded(model string) bool {
	if model == "" {
		return false
	}
	_, ok := t.excludes[model]
	return ok
}

// callerLabel: C5 防高基数。用 known/anon 二分, 不用 token_hash/user_id。
func callerLabel(tokenHash string) string {
	if tokenHash == "" {
		return "anon"
	}
	return "known"
}

func modelOr(rec *audit.Record, fallback string) string {
	if rec == nil {
		return fallback
	}
	return rec.ModelSafe()
}

func statusLabel(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	}
	return "2xx"
}

var _ = errors.New
