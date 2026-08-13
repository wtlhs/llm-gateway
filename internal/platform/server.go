package platform

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/company/llm-gateway/internal/db"
	"github.com/company/llm-gateway/internal/platform/handler"
)

// Server 平台 HTTP 服务。
type Server struct {
	cfg    *Config
	store  *db.Store
	auth   *passwordAuth
	sess   *sessionStore
}

// New 构造平台服务。
// 若配置了管理员密码, 初始化密码校验 + 会话存储; 否则 auth 为 nil(登录不可用)。
func New(cfg *Config, store *db.Store) *Server {
	s := &Server{cfg: cfg, store: store}
	if cfg.AdminPassword != "" {
		if a, err := newPasswordAuth(cfg.AdminUser, cfg.AdminPassword); err == nil {
			s.auth = a
			s.sess = newSessionStore(cfg.SessionTTL)
		} else {
			slog.Warn("password auth init failed", "err", err)
		}
	}
	return s
}

// Handler 装配全部路由(Go 1.22+ ServeMux 增强路由, 支持路径参数 {id})。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 安全中间件链(外→内):
	// 安全响应头 → CORS → IP白名单 → 速率限制 → 鉴权 → JSON
	wrap := func(hf http.HandlerFunc) http.Handler {
		return s.securityHeadersMiddleware(
			s.corsMiddleware(
				s.ipAllowListMiddleware(
					s.rateLimitMiddleware(
						s.authMiddleware(
							s.jsonMiddleware(hf))))))
	}

	h := handler.New(s.store, s.cfg.Timezone, s.cfg.QueryTimeout)

	// 认证(免 token, 但受 IP白名单+限流保护): 登录/登出
	mux.Handle("POST /api/v1/auth/login", s.securityHeadersMiddleware(
		s.corsMiddleware(s.ipAllowListMiddleware(s.rateLimitMiddleware(http.HandlerFunc(s.loginHandler))))))
	mux.Handle("POST /api/v1/auth/logout", wrap(s.logoutHandler))
	// 当前登录态(供前端判断是否需要跳登录页)
	mux.Handle("GET /api/v1/auth/me", wrap(s.meHandler))

	// 总览看板
	mux.Handle("GET /api/v1/dashboard/overview", wrap(h.DashboardOverview))
	mux.Handle("GET /api/v1/dashboard/trend", wrap(h.DashboardTrend))
	mux.Handle("GET /api/v1/dashboard/top-models", wrap(h.TopModels))
	mux.Handle("GET /api/v1/dashboard/top-callers", wrap(h.TopCallers))
	mux.Handle("GET /api/v1/dashboard/hourly", wrap(h.HourlyTrend))
	mux.Handle("GET /api/v1/dashboard/endpoints", wrap(h.EndpointDist))
	mux.Handle("GET /api/v1/dashboard/model-stats", wrap(h.ModelStats))

	// 对话检索
	mux.Handle("GET /api/v1/conversations", wrap(h.ListConversations))
	mux.Handle("GET /api/v1/conversations/{id}", wrap(h.GetConversation))
	mux.Handle("GET /api/v1/conversations/export", wrap(h.ExportConversations))

	// 知识库(配置层)
	mux.Handle("GET /api/v1/knowledge/configs", wrap(h.ListSystemPrompts))
	mux.Handle("GET /api/v1/knowledge/configs/{hash}", wrap(h.GetSystemPrompt))
	mux.Handle("GET /api/v1/knowledge/stats", wrap(h.KnowledgeStats))
	// 知识库(问答对层)
	mux.Handle("GET /api/v1/knowledge/search", wrap(h.SearchKnowledgePairs))
	mux.Handle("GET /api/v1/knowledge/pair-stats", wrap(h.KnowledgePairStats))

	// 运维监控
	mux.Handle("GET /api/v1/ops/db-stats", wrap(h.DBStats))
	mux.Handle("GET /api/v1/ops/data-quality", wrap(h.DataQuality))
	mux.Handle("GET /api/v1/ops/latency", wrap(h.LatencyDist))

	// 健康检查(免鉴权)
	mux.HandleFunc("GET /api/v1/health", s.healthHandler)

	// 前端静态文件(embed web/dist, 单进程 serve SPA)
	mux.Handle("/", s.staticHandler())

	return mux
}

// staticHandler serve 前端 SPA(embed web/dist)。
// 生产构建后 web/dist 会被 embed 进二进制; 开发时前端走 vite dev server (5173)。
func (s *Server) staticHandler() http.Handler {
	dist, err := distFS()
	if err != nil {
		// 无前端构建产物(开发模式), 返回提示
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("platform api running. frontend dev mode: run `pnpm dev` in web/"))
		})
	}
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SPA: 非 /api 且非真实文件的请求回退到 index.html
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			if _, err := dist.Open(strings.TrimPrefix(r.URL.Path, "/")); err != nil {
				r.URL.Path = "/"
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

// jsonMiddleware 设置 JSON 响应头。
func (s *Server) jsonMiddleware(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next(w, r)
	})
}

// corsMiddleware CORS(生产建议设具体域名)。
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.cfg.CORSOrigins)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.store.Pool().Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db unreachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// loginHandler 登录: 校验用户名+密码, 签发 httpOnly session cookie。
func (s *Server) loginHandler(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusServiceUnavailable, "登录未配置(缺少 PLATFORM_ADMIN_PASSWORD)")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !s.auth.verify(req.Username, req.Password) {
		// 统一 401, 不泄露"用户名或密码哪个错"
		writeError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	sid := s.sess.create()
	setSessionCookie(w, sid, s.cfg.SessionTTL)
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"user": s.cfg.AdminUser}})
}

// logoutHandler 登出: 吊销会话 + 清 cookie。
func (s *Server) logoutHandler(w http.ResponseWriter, r *http.Request) {
	if sid := sessionFromRequest(r); sid != "" && s.sess != nil {
		s.sess.revoke(sid)
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"code": 0})
}

// meHandler 返回当前登录态(供前端判断)。
func (s *Server) meHandler(w http.ResponseWriter, r *http.Request) {
	// 走到这里说明已通过 authMiddleware(会话有效)
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"user": s.cfg.AdminUser}})
}

// --- 辅助 ---

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	slog.Warn("platform api error", "code", code, "msg", msg, "path", "")
	writeJSON(w, code, map[string]any{"code": code, "error": msg})
}
