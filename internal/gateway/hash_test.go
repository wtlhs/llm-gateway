package gateway

import (
	"net/http/httptest"
	"testing"
)

// TestNewGatewayID_UniqueAcrossRestarts 回归: 重启后计数器归零,
// 相同特征组合(同 URL/Auth/RemoteAddr)不得生成相同 request_id,
// 否则 INSERT ON CONFLICT DO NOTHING 静默冲突丢记录(2026-08-04 线上事故)。
func TestNewGatewayID_UniqueAcrossRestarts(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 500; i++ {
		r := httptest.NewRequest("POST", "/v1/messages", nil)
		r.RemoteAddr = "127.0.0.1:54321"
		r.Header.Set("Authorization", "Bearer sk-same-token")
		id := newGatewayID(r)
		if seen[id] {
			t.Fatalf("duplicate gateway_id: %s", id)
		}
		seen[id] = true
	}
}
