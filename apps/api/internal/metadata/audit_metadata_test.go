package metadata

import (
	"encoding/json"
	"testing"
)

// TestAuditMetadataValidate_AllowedFields 验证每个规定事件的合法 metadata 通过 fail-closed 校验。
func TestAuditMetadataValidate_AllowedFields(t *testing.T) {
	uuidStr := "123e4567-e89b-42d3-a456-426614174000"
	hash := hex64("sha256placeholder00000000000000000000000000000000")[:64]
	_ = hash

	tests := []struct {
		name    string
		action  string
		outcome AuditOutcome
		md      AuditMetadata
	}{
		{name: "E1 connection.create", action: ActionConnectionCreate, outcome: OutcomeSucceeded, md: AuditMetadata{Engine: strPtr("postgresql"), Environment: strPtr("development")}},
		{name: "E2 connection.update", action: ActionConnectionUpdate, outcome: OutcomeSucceeded, md: AuditMetadata{Environment: strPtr("production")}},
		{name: "E3 credential.create", action: ActionCredentialCreate, outcome: OutcomeSucceeded, md: AuditMetadata{SecretRef: strPtr(uuidStr), SecretVersion: intPtr(1), EnvelopeSuite: strPtr("AES256GCM-v1"), KEKVersion: intPtr(2)}},
		{name: "E4 credential.rotate succeeded", action: ActionCredentialRotate, outcome: OutcomeSucceeded, md: AuditMetadata{SecretRef: strPtr(uuidStr), OldVersion: intPtr(1), NewVersion: intPtr(2), EnvelopeSuite: strPtr("AES256GCM-v1"), KEKVersion: intPtr(2)}},
		{name: "E5 credential.rotate failed", action: ActionCredentialRotate, outcome: OutcomeFailed, md: AuditMetadata{SecretRef: strPtr(uuidStr), ErrorCode: strPtr("version_conflict"), ExpectedVersion: intPtr(2), ActualVersion: intPtr(3)}},
		{name: "E6 credential.retire", action: ActionCredentialRetire, outcome: OutcomeSucceeded, md: AuditMetadata{SecretRef: strPtr(uuidStr), SecretVersion: intPtr(1)}},
		{name: "E7 connection.test succeeded", action: ActionConnectionTest, outcome: OutcomeSucceeded, md: AuditMetadata{Engine: strPtr("mysql"), Environment: strPtr("staging"), DurationMs: intPtr(42)}},
		{name: "E8 connection.test failed", action: ActionConnectionTest, outcome: OutcomeFailed, md: AuditMetadata{Engine: strPtr("postgresql"), Environment: strPtr("development"), ErrorCode: strPtr("connection_failed")}},
		{name: "E9 sql.execute denied", action: ActionSQLExecute, outcome: OutcomeDenied, md: AuditMetadata{StatementHash: strPtr(hash), ReasonCode: strPtr("statement_not_allowed"), Engine: strPtr("postgresql")}},
		{name: "E10 sql.execute succeeded", action: ActionSQLExecute, outcome: OutcomeSucceeded, md: AuditMetadata{StatementHash: strPtr(hash), RowCount: intPtr(10), DurationMs: intPtr(5), Engine: strPtr("mysql"), Environment: strPtr("production")}},
		{name: "E11 sql.execute failed", action: ActionSQLExecute, outcome: OutcomeFailed, md: AuditMetadata{StatementHash: strPtr(hash), ErrorCode: strPtr("database_error"), Engine: strPtr("postgresql"), Environment: strPtr("development")}},
		{name: "E12 sql.execute timeout", action: ActionSQLExecute, outcome: OutcomeFailed, md: AuditMetadata{StatementHash: strPtr(hash), ErrorCode: strPtr("query_timeout"), Engine: strPtr("postgresql")}},
		{name: "E13 sql.execute cancelled", action: ActionSQLExecute, outcome: OutcomeCancelled, md: AuditMetadata{StatementHash: strPtr(hash), ErrorCode: strPtr("query_cancelled"), Engine: strPtr("mysql")}},
		{name: "E14 credential.lookup failed", action: ActionCredentialLookup, outcome: OutcomeFailed, md: AuditMetadata{SecretRef: strPtr(uuidStr), ErrorCode: strPtr("credential_not_found")}},
		{name: "E15 credential.decrypt failed", action: ActionCredentialDecrypt, outcome: OutcomeFailed, md: AuditMetadata{SecretRef: strPtr(uuidStr), SecretVersion: intPtr(1), ErrorCode: strPtr("decryption_failed")}},
		{name: "E16 unknown KEK version", action: ActionCredentialDecrypt, outcome: OutcomeFailed, md: AuditMetadata{SecretRef: strPtr(uuidStr), SecretVersion: intPtr(1), KEKVersion: intPtr(9), ErrorCode: strPtr("unknown_kek_version")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := tt.md.Marshal()
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if err := ValidateAuditEventMetadata(tt.action, tt.outcome, raw); err != nil {
				t.Fatalf("ValidateAuditEventMetadata(%q) error = %v", tt.action, err)
			}
		})
	}
}

