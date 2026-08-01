package metadata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// 审计事件 action 常量（proposal §8.1 E1-E16）。
// E17（审计写入失败）不持久化到 audit_events，走独立安全告警通道。
const (
	ActionConnectionCreate  = "connection.create"
	ActionConnectionUpdate  = "connection.update"
	ActionCredentialCreate  = "credential.create"
	ActionCredentialRotate  = "credential.rotate"
	ActionCredentialRetire  = "credential.retire"
	ActionConnectionTest    = "connection.test"
	ActionSQLExecute        = "sql.execute"
	ActionCredentialLookup  = "credential.lookup"
	ActionCredentialDecrypt = "credential.decrypt"
)

// AuditMetadata 是审计 metadata 的强类型表示。
// 仅包含 proposal §8.2 批准的 16 字段 + credential.rotate 事件专用的
// expected_version/actual_version（proposal §8.1 E5 矩阵）。
// 禁止任意 map[string]any 或自由文本直接进入审计仓储；
// 所有字段经 Marshal → ValidateAuditEventMetadata 精确校验后持久化。
type AuditMetadata struct {
	SecretRef       *string `json:"secret_ref,omitempty"`       // UUID 格式
	SecretVersion   *int    `json:"secret_version,omitempty"`   // > 0
	OldVersion      *int    `json:"old_version,omitempty"`      // > 0
	NewVersion      *int    `json:"new_version,omitempty"`      // > old_version
	EnvelopeSuite   *string `json:"envelope_suite,omitempty"`   // 精确枚举 AES256GCM-v1
	KEKVersion      *int    `json:"kek_version,omitempty"`      // > 0
	StatementHash   *string `json:"statement_hash,omitempty"`   // 64 字符 hex
	RowCount        *int    `json:"row_count,omitempty"`        // 0..2^31-1
	DurationMs      *int    `json:"duration_ms,omitempty"`      // >= 0
	ErrorCode       *string `json:"error_code,omitempty"`       // 稳定错误码枚举
	ReasonCode      *string `json:"reason_code,omitempty"`      // 稳定拒绝原因码
	Engine          *string `json:"engine,omitempty"`           // postgresql | mysql
	Environment     *string `json:"environment,omitempty"`      // development | staging | production
	Summary         *string `json:"summary,omitempty"`          // P0-04 兼容，<=255
	RowsAffected    *int    `json:"rows_affected,omitempty"`    // P0-04 兼容
	Cached          *bool   `json:"cached,omitempty"`           // P0-04 兼容
	ExpectedVersion *int    `json:"expected_version,omitempty"` // credential.rotate 专用
	ActualVersion   *int    `json:"actual_version,omitempty"`   // credential.rotate 专用
}

// Marshal 序列化为严格 JSON 对象。所有字段 nil 时输出空对象。
func (m AuditMetadata) Marshal() (json.RawMessage, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("audit metadata marshal: %w", err)
	}
	return json.RawMessage(b), nil
}

// auditActionFields 定义每个事件 action 允许的 metadata 字段（proposal §8.1/§8.2）。
// 未知字段或不属于该事件的字段一律 fail-closed 拒绝。
var auditActionFields = map[string]map[string]bool{
	ActionConnectionCreate:  {"engine": true, "environment": true},
	ActionConnectionUpdate:  {"environment": true},
	ActionCredentialCreate:  {"secret_ref": true, "secret_version": true, "envelope_suite": true, "kek_version": true},
	ActionCredentialRotate:  {"secret_ref": true, "old_version": true, "new_version": true, "envelope_suite": true, "kek_version": true, "error_code": true, "expected_version": true, "actual_version": true},
	ActionCredentialRetire:  {"secret_ref": true, "secret_version": true},
	ActionConnectionTest:    {"engine": true, "environment": true, "duration_ms": true, "error_code": true},
	ActionSQLExecute:        {"statement_hash": true, "row_count": true, "duration_ms": true, "error_code": true, "reason_code": true, "engine": true, "environment": true},
	ActionCredentialLookup:  {"secret_ref": true, "error_code": true},
	ActionCredentialDecrypt: {"secret_ref": true, "secret_version": true, "kek_version": true, "error_code": true},
}

type auditFieldKind int

const (
	kindString auditFieldKind = iota
	kindInt
	kindBool
)

