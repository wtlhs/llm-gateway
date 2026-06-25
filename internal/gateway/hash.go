package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync/atomic"
)

// sha256Hex 计算字符串的 SHA256 hex。
// 用于 token_key_hash(M3)。
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// 进程级单调计数器, 保证同毫秒/同熵也能产生唯一 gateway_id。
var gatewayCounter uint64

// newGatewayID 生成网关内唯一的追踪 id(注入 X-Ctx-Gateway-Id, 见 §5.1.1 M1)。
// 作为 llm_conversation.request_id 的值(UNIQUE 幂等键)。
func newGatewayID(r *http.Request) string {
	n := atomic.AddUint64(&gatewayCounter, 1)
	entropy := r.RemoteAddr + "|" + r.URL.String() + "|" + r.Header.Get("Authorization") + "|" + strconv.FormatUint(n, 10)
	// 32 hex(128bit) 足够唯一, 且短于列宽 64
	return sha256Hex(entropy)[:32]
}