// TestAuditMetadataValidate_UnknownFields 验证未知字段 fail-closed 拒绝。
func TestAuditMetadataValidate_UnknownFields(t *testing.T) {
	raw := json.RawMessage(`{"summary":"ok","unknown_field":"x"}`)
	if err := ValidateAuditEventMetadata(ActionSQLExecute, OutcomeFailed, raw); err == nil {
		t.Fatal("unknown field should be rejected fail-closed")
	}
}

// TestAuditMetadataValidate_DisallowedFieldForEvent 验证字段不在事件允许集内时拒绝。
func TestAuditMetadataValidate_DisallowedFieldForEvent(t *testing.T) {
	// connection.create 不允许 secret_ref
	raw := json.RawMessage(`{"engine":"postgresql","environment":"development","secret_ref":"123e4567-e89b-42d3-a456-426614174000"}`)
	if err := ValidateAuditEventMetadata(ActionConnectionCreate, OutcomeSucceeded, raw); err == nil {
		t.Fatal("secret_ref on connection.create should be rejected")
	}
}

// TestAuditMetadataValidate_TypeErrors 验证类型错误 fail-closed 拒绝。
func TestAuditMetadataValidate_TypeErrors(t *testing.T) {
	tests := []struct {
		name   string
		action string
		raw    string
	}{
		{name: "engine as number", action: ActionConnectionCreate, raw: `{"engine":1,"environment":"development"}`},
		{name: "secret_version as string", action: ActionCredentialCreate, raw: `{"secret_ref":"123e4567-e89b-42d3-a456-426614174000","secret_version":"1","envelope_suite":"AES256GCM-v1","kek_version":1}`},
		{name: "duration_ms as string", action: ActionConnectionTest, raw: `{"engine":"postgresql","environment":"development","duration_ms":"42"}`},
		{name: "error_code as number", action: ActionCredentialLookup, raw: `{"secret_ref":"123e4567-e89b-42d3-a456-426614174000","error_code":1}`},
		{name: "cached as string", action: ActionSQLExecute, raw: `{"statement_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","cached":"true"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateAuditEventMetadata(tt.action, OutcomeFailed, json.RawMessage(tt.raw)); err == nil {
				t.Fatalf("type error should be rejected: %s", tt.raw)
			}
		})
	}
}

// TestAuditMetadataValidate_Malformed 验证畸形 JSON fail-closed 拒绝。
func TestAuditMetadataValidate_Malformed(t *testing.T) {
	tests := []struct {
		name   string
		action string
		raw    string
	}{
		{name: "invalid json", action: ActionSQLExecute, raw: `{not json`},
		{name: "null", action: ActionSQLExecute, raw: `null`},
		{name: "array", action: ActionSQLExecute, raw: `[1,2]`},
		{name: "string", action: ActionSQLExecute, raw: `"hello"`},
		{name: "trailing data", action: ActionSQLExecute, raw: `{"a":1} garbage`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateAuditEventMetadata(tt.action, OutcomeFailed, json.RawMessage(tt.raw)); err == nil {
				t.Fatalf("malformed metadata should be rejected: %q", tt.raw)
			}
		})
	}
}

// TestAuditMetadataValidate_NestedObject 验证嵌套对象 fail-closed 拒绝。
func TestAuditMetadataValidate_NestedObject(t *testing.T) {
	raw := json.RawMessage(`{"statement_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","nested":{"a":1}}`)
	if err := ValidateAuditEventMetadata(ActionSQLExecute, OutcomeFailed, raw); err == nil {
		t.Fatal("nested object should be rejected fail-closed")
	}
}

// TestAuditMetadataValidate_TooLong 验证超长值 fail-closed 拒绝。
func TestAuditMetadataValidate_TooLong(t *testing.T) {
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	raw, _ := json.Marshal(map[string]any{"summary": string(long)})
	if err := ValidateAuditEventMetadata(ActionSQLExecute, OutcomeFailed, raw); err == nil {
		t.Fatal("overlong summary should be rejected")
	}
}

// TestAuditMetadataValidate_InvalidFormats 验证格式错误拒绝（UUID、hex、枚举）。
func TestAuditMetadataValidate_InvalidFormats(t *testing.T) {
	tests := []struct {
		name   string
		action string
		raw    string
	}{
		{name: "secret_ref not uuid", action: ActionCredentialCreate, raw: `{"secret_ref":"not-a-uuid","secret_version":1,"envelope_suite":"AES256GCM-v1","kek_version":1}`},
		{name: "statement_hash not 64 hex", action: ActionSQLExecute, raw: `{"statement_hash":"short","engine":"postgresql"}`},
		{name: "engine invalid enum", action: ActionConnectionCreate, raw: `{"engine":"oracle","environment":"development"}`},
		{name: "environment invalid enum", action: ActionConnectionCreate, raw: `{"engine":"postgresql","environment":"prod"}`},
		{name: "envelope_suite unknown", action: ActionCredentialCreate, raw: `{"secret_ref":"123e4567-e89b-42d3-a456-426614174000","secret_version":1,"envelope_suite":"AES-256-XYZ","kek_version":1}`},
		{name: "secret_version not positive", action: ActionCredentialCreate, raw: `{"secret_ref":"123e4567-e89b-42d3-a456-426614174000","secret_version":0,"envelope_suite":"AES256GCM-v1","kek_version":1}`},
		{name: "row_count negative", action: ActionSQLExecute, raw: `{"statement_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","row_count":-1}`},
		{name: "new_version not > old_version", action: ActionCredentialRotate, raw: `{"secret_ref":"123e4567-e89b-42d3-a456-426614174000","old_version":2,"new_version":2}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateAuditEventMetadata(tt.action, OutcomeFailed, json.RawMessage(tt.raw)); err == nil {
				t.Fatalf("invalid format should be rejected: %s", tt.raw)
			}
		})
	}
}

