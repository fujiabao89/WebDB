package executionhttp

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

// maxRequestBytes 请求体硬上限（P0-06A §5.3/§13.2 D06b：256 KiB）。
const maxRequestBytes = 256 << 10

// decodeJSONBody 解码单个 JSON 请求体并执行传输层校验（P0-06A §5.3）。
//
// 校验（任一失败返回 invalid_request，不继续解析）：
//   - POST 请求必须携带 application/json Content-Type（非法/缺失拒绝）
//   - 请求体字节上限 256 KiB（http.MaxBytesReader 强制）
//   - 非空 body
//   - 单值 JSON（body 中只能有一个 JSON 值，多余内容拒绝）
//
// 未知字段与浏览器不得控制的字段（engine/workspace 覆盖/actor/role/trace 等）
// 一律忽略（encoding/json 默认行为），服务端只派生可信值（P0-06A §3/§5.2）。
// 返回 ok=false 时响应已写出稳定错误 envelope。
func decodeJSONBody(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) (bool, error) {
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		ct := r.Header.Get("Content-Type")
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || mt != "application/json" {
			writeError(w, ErrInvalidRequest)
			return false, ErrInvalidRequest
		}
	}
	body := http.MaxBytesReader(w, r.Body, maxBytes)
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		writeError(w, ErrInvalidRequest)
		return false, ErrInvalidRequest
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		writeError(w, ErrInvalidRequest)
		return false, ErrInvalidRequest
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		writeError(w, ErrInvalidRequest)
		return false, ErrInvalidRequest
	}
	// 单值：解码后 body 不得再有第二个 JSON 值。
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, ErrInvalidRequest)
		return false, ErrInvalidRequest
	}
	return true, nil
}

// codef 构造带稳定错误码的错误（供 handler 内部使用）。
func codef(code StableErrorCode, format string, a ...any) error {
	return fmt.Errorf("%s: %s", code, fmt.Sprintf(format, a...))
}
