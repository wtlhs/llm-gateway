package gateway

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// TestDecodeMaybe_Gzip 验证 gzip 请求体解压(K3)。
func TestDecodeMaybe_Gzip(t *testing.T) {
	original := []byte(`{"model":"gpt-4o","messages":[]}`)
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(original)
	w.Close()

	decoded, tail, trunc, err := decodeMaybe(gz.Bytes(), "gzip", 1<<20)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if trunc {
		t.Fatal("should not be truncated")
	}
	if tail != nil {
		t.Fatalf("tail should be nil when not truncated, got %q", tail)
	}
	if !bytes.Equal(decoded, original) {
		t.Fatalf("mismatch: got %s want %s", decoded, original)
	}
}

// TestDecodeMaybe_Truncation 验证 postMax 截断标记 truncated=true。
func TestDecodeMaybe_Truncation(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 1000)
	decoded, tail, trunc, err := decodeMaybe(big, "", 100)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !trunc {
		t.Fatal("expected truncated=true")
	}
	if len(decoded) != 100 {
		t.Fatalf("decoded len = %d, want 100", len(decoded))
	}
	// 截断时必须保留尾部窗口供 model/stream 探测
	if len(tail) != 100 {
		t.Fatalf("tail len = %d, want 100", len(tail))
	}
	if string(tail) != string(big[len(big)-100:]) {
		t.Fatalf("tail mismatch: got %q want suffix", tail)
	}
}

// TestDecodeMaybe_TruncationTail_ModelInTail 回归: 大请求体截断后, model/stream
// 位于请求体尾部(Claude Code /v1/messages 的 context_management 前置), 尾部窗口
// 必须保留这些字段, 否则 is_stream 误判为非流式 → 缓冲 SSE → 客户端超时 client_gone。
func TestDecodeMaybe_TruncationTail_ModelInTail(t *testing.T) {
	head := []byte(`{"context_management":{"edits":[]},"max_tokens":64000,"messages":[{"role":"user","content":"`)
	filler := bytes.Repeat([]byte("x"), 50000)
	tailJSON := []byte(`"}],"model":"glm-5.2","stream":true}`)
	body := append(append(append([]byte{}, head...), filler...), tailJSON...)

	decoded, tail, trunc, err := decodeMaybe(body, "", 1000)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !trunc {
		t.Fatal("expected truncated=true")
	}
	if len(decoded) != 1000 {
		t.Fatalf("decoded len = %d, want 1000", len(decoded))
	}
	// 尾部必须包含 model/stream(截断点在中间, 关键字段在尾)
	joined := string(tail)
	if !strings.Contains(joined, "\"model\":\"glm-5.2\"") || !strings.Contains(joined, "\"stream\":true") {
		t.Fatalf("tail missing model/stream: %q", joined)
	}
}

// TestDecodeMaybe_TruncationTail_Gzip 回归: gzip 压缩的大请求体截断后,
// 尾部窗口必须滑到解压流的真实末尾(Claude Code 默认 gzip 压缩)。
func TestDecodeMaybe_TruncationTail_Gzip(t *testing.T) {
	head := []byte(`{"context_management":{"edits":[]},"max_tokens":64000,"messages":[{"role":"user","content":"`)
	filler := bytes.Repeat([]byte("x"), 50000)
	tailJSON := []byte(`"}],"model":"glm-5.2","stream":true}`)
	body := append(append(append([]byte{}, head...), filler...), tailJSON...)

	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(body)
	w.Close()

	decoded, tail, trunc, err := decodeMaybe(gz.Bytes(), "gzip", 1000)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !trunc {
		t.Fatal("expected truncated=true")
	}
	if len(decoded) != 1000 {
		t.Fatalf("decoded len = %d, want 1000", len(decoded))
	}
	joined := string(tail)
	if !strings.Contains(joined, "\"model\":\"glm-5.2\"") || !strings.Contains(joined, "\"stream\":true") {
		t.Fatalf("gzip tail missing model/stream: %q", joined)
	}
}

// TestEndpointOf 端点提取。
func TestEndpointOf(t *testing.T) {
	cases := map[string]string{
		"/v1/chat/completions":   "chat/completions",
		"/v1/embeddings":         "embeddings",
		"/v1/realtime":           "realtime",
		"/healthz":               "healthz",
	}
	for in, want := range cases {
		if got := endpointOf(in); got != want {
			t.Errorf("endpointOf(%q)=%q want %q", in, got, want)
		}
	}
}

// TestNormalizeEncoding 大小写规范化。
func TestNormalizeEncoding(t *testing.T) {
	if got := normalizeEncoding("GZIP"); got != "gzip" {
		t.Errorf("got %q", got)
	}
	if got := normalizeEncoding("  Br "); got != "br" {
		t.Errorf("got %q", got)
	}
}

// TestRestoreBody 验证 K2: body 还原后可重复读。
func TestRestoreBody(t *testing.T) {
	r := newReqWithBody([]byte("payload"))
	snap, err := snapshotBody(r, 1<<20, 1<<20)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// 验证 decoded 等于原始(identity 编码)
	if string(snap.decoded) != "payload" {
		t.Fatalf("decoded=%q", snap.decoded)
	}
	// 验证 r.Body 已还原, 可再读(模拟 RoundTrip 转发)
	buf := make([]byte, 100)
	n, _ := r.Body.Read(buf)
	if string(buf[:n]) != "payload" {
		t.Fatalf("restored body = %q, want payload", buf[:n])
	}
}
