package gateway

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/company/llm-gateway/internal/audit"
	"github.com/company/llm-gateway/internal/metrics"
)

// sseCaptureLoop 流式透传 + 尽力捕获(K1 修正)。
// 单 goroutine: 边转发给客户端, 边累积进 record; 捕获永不返 error, 不影响转发。
// 流结束后 Finalize + push(M1 时序: 响应阶段才推)。
//
// 注意: 必须在 handler 拿到 ResponseWriter 后直接调用, 而非经 httputil.ReverseProxy,
// 因为 ReverseProxy 的默认 io.Copy 无法触发本函数。
func sseCaptureLoop(ctx context.Context, upstream io.ReadCloser, w http.ResponseWriter, rec *audit.Record, push func(*audit.Record), maxBytes int64, upstreamT0 time.Time) {
	defer upstream.Close()
	flusher, _ := w.(http.Flusher)

	buf := make([]byte, 32*1024)
	firstByte := true
	var lastUpstreamChunk []byte // 上游最后一段原始数据, 中断时用于诊断
	const lastChunkMax = 4096

loop:
	for {
		select {
		case <-ctx.Done():
			metrics.StreamInterrupted.WithLabelValues("context_canceled").Inc()
			rec.SetStreamError(http.StatusGatewayTimeout, "context canceled", lastUpstreamChunk)
			break loop
		default:
		}

		n, rerr := upstream.Read(buf)
		if n > 0 {
			if firstByte {
				firstByte = false
				metrics.UpstreamFirstByte.Observe(time.Since(upstreamT0).Seconds())
			}
			// 保留最后一段上游原始数据用于诊断(限制长度)
			lastUpstreamChunk = appendLastChunk(lastUpstreamChunk, buf[:n], lastChunkMax)
			// (a) 优先转发给客户端; 客户端断开则停止
			if _, werr := w.Write(buf[:n]); werr != nil {
				metrics.StreamClientGone.Inc()
				rec.SetStreamError(499, "client gone: "+werr.Error(), lastUpstreamChunk)
				break loop
			}
			if flusher != nil {
				flusher.Flush() // 立即 flush, 零延迟
			}
			// (b) 尽力累积进 rec; 永不返回 error
			rec.AppendCapture(buf[:n], maxBytes)
		}
		if rerr != nil {
			if rerr != io.EOF {
				metrics.StreamInterrupted.WithLabelValues("upstream_read_error").Inc()
				rec.SetStreamError(http.StatusBadGateway, "upstream read error: "+rerr.Error(), lastUpstreamChunk)
			}
			break loop
		}
	}

	rec.UpstreamLatencyMs = int32(time.Since(upstreamT0).Milliseconds())
	if rec.HTTPStatus == 0 {
		rec.HTTPStatus = http.StatusOK
	}
	rec.Finalize()
	push(rec)
}

// appendLastChunk 保留最近 up to max 字节的上游原始数据, 用于中断诊断。
func appendLastChunk(prev, chunk []byte, max int) []byte {
	if len(chunk) >= max {
		return append([]byte(nil), chunk[len(chunk)-max:]...)
	}
	total := len(prev) + len(chunk)
	if total > max {
		prev = prev[total-max:]
	}
	return append(prev, chunk...)
}
