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
	cfg   *Config
	store *db.Store
}

// New 构造平台服务。
func New(cfg *Config, store *db.Store) *Server {
	return &Server{cfg: cfg, store: store}
}

// Handler 装配全部路由(Go 1.22+ ServeMux 增强路由, 支持路径参数 {id})。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 鉴权 + CORS + JSON 中间件链
	wrap := func(hf http.HandlerFunc) http.Handler {
		return s.corsMiddleware(s.authMiddleware(s.jsonMiddleware(hf)))
	}

	h := handler.New(s.store, s.cfg.Timezone, s.cfg.QueryTimeout)

	// 总览看板
	mux.Handle("GET /api/v1/dashboard/overview", wrap(h.DashboardOverview))
	mux.Handle("GET /api/v1/dashboard/trend", wrap(h.DashboardTrend))
	mux.Handle("GET /api/v1/dashboard/top-models", wrap(h.TopModels))
	mux.Handle("GET /api/v1/dashboard/top-callers", wrap(h.TopCallers))

	// 对话检索
	mux.Handle("GET /api/v1/conversations", wrap(h.ListConversations))
	mux.Handle("GET /api/v1/conversations/{id}", wrap(h.GetConversation))

	// 知识库(配置层)
	mux.Handle("GET /api/v1/knowledge/configs", wrap(h.ListSystemPrompts))
	mux.Handle("GET /api/v1/knowledge/configs/{hash}", wrap(h.GetSystemPrompt))

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

// authMiddleware Bearer Token 鉴权。token 为空时跳过(本地开发友好)。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AuthToken == "" {
			next.ServeHTTP(w, r) // 无 token 配置, 放行(内网/开发场景)
			return
		}
		bearer := extractBearer(r)
		if bearer != s.cfg.AuthToken {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// jsonMiddleware 设置 JSON 响应头。
func (s *Server) jsonMiddleware(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next(w, r)
	})
}

// corsMiddleware 简单 CORS(前端独立部署时需要)。
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