type auditFieldSpec struct {
	kind     auditFieldKind
	validate func(name string, v any) error
}

// auditFieldSpecs 定义每个允许字段的精确类型与格式约束。
var auditFieldSpecs = map[string]auditFieldSpec{
	"secret_ref":       {kind: kindString, validate: validateUUIDField},
	"secret_version":   {kind: kindInt, validate: validatePositiveIntField},
	"old_version":      {kind: kindInt, validate: validatePositiveIntField},
	"new_version":      {kind: kindInt, validate: validatePositiveIntField},
	"expected_version": {kind: kindInt, validate: validatePositiveIntField},
	"actual_version":   {kind: kindInt, validate: validatePositiveIntField},
	"envelope_suite":   {kind: kindString, validate: validateEnvelopeSuiteField},
	"kek_version":      {kind: kindInt, validate: validatePositiveIntField},
	"statement_hash":   {kind: kindString, validate: validateHex64Field},
	"row_count":        {kind: kindInt, validate: validateNonNegIntField},
	"rows_affected":    {kind: kindInt, validate: validateNonNegIntField},
	"duration_ms":      {kind: kindInt, validate: validateNonNegIntField},
	"error_code":       {kind: kindString, validate: validateStableErrorCodeField},
	"reason_code":      {kind: kindString, validate: validateStableReasonCodeField},
	"engine":           {kind: kindString, validate: validateEngineField},
	"environment":      {kind: kindString, validate: validateEnvironmentField},
	"summary":          {kind: kindString, validate: validateSummaryField},
	"cached":           {kind: kindBool},
}

// ValidateAuditEventMetadata 对审计 metadata 做事件级精确校验（fail-closed）。
// 畸形 JSON、未知字段、错误类型、超长值、格式错误或敏感内容一律拒绝。
func ValidateAuditEventMetadata(action string, raw json.RawMessage) error {
	allowed, ok := auditActionFields[action]
	if !ok {
		return fmt.Errorf("audit metadata: unknown action %q", action)
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" {
		return nil // 空 metadata 合法（audit_events.metadata 默认 '{}'）
	}

	// 1. 严格解析对象：拒绝畸形 JSON、重复键、尾随数据。
	m, err := parseStrictObject(raw)
	if err != nil {
		return fmt.Errorf("audit metadata: %w", err)
	}

	// 2. 未知字段 fail-closed。
	for k := range m {
		if !allowed[k] {
			return fmt.Errorf("audit metadata: field %q not allowed for action %s", k, action)
		}
	}

	// 3. 逐字段类型/格式校验。
	for k, rawVal := range m {
		if err := validateAuditField(k, rawVal); err != nil {
			return err
		}
	}

	// 4. 跨字段约束（new_version > old_version）。
	return validateAuditCrossField(m)
}

// parseStrictObject 使用 json.Decoder 逐 token 解析 JSON 对象，
// 检测重复键并拒绝畸形输入与尾随数据。
func parseStrictObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("expected JSON object")
	}

	m := make(map[string]json.RawMessage)
	seen := make(map[string]bool)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid key: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key")
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = true

		var rawVal json.RawMessage
		if err := dec.Decode(&rawVal); err != nil {
			return nil, fmt.Errorf("invalid value for %q: %w", key, err)
		}
		m[key] = rawVal
	}
	// 消费对象结束标记 '}'。
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("unexpected end of object: %w", err)
	}
	// 确认无尾随数据（第二个 JSON 值或垃圾字节）。
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after JSON object")
	}
	return m, nil
}

// validateAuditField 校验单个字段的类型与格式。
func validateAuditField(key string, raw json.RawMessage) error {
	spec, ok := auditFieldSpecs[key]
	if !ok {
		// 应被审计动作允许集挡住；这里兜底。
		return fmt.Errorf("audit metadata: unsupported field %q", key)
	}

	switch spec.kind {
	case kindString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("audit metadata: field %q must be a string", key)
		}
		if spec.validate != nil {
			return spec.validate(key, s)
		}
	case kindInt:
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			return fmt.Errorf("audit metadata: field %q must be an integer", key)
		}
		// 首字符必须是数字（拒绝字符串、布尔、对象、数组、null）。
		if trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9') {
			return fmt.Errorf("audit metadata: field %q must be an integer", key)
		}
		i, err := strconv.ParseInt(string(trimmed), 10, 64)
		if err != nil {
			return fmt.Errorf("audit metadata: field %q must be an integer", key)
		}
		if spec.validate != nil {
			return spec.validate(key, i)
		}
	case kindBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return fmt.Errorf("audit metadata: field %q must be a boolean", key)
		}
	}
	return nil
}

