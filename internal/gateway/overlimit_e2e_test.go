package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/company/llm-gateway/internal/audit"
)

// TestForward_OverLimit_Passthrough 回归(2026-08-04): 请求体超过 preMax(32MB)
// 时, 网关必须完整透传原始 body 给上游(1M 上下文+图片附件场景),
// 不能截断后透传导致 New API 解析失败(客户端报 Request too large)。
func TestForward_OverLimit_Passthrough(t *testing.T) {
	// 上游记录收到的 body
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"id":"x","object":"chat.completion"}`))
	}))
	defer upstream.Close()

	cfg := testConfig()
	cfg.NewAPIBaseURL = upstream.URL
	cfg.MaxBodyBytes = 1 << 20
	cfg.PreBodyMaxBytes = 32 * 1024 * 1024

	spy := &mockPersister{}
	callers := audit.NewNoopCallerCache()
	pipeline := audit.NewPipeline(cfg, spy, callers)
	pipeline.Start(t.Context())
	t.Cleanup(func() { pipeline.Shutdown(t.Context()) })

	tr := NewCaptureTransportExposed(TransportConfig{
		Base:     upstream.Client().Transport,
		Pipeline: pipeline,
		Cfg:      cfg,
	})

	// 构造 >32MB 的请求体(模拟 1M 上下文, JSON 结构完整)
	msg := `{"model":"glm-5.2","messages":[{"role":"user","content":"`
	filler := strings.Repeat("x", 40*1024*1024) // 40MB
	suffix := `"}]}`
	body := []byte(msg + filler + suffix)
	if !json.Valid(body) {
		t.Fatal("test precondition: body must be valid JSON")
	}

	r := httptest.NewRequest("POST", upstream.URL+"/v1/messages", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer sk-test")

	resp, rec, err := tr.Forward(r)
	if err != nil {
		t.Fatalf("forward failed: %v", err)
	}
	defer resp.Body.Close()

	// 上游必须收到完整 body(未截断)
	if len(gotBody) != len(body) {
		t.Fatalf("upstream got %d bytes, want %d (完整透传)", len(gotBody), len(body))
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatal("upstream body mismatch (截断导致损坏)")
	}
	// 捕获被跳过(rec=nil), 请求仍成功
	if rec != nil {
		t.Fatal("capture should be skipped for over-limit body")
	}
}
