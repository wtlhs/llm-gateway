package gateway

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/company/llm-gateway/internal/audit"
	"github.com/company/llm-gateway/internal/metrics"
)

// Proxy 是核心反向代理 handler。
// 设计取舍: 不用 httputil.ReverseProxy, 而是自定义 handler,
// 因为流式(SSE)需要直接控制 ResponseWriter + 旁路聚合(K1/M1)。
type Proxy struct {
	transport *captureTransport
	newAPI    string
	maxBytes  int64
}

// NewProxy 构造。
func NewProxy(t *captureTransport, newAPIBase string, maxBytes int64) *Proxy {
	return &Proxy{transport: t, newAPI: strings.TrimRight(newAPIBase, "/"), maxBytes: maxBytes}
}

// ServeHTTP 处理一次 /v1/* 转发。
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := r.Context()

	// 构造上游请求: 复用原始 Method/URL/Body/Header, 目标指向 New API
	upReq, err := http.NewRequestWithContext(ctx, r.Method, p.newAPI+r.RequestURI, r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	copyHeaders(upReq.Header, r.Header)

	// 走 transport 完整流程: 限流→捕获 prompt→熔断→转发, 拿到 resp + record
	resp, rec, err := p.transport.Forward(upReq)
	if err != nil {
		status := http.StatusBadGateway
		if err == ErrRateLimited {
			status = http.StatusTooManyRequests
		}
		http.Error(w, "upstream unavailable", status)
		metrics.RequestDuration.Observe(time.Since(start).Seconds())
		return
	}
	defer resp.Body.Close()

	// 复制响应头 + 状态码给客户端
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	// 响应体处理: 分流式/非流式
	if rec != nil && rec.IsStream {
		// 流式(K1): sseCaptureLoop 边透传边聚合, 结束后 push(M1)
		sseCaptureLoop(ctx, resp.Body, w, rec, p.transport.pushRecord, p.maxBytes, rec.UpstreamT0)
	} else if rec != nil {
		// 非流式: 透传 + 累积副本, 结束后 push
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, p.maxBytes+1))
		truncated := int64(len(buf)) > p.maxBytes
		if truncated {
			buf = buf[:p.maxBytes]
			rec.Truncated = true
		}
		w.Write(buf)
		rec.UpstreamLatencyMs = int32(time.Since(rec.UpstreamT0).Milliseconds())
		rec.Finalize()
		if resp.StatusCode >= 400 {
			rec.SetError(resp.StatusCode, buf)
		} else {
			rec.SetNonStreamCompletion(buf)
		}
		p.transport.pushRecord(rec)
	} else {
		// 非捕获端点(C/D 类): 纯透传
		io.Copy(w, resp.Body)
	}

	metrics.RequestDuration.Observe(time.Since(start).Seconds())
}

// copyHeaders 浅拷贝 header(剔除 hop-by-hop)。
func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func isHopByHop(h string) bool {
	switch strings.ToLower(h) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade", "content-length":
		return true
	}
	return false
}

// 防止未使用 import 警告(audit 用于类型注释)。
var _ = (*audit.Record)(nil)