// validateAuditCrossField 校验跨字段约束。
func validateAuditCrossField(m map[string]json.RawMessage) error {
	oldRaw, hasOld := m["old_version"]
	newRaw, hasNew := m["new_version"]
	if !hasOld || !hasNew {
		return nil
	}
	var oldV, newV int
	if err := json.Unmarshal(oldRaw, &oldV); err != nil {
		return fmt.Errorf("audit metadata: old_version must be an integer")
	}
	if err := json.Unmarshal(newRaw, &newV); err != nil {
		return fmt.Errorf("audit metadata: new_version must be an integer")
	}
	if newV <= oldV {
		return fmt.Errorf("audit metadata: new_version (%d) must be > old_version (%d)", newV, oldV)
	}
	return nil
}

// ---- 字段校验器 -------------------------------------------------------------

func validateUUIDField(name string, v any) error {
	s, _ := v.(string)
	if !isValidUUID(s) {
		return fmt.Errorf("audit metadata: field %q must be a UUID", name)
	}
	return nil
}

func validatePositiveIntField(name string, v any) error {
	i, _ := v.(int64)
	if i <= 0 {
		return fmt.Errorf("audit metadata: field %q must be > 0", name)
	}
	return nil
}

func validateNonNegIntField(name string, v any) error {
	i, _ := v.(int64)
	if i < 0 {
		return fmt.Errorf("audit metadata: field %q must be >= 0", name)
	}
	if i > 1<<31-1 {
		return fmt.Errorf("audit metadata: field %q exceeds int32 range", name)
	}
	return nil
}

func validateEnvelopeSuiteField(name string, v any) error {
	s, _ := v.(string)
	if s != "AES256GCM-v1" {
		return fmt.Errorf("audit metadata: field %q must be AES256GCM-v1", name)
	}
	return nil
}

func validateHex64Field(name string, v any) error {
	s, _ := v.(string)
	if !isHex64(s) {
		return fmt.Errorf("audit metadata: field %q must be 64-char lowercase hex", name)
	}
	return nil
}

func validateStableErrorCodeField(name string, v any) error {
	s, _ := v.(string)
	if !isValidStableCode(s) {
		return fmt.Errorf("audit metadata: field %q must be a stable error code", name)
	}
	return nil
}

func validateStableReasonCodeField(name string, v any) error {
	s, _ := v.(string)
	if !isValidReasonCode(s) {
		return fmt.Errorf("audit metadata: field %q must be a stable reason code", name)
	}
	return nil
}

func validateEngineField(name string, v any) error {
	s, _ := v.(string)
	if s != "postgresql" && s != "mysql" {
		return fmt.Errorf("audit metadata: field %q must be postgresql or mysql", name)
	}
	return nil
}

func validateEnvironmentField(name string, v any) error {
	s, _ := v.(string)
	if s != "development" && s != "staging" && s != "production" {
		return fmt.Errorf("audit metadata: field %q must be development/staging/production", name)
	}
	return nil
}

func validateSummaryField(name string, v any) error {
	s, _ := v.(string)
	if len(s) > 255 {
		return fmt.Errorf("audit metadata: field %q exceeds 255 bytes", name)
	}
	return nil
}

// isValidUUID 校验标准 RFC 4122 UUID 字符串格式（8-4-4-4-12 hex）。
func isValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	hexDigits := func(r byte) bool {
		return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
	}
	for i := 0; i < 36; i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !hexDigits(s[i]) {
				return false
			}
		}
	}
	return true
}

// isValidReasonCode 校验稳定拒绝原因码（P0-04 枚举）。
func isValidReasonCode(s string) bool {
	switch s {
	case "allowed", "sql_parse_error", "multiple_statements", "statement_not_allowed",
		"unsupported_statement", "empty_sql", "executable_comment_detected":
		return true
	default:
		return false
	}
}
