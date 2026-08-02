// Package credentials 提供凭证 Payload 编解码、信封加密、KEK 管理和生命周期操作。
// 全部使用 Go 标准库，不引入第三方加密依赖。
package credentials

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// ---- 域名错误 ----------------------------------------------------------------

// ErrorCode 凭证操作稳定错误码。
type ErrorCode string

func (e ErrorCode) Error() string { return string(e) }

const (
	ErrInvalidPayload  ErrorCode = "invalid_payload"
	ErrPayloadTooLarge ErrorCode = "payload_too_large"
)

// ---- 数据模型 ----------------------------------------------------------------

// CredentialPayload 数据库凭证明文（仅存在于内存）。
// Password 字段带有 json:"-" tag，防止 json.Marshal 意外泄露。
// EncodePayload 使用独立的 rawPayload 结构进行序列化。
type CredentialPayload struct {
	User     string `json:"user"`
	Password string `json:"-"`
}

// String 防止 %v/%+v 泄露明文密码。
func (p CredentialPayload) String() string {
	return fmt.Sprintf("CredentialPayload{User:%q, Password:[REDACTED]}", p.User)
}

// rawPayload 用于严格解码的中间结构。
type rawPayload struct {
	V        *int    `json:"v"`
	User     *string `json:"user"`
	Password *string `json:"password"`
}

const (
	// maxPayloadBytes Payload 最大字节数。
	maxPayloadBytes = 4096
	// maxUserBytes user 字段最大 UTF-8 字节数（proposal §3.2，PAY-06）。
	maxUserBytes = 255
	// maxPasswordBytes password 字段最大 UTF-8 字节数（proposal §3.2，PAY-07）。
	maxPasswordBytes = 1024
)

// ---- 编码 -------------------------------------------------------------------

// EncodePayload 将 CredentialPayload 编码为 JSON（UTF-8）。
func EncodePayload(p CredentialPayload) ([]byte, error) {
	if err := validatePayloadFields(p); err != nil {
		return nil, err
	}

	v := 1
	raw := rawPayload{V: &v, User: &p.User, Password: &p.Password}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(raw); err != nil {
		return nil, fmt.Errorf("%w: encode failed", ErrInvalidPayload)
	}

	encoded := buf.Bytes()

	// json.Encoder 追加 \n
	if len(encoded) > 0 && encoded[len(encoded)-1] == '\n' {
		encoded = encoded[:len(encoded)-1]
	}

	if len(encoded) > maxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}

	out := make([]byte, len(encoded))
	copy(out, encoded)
	return out, nil
}

// ---- 解码 -------------------------------------------------------------------

// DecodePayload 从 JSON 字节解码 CredentialPayload。
// 严格校验：仅允许 v=1, user, password 三个字段。
func DecodePayload(data []byte) (CredentialPayload, error) {
	if len(data) > maxPayloadBytes {
		return CredentialPayload{}, ErrPayloadTooLarge
	}

	if !utf8.Valid(data) {
		return CredentialPayload{}, fmt.Errorf("%w: invalid UTF-8", ErrInvalidPayload)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var raw rawPayload
	if err := decoder.Decode(&raw); err != nil {
		return CredentialPayload{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}

	// 严格校验：拒绝尾随数据（第二个 JSON 值或垃圾字节）
	if _, err := decoder.Token(); err != io.EOF {
		return CredentialPayload{}, fmt.Errorf("%w: trailing data after JSON object", ErrInvalidPayload)
	}

	if err := checkDuplicateKeys(data); err != nil {
		return CredentialPayload{}, err
	}

	if raw.V == nil {
		return CredentialPayload{}, fmt.Errorf("%w: missing field v", ErrInvalidPayload)
	}
	if *raw.V != 1 {
		return CredentialPayload{}, fmt.Errorf("%w: v must be 1", ErrInvalidPayload)
	}

	if raw.User == nil || *raw.User == "" {
		return CredentialPayload{}, fmt.Errorf("%w: user is required", ErrInvalidPayload)
	}

	if raw.Password == nil || *raw.Password == "" {
		return CredentialPayload{}, fmt.Errorf("%w: password is required", ErrInvalidPayload)
	}

	p := CredentialPayload{User: *raw.User, Password: *raw.Password}
	if err := validatePayloadFields(p); err != nil {
		return CredentialPayload{}, err
	}

	return p, nil
}

// ---- 校验 -------------------------------------------------------------------

func validatePayloadFields(p CredentialPayload) error {
	if p.User == "" {
		return fmt.Errorf("%w: user is required", ErrInvalidPayload)
	}
	if p.Password == "" {
		return fmt.Errorf("%w: password is required", ErrInvalidPayload)
	}

	// per-field 长度限制按 UTF-8 字节数（proposal §3.2，PAY-06/PAY-07）。
	if len([]byte(p.User)) > maxUserBytes {
		return fmt.Errorf("%w: user exceeds %d bytes", ErrInvalidPayload, maxUserBytes)
	}
	if len([]byte(p.Password)) > maxPasswordBytes {
		return fmt.Errorf("%w: password exceeds %d bytes", ErrInvalidPayload, maxPasswordBytes)
	}

	for _, r := range p.User {
		if r <= 0x1f && r != '\t' {
			return fmt.Errorf("%w: user contains control character", ErrInvalidPayload)
		}
	}

	return nil
}

// ---- 重复字段检测 -----------------------------------------------------------

func checkDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))

	t, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if t != json.Delim('{') {
		return fmt.Errorf("%w: expected JSON object", ErrInvalidPayload)
	}

	seen := make(map[string]bool)

	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("%w: expected string key", ErrInvalidPayload)
		}

		if seen[key] {
			return fmt.Errorf("%w: duplicate key %q", ErrInvalidPayload, key)
		}
		seen[key] = true

		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
	}

	return nil
}
