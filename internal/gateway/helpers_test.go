package gateway

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
)

// newReqWithBody 构造带 body 的 POST 请求(测试辅助)。
func newReqWithBody(body []byte) *http.Request {
	r := httptest.NewRequest("POST", "/v1/chat/completions", io.NopCloser(bytesReader(body)))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func bytesReader(b []byte) io.Reader {
	return &byteReader{b: b}
}

type byteReader struct{ b []byte; i int }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// newGzipWriter 返回写入 buf 的 gzip writer(测试辅助)。
func newGzipWriter(buf *bytes.Buffer) *gzip.Writer {
	return gzip.NewWriter(buf)
}
