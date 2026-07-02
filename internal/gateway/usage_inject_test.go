package gateway

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"testing"
)

// TestInjectIncludeUsage_Plain 流式明文 body, 无 stream_options → 应注入。
func TestInjectIncludeUsage_Plain(t *testing.T) {
	orig := []byte(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	snap := &bodySnapshot{raw: orig, decoded: orig}

	changed := injectIncludeUsage(snap, "")
	if !changed {
		t.Fatal("应注入 include_usage, 但返回 changed=false")
	}

	// decoded 应含 stream_options.include_usage=true
	var m map[string]any
	if err := json.Unmarshal(snap.decoded, &m); err != nil {
		t.Fatalf("decoded 非法 JSON: %v", err)
	}
	so, ok := m["stream_options"].(map[string]any)
	if !ok {
		t.Fatal("缺少 stream_options 字段")
	}
	if so["include_usage"] != true {
		t.Fatalf("include_usage=%v, want true", so["include_usage"])
	}

	// raw 应等于 decoded(明文编码)
	if !bytes.Equal(snap.raw, snap.decoded) {
		t.Fatalf("raw 与 decoded 不一致: raw=%s decoded=%s", snap.raw, snap.decoded)
	}

	// 原字段保留
	if m["model"] != "glm-5.2" {
		t.Fatalf("model 丢失: %v", m["model"])
	}
	if m["stream"] != true {
		t.Fatalf("stream 丢失")
	}
}

// TestInjectIncludeUsage_AlreadyPresent 客户端已带 include_usage=true → 幂等, 不改。
func TestInjectIncludeUsage_AlreadyPresent(t *testing.T) {
	orig := []byte(`{"model":"x","stream":true,"stream_options":{"include_usage":true},"messages":[]}`)
	snap := &bodySnapshot{raw: orig, decoded: orig}

	changed := injectIncludeUsage(snap, "")
	if changed {
		t.Fatal("已带 include_usage=true 应幂等不改, 但返回 changed=true")
	}
	if !bytes.Equal(snap.raw, orig) {
		t.Fatal("raw 被篡改")
	}
}

// TestInjectIncludeUsage_NonStream 非流式 → 不注入(usage 在响应体里天然有)。
func TestInjectIncludeUsage_NonStream(t *testing.T) {
	orig := []byte(`{"model":"x","stream":false,"messages":[]}`)
	snap := &bodySnapshot{raw: orig, decoded: orig}

	changed := injectIncludeUsage(snap, "")
	if changed {
		t.Fatal("非流式不应注入")
	}
}

// TestInjectIncludeUsage_NoStreamField 无 stream 字段(默认非流式) → 不注入。
func TestInjectIncludeUsage_NoStreamField(t *testing.T) {
	orig := []byte(`{"model":"x","messages":[]}`)
	snap := &bodySnapshot{raw: orig, decoded: orig}

	changed := injectIncludeUsage(snap, "")
	if changed {
		t.Fatal("无 stream 字段不应注入")
	}
}

// TestInjectIncludeUsage_AlreadyFalse 客户端带了 include_usage=false → 应改写为 true。
func TestInjectIncludeUsage_AlreadyFalse(t *testing.T) {
	orig := []byte(`{"model":"x","stream":true,"stream_options":{"include_usage":false},"messages":[]}`)
	snap := &bodySnapshot{raw: orig, decoded: orig}

	changed := injectIncludeUsage(snap, "")
	if !changed {
		t.Fatal("include_usage=false 应改写为 true")
	}
	var m map[string]any
	json.Unmarshal(snap.decoded, &m)
	so := m["stream_options"].(map[string]any)
	if so["include_usage"] != true {
		t.Fatalf("include_usage=%v, want true", so["include_usage"])
	}
}

// TestInjectIncludeUsage_Gzip gzip 编码 → 注入后 raw 应重新 gzip 压缩, 且解压后含 include_usage。
func TestInjectIncludeUsage_Gzip(t *testing.T) {
	// 原始 gzip body
	origJSON := []byte(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	gw.Write(origJSON)
	gw.Close()

	snap := &bodySnapshot{raw: gzBuf.Bytes(), decoded: origJSON}

	changed := injectIncludeUsage(snap, "gzip")
	if !changed {
		t.Fatal("gzip 流式应注入")
	}

	// raw 应是合法 gzip, 解压后含 include_usage
	gr, err := gzip.NewReader(bytes.NewReader(snap.raw))
	if err != nil {
		t.Fatalf("注入后 raw 非合法 gzip: %v", err)
	}
	decompressed := readAll(gr)
	gr.Close()

	var m map[string]any
	if err := json.Unmarshal(decompressed, &m); err != nil {
		t.Fatalf("解压后非法 JSON: %v", err)
	}
	so, ok := m["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Fatalf("gzip 解压后未含 include_usage=true: %s", decompressed)
	}

	// decoded 也应同步更新
	if !bytes.Contains(snap.decoded, []byte("include_usage")) {
		t.Fatal("decoded 未同步更新")
	}
}

// TestInjectIncludeUsage_InvalidJSON 非法 JSON → 静默不改, 不 panic。
func TestInjectIncludeUsage_InvalidJSON(t *testing.T) {
	orig := []byte(`{not valid json`)
	snap := &bodySnapshot{raw: orig, decoded: orig}

	changed := injectIncludeUsage(snap, "") // 不应 panic
	if changed {
		t.Fatal("非法 JSON 不应改写")
	}
	if !bytes.Equal(snap.raw, orig) {
		t.Fatal("raw 被篡改")
	}
}

func readAll(r interface{ Read([]byte) (int, error) }) []byte {
	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return out.Bytes()
}
