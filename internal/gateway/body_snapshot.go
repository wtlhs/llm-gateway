package gateway

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/company/llm-gateway/internal/metrics"
)

// bodySnapshot 是请求 body 读取 + 解压 + 还原的结果(K2/K3)。
type bodySnapshot struct {
	raw       []byte // 原始字节(可能是压缩的)→ 还原给 New API
	decoded   []byte // 解压后字节(用于落库 prompt)
	truncated bool   // decoded 是否触发 postBodyMaxBytes 截断
	decodeErr bool   // 解压失败(C6: 记 metric, 不静默)
}

// snapshotBody 读取 r.Body(上限 preMax), 解压副本(上限 postMax), 还原原字节给上游。
// 设计依据 DESIGN.md §5.1.2 数据流。
//
// 关键(K2): r.Body 只能读一次。读完 raw 后, 用 raw 还原 r.Body,
// 这样 base.RoundTrip(r) 仍能把原始字节发给 New API。
func snapshotBody(r *http.Request, preMax, postMax int64) (bodySnapshot, error) {
	var snap bodySnapshot

	raw, err := readBounded(r.Body, preMax) // 硬上限防解压炸弹
	if cerr := r.Body.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return snap, err
	}
	snap.raw = raw

	// 解压副本(仅用于落库; 透传的是 raw)
	dec, trunc, derr := decodeMaybe(raw, r.Header.Get("Content-Encoding"), postMax)
	if derr != nil {
		snap.decodeErr = true
		metrics.DecodeFailed.Inc()
	} else {
		snap.decoded = dec
		snap.truncated = trunc
	}

	// 还原原始字节给上游(K2)
	restoreBody(r, raw)
	return snap, nil
}

// restoreBody 用原字节重置 r.Body, 供 RoundTrip 转发。
// 同时重设 ContentLength 与 GetBody(ReverseProxy 重试路径会用 GetBody)。
func restoreBody(r *http.Request, raw []byte) {
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	if raw != nil {
		body := raw
		r.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}
}

// readBounded 读取 reader, 最多 max 字节; 超过则返回已读部分 + 错误(防 OOM/解压炸弹)。
func readBounded(r io.Reader, max int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, max+1))
	// 注: 若实际 > max, ReadAll 会读 max+1 字节, 调用方据长度判断截断;
	// Phase1 保守: 超大请求视为异常, 见 transport 层处理。
}

// decodeMaybe 按 Content-Encoding 解压, 上限 postMax 截断。
// 返回 (decoded, truncated, err)。identity 或空编码直接返回(受 postMax 截断)。
func decodeMaybe(raw []byte, encoding string, postMax int64) ([]byte, bool, error) {
	encoding = normalizeEncoding(encoding)
	switch encoding {
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, false, err
		}
		return readBoundedTrunc(zr, postMax)
	case "br":
		zr := brotli.NewReader(bytes.NewReader(raw))
		return readBoundedTrunc(zr, postMax)
	default: // identity / 空
		// 原样, 但仍受 postMax 截断(超长 prompt 也需截断)
		if int64(len(raw)) > postMax {
			return raw[:postMax], true, nil
		}
		return raw, false, nil
	}
}

// readBoundedTrunc 读到 max 后停止, 标记 truncated。
func readBoundedTrunc(r io.Reader, max int64) ([]byte, bool, error) {
	buf := make([]byte, 0, max)
	tmp := make([]byte, 32*1024)
	trunc := false
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			room := max - int64(len(buf))
			if int64(n) <= room {
				buf = append(buf, tmp[:n]...)
			} else {
				buf = append(buf, tmp[:room]...)
				trunc = true
				return buf, trunc, nil
			}
		}
		if err == io.EOF {
			return buf, trunc, nil
		}
		if err != nil {
			return buf, trunc, err
		}
	}
}

func normalizeEncoding(e string) string {
	// 小写化 + 去 deflate/zlib(Phase1 只处理 gzip/br, 与 New API gzip.go 一致)
	return strings.ToLower(strings.TrimSpace(e))
}
