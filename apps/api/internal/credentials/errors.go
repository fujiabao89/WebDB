package credentials

import (
	"errors"
	"fmt"
)

// KEKVersionError 携带未知 KEK 版本号，用于 E16 审计记录 kek_version（Codex P1）。
type KEKVersionError struct {
	Version int
	err     error
}

func (e *KEKVersionError) Error() string {
	return fmt.Sprintf("unknown kek version %d", e.Version)
}

func (e *KEKVersionError) Unwrap() error { return e.err }

// ErrorCode 凭证操作稳定错误码。
// 在 payload.go 中已定义 ErrInvalidPayload, ErrPayloadTooLarge。
// 此处定义加密相关错误码。

const (
	ErrDecryptionFailed   ErrorCode = "decryption_failed"
	ErrUnknownSuite       ErrorCode = "unknown_envelope_suite"
	ErrUnknownKEKVersion  ErrorCode = "unknown_kek_version"
	ErrInternalError      ErrorCode = "internal_error"
	ErrInvalidKEK         ErrorCode = "invalid_kek"
	ErrCredentialNotFound ErrorCode = "credential_not_found"
	ErrCredentialRetired  ErrorCode = "credential_retired"
	ErrVersionConflict    ErrorCode = "version_conflict"
	ErrCredentialInUse    ErrorCode = "credential_in_use"
	ErrWrapQuotaExhausted ErrorCode = "wrap_quota_exhausted"
)

// IsErrorCode 检查错误链中是否包含特定的 ErrorCode。
func IsErrorCode(err error, code ErrorCode) bool {
	var ec ErrorCode
	if errors.As(err, &ec) {
		return ec == code
	}
	return false
}
