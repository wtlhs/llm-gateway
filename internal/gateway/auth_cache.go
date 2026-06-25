package gateway

import (
	"net/http"
	"strings"
	"sync"
)

// AuthCache 解析 Authorization 头, 缓存 hash(M3: 绝不缓存原文 sk-xxx)。
// 设计依据 DESIGN.md §5.1.3 / §5.4 M3 安全约束。
//
// 缓存语义: key = 原始 token 字符串(仅作 map 查找键, 单次请求内短暂存在),
// 值 = sha256 hash。注意这是有意识的权衡:为了让 map 查找 O(1), key 用原文;
// 但 value 是 hash, 用于落库与反查。若严格杜绝内存常驻原文, 可改为不做缓存
// (每请求现算), 由 LRU 大小约束风险面。Phase 1 用小 LRU。
type AuthCache struct {
	mu    sync.RWMutex
	store map[string]string // rawKey -> sha256(rawKey), 容量受限
	cap   int
}

// NewAuthCache 构造, cap 控制 LRU 风险面(默认较小)。
func NewAuthCache(cap int) *AuthCache {
	if cap <= 0 {
		cap = 1024
	}
	return &AuthCache{store: make(map[string]string, cap), cap: cap}
}

// extractBearer 从请求头取出 Bearer token 原文(可能为空)。
// "Authorization: Bearer sk-xxx" → "sk-xxx"
func extractBearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(v) > len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
		return strings.TrimSpace(v[len(prefix):])
	}
	return ""
}

// HashOf 返回当前请求 token 的 sha256 hash(用于落库 + 反查)。
// 原文 token 仅在此函数栈帧内短暂存在。
func (a *AuthCache) HashOf(r *http.Request) string {
	raw := extractBearer(r)
	if raw == "" {
		return ""
	}
	// 缓存命中: 避免每请求重算 sha256
	a.mu.RLock()
	if h, ok := a.store[raw]; ok {
		a.mu.RUnlock()
		return h
	}
	a.mu.RUnlock()

	h := sha256Hex(raw)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.store) >= a.cap {
		// 简单淘汰: 删一个任意 key(LRU 需额外结构, Phase1 用随机淘汰足够)
		for k := range a.store {
			delete(a.store, k)
			break
		}
	}
	a.store[raw] = h
	return h
}
