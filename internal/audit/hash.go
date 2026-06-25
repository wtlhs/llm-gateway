package audit

import (
	"crypto/sha256"
	"encoding/hex"
)

// SHA256Hex 计算 string 的 SHA256 并返回 hex(导出版本, 供 main 使用)。
func SHA256Hex(s string) string {
	return sha256Hex(s)
}

// sha256Hex 计算字符串的 SHA256 并返回 hex。
// 用于 token_key_hash(M3: 不落盘明文 key)与 request_body_hash(去重统计)。
func sha256Hex[T string | []byte](s T) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
