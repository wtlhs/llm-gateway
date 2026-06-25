package gateway

import (
	"sync"

	"github.com/company/llm-gateway/internal/metrics"
	"golang.org/x/time/rate"
)

// TokenLimiterPool 按 caller(token hash)维度维护令牌桶, 另含一个共享匿名桶。
// 设计依据 DESIGN.md §5.1.3 / §6.1: burst/rate 分离(I2), 无 Authorization 走匿名桶。
type TokenLimiterPool struct {
	mu sync.Mutex

	perCaller map[string]*rate.Limiter
	rate      rate.Limit // 稳态
	burst     int        // 突发

	anonBucket *rate.Limiter // 共享匿名桶
}

// NewTokenLimiterPool 构造。
//   - perCaller rate = ratePerCaller, burst = rateBurst
//   - 匿名桶 rate = rateAnon, burst = rateAnon(无突发优待)
func NewTokenLimiterPool(ratePerCaller, rateBurst, rateAnon int) *TokenLimiterPool {
	p := &TokenLimiterPool{
		perCaller: make(map[string]*rate.Limiter),
		rate:      rate.Limit(ratePerCaller),
		burst:     rateBurst,
		anonBucket: rate.NewLimiter(rate.Limit(rateAnon), rateAnon),
	}
	return p
}

// Allow 判定是否放行。hash 为空走匿名桶。
func (p *TokenLimiterPool) Allow(hash string) bool {
	if hash == "" {
		ok := p.anonBucket.Allow()
		if !ok {
			metrics.RateLimitRejected.WithLabelValues("anon").Inc()
		}
		return ok
	}
	l := p.getOrCreate(hash)
	ok := l.Allow()
	if !ok {
		metrics.RateLimitRejected.WithLabelValues("known").Inc()
	}
	return ok
}

func (p *TokenLimiterPool) getOrCreate(hash string) *rate.Limiter {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.perCaller[hash]; ok {
		return l
	}
	l := rate.NewLimiter(p.rate, p.burst)
	p.perCaller[hash] = l
	return l
}
