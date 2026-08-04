package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/binary"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// sha256Hex 计算字符串的 SHA256 hex。
// 用于 token_key_hash(M3)。
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// 进程级单调计数器, 保证同毫秒/同熵也能产生唯一 gateway_id。
var gatewayCounter uint64

// randUint64 用 crypto/rand 生成随机数(跨重启唯一性来源)。
func randUint64() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint64(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint64(b[:])
}

// newGatewayID 生成网关内唯一的追踪 id(注入 X-Ctx-Gateway-Id, 见 §5.1.1 M1)。
// 作为 llm_conversation.request_id 的值(UNIQUE 幂等键)。
//
// 修复(2026-08-04): 原先熵为 RemoteAddr|URL|Authorization|进程内原子计数器,
// 网关重启后计数器归零, 相同特征组合(同一用户 token + 同一端点 + 相同 RemoteAddr)
// 会生成与重启前完全相同的 request_id → INSERT 时 ON CONFLICT DO NOTHING 静默冲突
// (ErrDuplicate), 记录(含 client_gone 中断诊断)被丢弃且无错误日志。
// 现在加入时间戳纳秒 + crypto/rand 随机数, 保证跨重启/跨连接唯一。
func newGatewayID(r *http.Request) string {
	n := atomic.AddUint64(&gatewayCounter, 1)
	entropy := r.RemoteAddr + "|" + r.URL.String() + "|" + r.Header.Get("Authorization") + "|" +
		strconv.FormatUint(n, 10) + "|" +
		strconv.FormatInt(time.Now().UnixNano(), 36) + "|" +
		strconv.FormatUint(randUint64(), 36)
	// 32 hex(128bit) 足够唯一, 且短于列宽 64
	return sha256Hex(entropy)[:32]
}
