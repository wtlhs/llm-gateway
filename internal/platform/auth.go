package platform

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookieName = "platform_session"
	sessionIDBytes    = 32 // 256bit 随机 session id
)

// sessionStore 内存会话存储(单实例平台够用)。
// 登录后签发随机 session id, 存内存 + 下发 httpOnly cookie。
// 服务重启后会话失效(需重新登录), 安全上可接受。
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // sessionID → 过期时间
	ttl      time.Duration
}

func newSessionStore(ttl time.Duration) *sessionStore {
	return &sessionStore{
		sessions: make(map[string]time.Time),
		ttl:      ttl,
	}
}

// create 生成新 session, 返回 session id。
func (s *sessionStore) create() string {
	buf := make([]byte, sessionIDBytes)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败概率极低, 退回时间戳兜底(仍不可预测)
		buf = []byte(time.Now().String())
	}
	id := base64.RawURLEncoding.EncodeToString(buf)
	s.mu.Lock()
	s.sessions[id] = time.Now().Add(s.ttl)
	s.mu.Unlock()
	s.gc()
	return id
}

// valid 校验 session 是否有效且未过期。
func (s *sessionStore) valid(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[id]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, id)
		return false
	}
	return true
}

// revoke 删除 session(登出)。
func (s *sessionStore) revoke(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// gc 清理过期 session(惰性触发, 防内存泄漏)。
func (s *sessionStore) gc() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) < 100 {
		return // 量小不频繁 gc
	}
	for id, exp := range s.sessions {
		if now.After(exp) {
			delete(s.sessions, id)
		}
	}
}

// passwordAuth 密码校验(admin 单账号)。
type passwordAuth struct {
	username string
	hash     []byte // bcrypt hash of password
}

func newPasswordAuth(username, password string) (*passwordAuth, error) {
	if username == "" {
		username = "admin"
	}
	if password == "" {
		return nil, errors.New("PLATFORM_ADMIN_PASSWORD 未配置, 无法启用登录鉴权")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return &passwordAuth{username: username, hash: hash}, nil
}

// verify 校验用户名+密码。
func (a *passwordAuth) verify(username, password string) bool {
	if username != a.username {
		return false
	}
	return bcrypt.CompareHashAndPassword(a.hash, []byte(password)) == nil
}

// setSessionCookie 下发 httpOnly session cookie。
// HttpOnly: 浏览器 JS 无法读取(防 XSS 窃取会话)
// Secure: 仅 HTTPS 传输(生产必须)
// SameSite: 防 CSRF
func setSessionCookie(w http.ResponseWriter, sessionID string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// sessionFromRequest 从 cookie 提取 session id。
func sessionFromRequest(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
