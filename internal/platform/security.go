package platform

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ipAllowListMiddleware IP 白名单。仅允许配置的来源访问。
// 空白名单=不限(开发模式友好); 配置后非白名单 IP 返回 403。
// 信任 X-Forwarded-For 首段(经 nginx 反代场景)或 X-Real-IP。
func (s *Server) ipAllowListMiddleware(next http.Handler) http.Handler {
	nets := s.cfg.AllowIPSet()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(nets) == 0 {
			next.ServeHTTP(w, r) // 未配置白名单, 放行
			return
		}
		clientIP := extractClientIP(r)
		ip := net.ParseIP(clientIP)
		if ip == nil {
			s.warnOnce("ip parse failed", "ip", clientIP)
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		for _, n := range nets {
			if n.Contains(ip) {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeError(w, http.StatusForbidden, "forbidden")
	})
}

// rateLimitMiddleware 速率限制(令牌桶, 每 IP 独立)。
// 防止单个来源暴力枚举 token 或爬取全量对话数据。
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	var mu sync.Mutex
	visitors := make(map[string]*rate.Limiter)
	// 每 IP: rps = limit/60, burst = limit/10(允许短时突发)
	rps := rate.Limit(float64(s.cfg.RateLimitPerMin) / 60.0)
	burst := s.cfg.RateLimitPerMin / 10
	if burst < 5 {
		burst = 5
	}

	getLimiter := func(ip string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		l, exists := visitors[ip]
		if !exists {
			l = rate.NewLimiter(rps, burst)
			visitors[ip] = l
		}
		return l
	}

	// 定期清理过期条目(防内存泄漏)
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mu.Lock()
			// 简单策略: 超过 500 个条目时清空(防无限增长)
			if len(visitors) > 500 {
				visitors = make(map[string]*rate.Limiter)
			}
			mu.Unlock()
		}
	}()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractClientIP(r)
		if !getLimiter(ip).Allow() {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeadersMiddleware 安全响应头。
// 防止点击劫持、MIME 嗅探、XSS 等(平台含敏感对话数据, 必须加固)。
func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY") // 禁止被 iframe 嵌入(防点击劫持)
		h.Set("X-XSS-Protection", "1; mode=block")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self' data:; object-src 'none'; base-uri 'self'")
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains") // 强制 HTTPS
		next.ServeHTTP(w, r)
	})
}

// authMiddleware 增强: token 为空时拒绝生产访问(仅开发模式放行 localhost)。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// token 未配置: 仅允许 localhost 访问(开发场景)
		if s.cfg.AuthToken == "" {
			ip := extractClientIP(r)
			if isLocalhost(ip) {
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusUnauthorized, "auth token not configured")
			return
		}
		// 优先 Bearer 头, 回退查询参数(?token=, 仅 export 场景)
		cred := extractBearer(r)
		if cred == "" {
			cred = r.URL.Query().Get("token")
		}
		if cred != s.cfg.AuthToken {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// warnOnce 速率受限的 warn(复用 Server 的 logLimiter, 如果有的话)。
func (s *Server) warnOnce(msg string, args ...any) {
	slog.Warn(msg, args...)
}

// extractClientIP 从请求中提取客户端真实 IP。
// 优先 X-Forwarded-For 首段(经反代), 其次 X-Real-IP, 最后 RemoteAddr。
func extractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.Index(xff, ","); idx > 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isLocalhost(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1" || ip == "localhost"
}
