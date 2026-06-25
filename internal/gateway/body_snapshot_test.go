package gateway

import (
	"bytes"
	"compress/gzip"
	"testing"
)

// TestDecodeMaybe_Gzip 验证 gzip 请求体解压(K3)。
func TestDecodeMaybe_Gzip(t *testing.T) {
	original := []byte(`{"model":"gpt-4o","messages":[]}`)
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(original)
	w.Close()

	decoded, trunc, err := decodeMaybe(gz.Bytes(), "gzip", 1<<20)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if trunc {
		t.Fatal("should not be truncated")
	}
	if !bytes.Equal(decoded, original) {
		t.Fatalf("mismatch: got %s want %s", decoded, original)
	}
}

// TestDecodeMaybe_Truncation 验证 postMax 截断标记 truncated=true。
func TestDecodeMaybe_Truncation(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 1000)
	decoded, trunc, err := decodeMaybe(big, "", 100)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !trunc {
		t.Fatal("expected truncated=true")
	}
	if len(decoded) != 100 {
		t.Fatalf("decoded len = %d, want 100", len(decoded))
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
