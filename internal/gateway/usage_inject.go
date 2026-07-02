package gateway

import (
	"bytes"
	"compress/gzip"
	"encoding/json"

	"github.com/andybalholm/brotli"
)

// injectIncludeUsage 在流式 chat 请求体里注入 stream_options.include_usage=true,
// 使上游在最后一帧下发 usage(prompt_tokens/completion_tokens), 否则流式默认无 usage。
//
// 触发条件(全部满足才注入):
//   - JSON 合法
//   - stream == true
//   - stream_options.include_usage != true(幂等: 已是 true 则不改)
//
// 注入后同步更新 snap.raw(按原编码重新压缩)与 snap.decoded(明文)。
// 任何环节失败(JSON 非法、重压缩失败)均静默放弃, 保留原 body, 绝不阻断请求。
//
// 返回 true 表示 body 被改写(调用方需注意 restoreBody 已用新 raw)。
func injectIncludeUsage(snap *bodySnapshot, encoding string) bool {
	if snap == nil || len(snap.decoded) == 0 {
		return false
	}

	// 解析为通用 map, 保留所有原字段
	var body map[string]any
	if err := json.Unmarshal(snap.decoded, &body); err != nil {
		return false // 非 JSON 或非法, 静默放弃
	}

	// 仅流式请求需要注入
	stream, _ := body["stream"].(bool)
	if !stream {
		return false
	}

	// 幂等: stream_options.include_usage 已是 true 则不改
	if so, ok := body["stream_options"].(map[string]any); ok {
		if v, _ := so["include_usage"].(bool); v {
			return false
		}
	}

	// 注入
	so, ok := body["stream_options"].(map[string]any)
	if !ok {
		so = make(map[string]any)
	}
	so["include_usage"] = true
	body["stream_options"] = so

	// 序列化新 body
	newJSON, err := json.Marshal(body)
	if err != nil {
		return false
	}

	// 按原编码重新生成 raw
	newRaw, err := reencode(newJSON, normalizeEncoding(encoding))
	if err != nil {
		return false // 重压缩失败, 放弃(保留原 body)
	}

	snap.decoded = newJSON
	snap.raw = newRaw
	return true
}

// reencode 按指定编码把明文 JSON 重新编码(gzip/br/identity)。
func reencode(data []byte, encoding string) ([]byte, error) {
	switch encoding {
	case "gzip":
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, err := gw.Write(data); err != nil {
			return nil, err
		}
		if err := gw.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case "br":
		var buf bytes.Buffer
		bw := brotli.NewWriter(&buf)
		if _, err := bw.Write(data); err != nil {
			return nil, err
		}
		if err := bw.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	default: // identity
		return data, nil
	}
}
