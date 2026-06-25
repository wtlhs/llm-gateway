package gateway

import (
	"net/http"
)

// realtimePassthrough Phase 1 对 /v1/realtime(WebSocket)仅做 HTTP 层透传占位。
//
// 说明(DESIGN.md §0 / K4): WebSocket 需 Hijacker + 双向 dial, 且会话可能小时级,
// schema(prompt/completion 二分)不适用。Phase 1 不实现捕获, 仅 stub。
// Phase 2 实现: gorilla/websocket 双向 tee + events JSONB 子结构。
//
// Phase 1 行为: 直接返回 501, 提示未实现(避免静默吞请求)。
// 真正的 WS 握手 Upgrade 在 Phase 2 加入。
func realtimePassthrough(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "realtime capture not implemented in Phase 1 (see DESIGN.md K4)", http.StatusNotImplemented)
}
