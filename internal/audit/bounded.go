package audit

import (
	"bytes"
	"sync"
)

// boundedWriter 有界写入器, 防 OOM(DESIGN.md §5.2)。
// 超过 max 字节后静默丢弃后续写入(不返回 error), 标记 truncated。
// 用于 sseCaptureLoop 的旁路累积: 捕获永不返 error, 不影响转发。
type boundedWriter struct {
	buf      bytes.Buffer
	max      int64
	truncated bool
	mu       sync.Mutex
}

// newBoundedWriter 构造。
func newBoundedWriter(max int64) *boundedWriter {
	return &boundedWriter{max: max}
}

// Write 尽力写入; 超限后静默丢弃, 不返 error。
func (b *boundedWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		return len(p), nil // 已截断, 静默丢弃
	}
	room := b.max - int64(b.buf.Len())
	if room <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) <= room {
		n, _ := b.buf.Write(p)
		return n, nil
	}
	// 部分写入
	b.buf.Write(p[:room])
	b.truncated = true
	return len(p), nil
}

// Bytes 返回已累积的内容。
func (b *boundedWriter) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Bytes()
}

// Truncated 返回是否触发了截断。
func (b *boundedWriter) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// Len 返回已写入字节数。
func (b *boundedWriter) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}