// TestAuditMetadataValidate_Canary 验证 SQL 正文、密码、KEK、DEK、连接串、结果和原始错误 cannot 进入审计 metadata。
func TestAuditMetadataValidate_Canary(t *testing.T) {
	canaries := []struct {
		action string
		key    string
		value  string
	}{
		{ActionSQLExecute, "summary", "SELECT * FROM users WHERE password = 'hunter2'"},
		{ActionCredentialCreate, "summary", "password=sup3rsecret"},
		{ActionConnectionCreate, "summary", "postgres://user:pass@host:5432/db"},
		{ActionSQLExecute, "summary", "pq: password authentication failed"},
		{ActionCredentialCreate, "summary", "kek=WnVqLWNvbmZpZzEyMzQ1Ng=="},
		{ActionCredentialCreate, "summary", "wrap_dek=aGVsbG93b3JsZDEyMzQ1Ng=="},
	}
	for _, c := range canaries {
		raw, _ := json.Marshal(map[string]any{c.key: c.value})
		if err := ValidateAuditEventMetadata(c.action, OutcomeFailed, raw); err == nil {
			t.Fatalf("canary %q should be rejected for action %s", c.value, c.action)
		}
	}
}

// TestAuditMetadataValidate_MissingRequiredField 验证缺失必填字段与互斥字段拒绝（Codex P1）。
func TestAuditMetadataValidate_MissingRequiredField(t *testing.T) {
	hash := hex64("sha256placeholder00000000000000000000000000000000")[:64]

	tests := []struct {
		name    string
		action  string
		outcome AuditOutcome
		raw     string
	}{
		{name: "E10 missing environment", action: ActionSQLExecute, outcome: OutcomeSucceeded, raw: `{"statement_hash":"` + hash + `","engine":"postgresql"}`},
		{name: "E9 missing reason_code", action: ActionSQLExecute, outcome: OutcomeDenied, raw: `{"statement_hash":"` + hash + `","engine":"postgresql"}`},
		{name: "E3 empty metadata", action: ActionCredentialCreate, outcome: OutcomeSucceeded, raw: `{}`},
		{name: "E1 empty metadata", action: ActionConnectionCreate, outcome: OutcomeSucceeded, raw: `{}`},
		{name: "succeeded with error_code (exclusive)", action: ActionSQLExecute, outcome: OutcomeSucceeded, raw: `{"statement_hash":"` + hash + `","engine":"postgresql","environment":"development","error_code":"database_error"}`},
		{name: "denied with error_code (exclusive)", action: ActionSQLExecute, outcome: OutcomeDenied, raw: `{"statement_hash":"` + hash + `","reason_code":"statement_not_allowed","engine":"postgresql","error_code":"database_error"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateAuditEventMetadata(tt.action, tt.outcome, json.RawMessage(tt.raw)); err == nil {
				t.Fatalf("expected rejection for: %s", tt.raw)
			}
		})
	}
}

// TestAuditMetadataValidate_UnknownAction 验证未知事件类型 fail-closed 拒绝。
func TestAuditMetadataValidate_UnknownAction(t *testing.T) {
	raw := json.RawMessage(`{}`)
	if err := ValidateAuditEventMetadata("connection.delete", OutcomeFailed, raw); err == nil {
		t.Fatal("unknown action should be rejected")
	}
}

// ---- helpers ---------------------------------------------------------------

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// hex64 生成 64 字符小写 hex 测试值（P0 测试夹具：非真实密钥）。
func hex64(seed string) string {
	if len(seed) >= 64 {
		return seed[:64]
	}
	hexChars := "0123456789abcdef"
	out := make([]byte, 64)
	for i := range out {
		out[i] = hexChars[(i+len(seed))%len(hexChars)]
	}
	return string(out)
}
