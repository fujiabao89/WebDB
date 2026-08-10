package executionhttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
)

// MaxResponseBytes 成功响应体字节上限（P0-06A §13.2 D06a：8 MiB）。
const MaxResponseBytes = 8 << 20

// writeJSON 写 JSON 响应并设置 Content-Type。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeDataEnvelope 写统一成功 envelope { "data": ..., "meta": ... }（P0-06A §5.3）。
// 先以 Encoder 完整序列化到内存（含 JSON 转义、Base64 膨胀、末尾换行与 page/audit
// metadata），并按最终实际写出字节数检查 8 MiB 上限（D06a，CodeRabbit #13）：
// 超限返回 result_too_large，不写出超限响应、不静默截断、不产生半个 200。
func writeDataEnvelope(w http.ResponseWriter, data, meta any) {
	body := map[string]any{"data": data}
	if meta != nil {
		body["meta"] = meta
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		writeError(w, ErrInternalError)
		return
	}
	if buf.Len() > MaxResponseBytes {
		writeError(w, ErrResultTooLarge)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// writeError 写统一错误 envelope：{ "error": { "code", "message" } }。
// message 固定为 code（安全摘要，不泄露根因）。429 附带确定性的 Retry-After
// （接入 retryAfterSeconds 常量，CodeRabbit #11）。
func writeError(w http.ResponseWriter, code StableErrorCode) {
	status := statusFor(code)
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": string(code), "message": string(code)},
	})
}
