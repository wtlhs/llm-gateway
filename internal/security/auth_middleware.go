// Package security 实现管理端点鉴权与日志脱敏(DESIGN.md §9)。
package security

import (
	"net"
	"net/http"
	"strings"

	"github.com/company/llm-gateway/internal/metrics"
)

// AuthMiddleware 校验管理端点(/metrics, /ctx/*)的 bearer token。
// 设计依据 DESIGN.md §9(C2: 同端口路由 + bearer 鉴权)。
//
//   - token 非空: 校验 Authorization: Bearer <token>, 不匹配则 401。
//   - token 为空: 仅允许内网 IP(私网/RFC1918/loopback)访问。
func AuthMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token != "" {
				if extractBearer(r) != token {
					metrics.AuthRejected.WithLabelValues(epLabel(r.URL.Path)).Inc()
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			} else {
				// 无 token 配置: 仅内网 IP 放行
				if !isPrivate(clientIP(r)) {
					metrics.AuthRejected.WithLabelValues(epLabel(r.URL.Path)).Inc()
					http.Error(w, "forbidden: management endpoint requires intranet", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func extractBearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(v) > len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
		return strings.TrimSpace(v[len(prefix):])
	}
	return ""
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isPrivate 判定是否内网 IP(loopback + RFC1918 + RFC4193 本地唯一)。
func isPrivate(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
		return true
	}
	return false
}

func epLabel(path string) string {
	if strings.HasPrefix(path, "/metrics") {
		return "metrics"
	}
	if strings.HasPrefix(path, "/ctx") {
		return "stats"
	}
	return "other"
}
