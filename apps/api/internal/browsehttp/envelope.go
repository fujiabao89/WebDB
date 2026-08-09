package browsehttp

import (
	"encoding/json"
	"net/http"

	"github.com/fujiabao89/webdb/internal/browse"
)

// 本文件提供 WEB-36 浏览路由的最小响应 envelope（P0-06A §5.3）。
// 这是 WEB-35 集成 seam：WEB-35 合并后可用其统一 envelope 替换，不构成竞争实现。

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeData 写成功 envelope：{ "data": ... }。浏览类路由 meta 可选省略（P0-06A §5.3）。
// 先序列化到内存并检查字节预算（F2 方案 A，P0-06A §6/§7 D06b：响应体上限 8 MiB）；
// 超过返回 422 result_too_large，不写出超限响应。
func writeData(w http.ResponseWriter, data any) {
	b, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		writeError(w, browse.ErrInternalError)
		return
	}
	if len(b) > browse.MaxResponseBytes {
		writeError(w, browse.ErrResultTooLarge)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// writeError 写统一错误 envelope：{ "error": { "code", "message" } }（固定安全摘要）。
// 429 附带可测试的 Retry-After（P0-06A §12 D15d）。
func writeError(w http.ResponseWriter, code browse.StableErrorCode) {
	status := statusFor(code)
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": string(code), "message": string(code)},
	})
}

// statusFor 映射稳定错误码到 HTTP 状态（P0-06A §12 Owner 已批准映射）。
func statusFor(code browse.StableErrorCode) int {
	switch code {
	case browse.ErrInvalidScope:
		return http.StatusBadRequest
	case browse.ErrUnauthorized:
		return http.StatusUnauthorized
	case browse.ErrForbidden, browse.ErrReadNotAllowed:
		return http.StatusForbidden
	case browse.ErrConnectionNotFound, browse.ErrPolicyNotConfigured:
		return http.StatusNotFound
	case browse.ErrResultTooLarge:
		return http.StatusUnprocessableEntity
	case browse.ErrRateLimited, browse.ErrConnectionBusy:
		return http.StatusTooManyRequests
	case browse.ErrConnectionUnavailable:
		return http.StatusServiceUnavailable
	case browse.ErrQueryTimeout:
		return http.StatusGatewayTimeout
	case browse.ErrQueryCancelled:
		return 499 // nginx 惯例，D15b 已批准
	default:
		return http.StatusInternalServerError
	}
}
