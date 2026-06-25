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

loop:
	for {
		select {
		case <-ctx.Done():
			metrics.StreamInterrupted.Inc()
			break loop
		default:
		}

		n, rerr := upstream.Read(buf)
		if n > 0 {
			if firstByte {
				firstByte = false
				metrics.UpstreamFirstByte.Observe(time.Since(upstreamT0).Seconds())
			}
			// (a) 优先转发给客户端; 客户端断开则停止
			if _, werr := w.Write(buf[:n]); werr != nil {
				metrics.StreamClientGone.Inc()
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
				metrics.StreamInterrupted.Inc()
			}
			break loop
		}
	}

	rec.UpstreamLatencyMs = int32(time.Since(upstreamT0).Milliseconds())
	rec.HTTPStatus = http.StatusOK
	rec.Finalize()
	push(rec)
}
