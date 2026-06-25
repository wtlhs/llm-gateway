package gateway

import (
	"errors"
	"log/slog"
	"time"

	"github.com/company/llm-gateway/internal/metrics"
	"github.com/sony/gobreaker"
)

// ErrRateLimited 限流错误。熔断器 IsSuccessful 把它排除(不计入 New API 失败)。
var ErrRateLimited = errors.New("rate limited")

// stateToGauge 把 gobreaker 状态映射为指标值。
func stateToGauge(s gobreaker.State) float64 {
	switch s {
	case gobreaker.StateClosed:
		return 0
	case gobreaker.StateHalfOpen:
		return 1
	case gobreaker.StateOpen:
		return 2
	}
	return 0
}

// newBreaker 构造熔断器(M2)。
// 设计取舍: 只覆盖"建连 + 首字节"阶段(RoundTrip 拿到 response header 即返回),
// 流中断失败走独立指标 stream_interrupted_total, 不进熔断。
func newBreaker(failures int) *gobreaker.CircuitBreaker {
	return gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "new-api",
		MaxRequests: 5,                       // 半开试探数
		Interval:    60 * time.Second,
		Timeout:     30 * time.Second,        // 开→半开等待
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures > uint32(failures)
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			slog.Warn("breaker state change", "name", name, "from", from, "to", to)
			metrics.BreakerState.Set(stateToGauge(to))
		},
		IsSuccessful: func(err error) bool {
			// 限流错误不算 New API 失败(I2)
			return !errors.Is(err, ErrRateLimited)
		},
	})
}
