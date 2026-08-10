package executionhttp

import (
	"encoding/json"
	"net/http"
)

// MaxResponseBytes 成功响应体字节上限（P0-06A §13.2 D06a：8 MiB）。
const MaxResponseBytes = 8 << 20

// writeJSON 写 JSON 响应并设置 Content-Type。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeData 写成功 envelope：{ "data": ... }。
// 先序列化到内存并检查 8 MiB 字节预算；超限返回 result_too_large，
// 不写出超限响应、不静默截断（P0-06A §6/§7 D06b）。序列化失败视为内部错误。
func writeData(w http.ResponseWriter, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		writeError(w, ErrInternalError)
		return
	}
	if len(b) > MaxResponseBytes {
		writeError(w, ErrResultTooLarge)
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(b))
}

// writeDataEnvelope 写统一成功 envelope { "data": ..., "meta": ... }（P0-06A §5.3）。
// 数据与 meta 一起序列化并检查字节预算。
func writeDataEnvelope(w http.ResponseWriter, data, meta any) {
	body := map[string]any{"data": data}
	if meta != nil {
		body["meta"] = meta
	}
	b, err := json.Marshal(body)
	if err != nil {
		writeError(w, ErrInternalError)
		return
	}
	if len(b) > MaxResponseBytes {
		writeError(w, ErrResultTooLarge)
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(b))
}

// writeError 写统一错误 envelope：{ "error": { "code", "message" } }。
// message 固定为 code（安全摘要，不泄露根因）。429 附带确定性的 Retry-After。
func writeError(w http.ResponseWriter, code StableErrorCode) {
	status := statusFor(code)
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": string(code), "message": string(code)},
	})
}
