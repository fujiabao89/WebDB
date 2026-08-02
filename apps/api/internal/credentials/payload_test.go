package credentials

import (
	"strings"
	"testing"
)

// ---- 正常 round-trip ----

func TestPayload_RoundTrip(t *testing.T) {
	original := CredentialPayload{User: "dbuser", Password: "s3cret!@#"}
	encoded, err := EncodePayload(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodePayload(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.User != original.User {
		t.Errorf("user: got %q, want %q", decoded.User, original.User)
	}
	if decoded.Password != original.Password {
		t.Errorf("password: got %q, want %q", decoded.Password, original.Password)
	}
}

// ---- v=1 正常 payload ----

func TestPayload_V1_Valid(t *testing.T) {
	payload := CredentialPayload{User: "u", Password: "p"}
	encoded, err := EncodePayload(payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(encoded), `"v":1`) {
		t.Error("expected v=1 in JSON")
	}
}

// ---- 空 user/password ----

func TestPayload_EmptyUser_Rejected(t *testing.T) {
	_, err := EncodePayload(CredentialPayload{User: "", Password: "p"})
	if err == nil {
		t.Fatal("expected error for empty user")
	}
}

func TestPayload_EmptyPassword_Rejected(t *testing.T) {
	_, err := EncodePayload(CredentialPayload{User: "u", Password: ""})
	if err == nil {
		t.Fatal("expected error for empty password")
	}
}

// ---- 不同 v 值 ----

func TestPayload_V2_Rejected(t *testing.T) {
	raw := []byte(`{"v":2,"user":"u","password":"p"}`)
	_, err := DecodePayload(raw)
	if err == nil {
		t.Fatal("expected error for v=2")
	}
}

func TestPayload_V0_Rejected(t *testing.T) {
	raw := []byte(`{"v":0,"user":"u","password":"p"}`)
	_, err := DecodePayload(raw)
	if err == nil {
		t.Fatal("expected error for v=0")
	}
}

func TestPayload_MissingV_Rejected(t *testing.T) {
	raw := []byte(`{"user":"u","password":"p"}`)
	_, err := DecodePayload(raw)
	if err == nil {
		t.Fatal("expected error for missing v")
	}
}

// ---- 未知字段 ----

func TestPayload_UnknownField_Rejected(t *testing.T) {
	raw := []byte(`{"v":1,"user":"u","password":"p","extra":"no"}`)
	_, err := DecodePayload(raw)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

// ---- 重复字段 ----

func TestPayload_DuplicateField_Rejected(t *testing.T) {
	raw := []byte(`{"v":1,"user":"u","user":"u2","password":"p"}`)
	_, err := DecodePayload(raw)
	if err == nil {
		t.Fatal("expected error for duplicate user field")
	}
}

func TestPayload_DuplicateV_Rejected(t *testing.T) {
	raw := []byte(`{"v":1,"v":2,"user":"u","password":"p"}`)
	_, err := DecodePayload(raw)
	if err == nil {
		t.Fatal("expected error for duplicate v field")
	}
}

// ---- 非法 UTF-8 ----

func TestPayload_InvalidUTF8_Rejected(t *testing.T) {
	raw := []byte(`{"v":1,"user":"u","password":"` + "\xff\xfe\xfd" + `"}`)
	_, err := DecodePayload(raw)
	if err == nil {
		t.Fatal("expected error for invalid UTF-8")
	}
}

// ---- user 控制字符 ----

func TestPayload_UserControlChar_Rejected(t *testing.T) {
	tests := []string{"\x00", "\x01", "\x1f"}
	for _, c := range tests {
		payload := CredentialPayload{User: "u" + c, Password: "p"}
		_, err := EncodePayload(payload)
		if err == nil {
			t.Errorf("expected error for user with control char %q", c)
		}
	}
}

// password 含控制字符允许

func TestPayload_PasswordControlChar_Allowed(t *testing.T) {
	payload := CredentialPayload{User: "u", Password: "p\x01\x1f"}
	encoded, err := EncodePayload(payload)
	if err != nil {
		t.Fatalf("expected password control chars allowed: %v", err)
	}
	decoded, err := DecodePayload(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Password != payload.Password {
		t.Errorf("password mismatch after round-trip")
	}
}

// ---- 总大小与 per-field 长度边界 ----

// per-field 限制（user≤255/password≤1024）使合法 payload 无法达到 4096 字节总上限；
// 此测试验证最大合法 per-field 长度的 round-trip。
func TestPayload_MaxPerFieldLengths_RoundTrip(t *testing.T) {
	payload := CredentialPayload{
		User:     strings.Repeat("u", maxUserBytes),
		Password: strings.Repeat("p", maxPasswordBytes),
	}
	encoded, err := EncodePayload(payload)
	if err != nil {
		t.Fatalf("encode max-length payload: %v", err)
	}
	decoded, err := DecodePayload(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.User != payload.User || decoded.Password != payload.Password {
		t.Error("round-trip mismatch")
	}
}

// DecodePayload 保留总大小 4096 兜底（对原始输入），即使合法 payload 达不到该上限。
func TestPayload_DecodeOver4096Bytes_Rejected(t *testing.T) {
	data := make([]byte, 4097)
	for i := range data {
		data[i] = 'a'
	}
	if _, err := DecodePayload(data); err == nil {
		t.Fatal("expected error for >4096 bytes")
	}
}

// ---- 前后空格保留 ----

func TestPayload_PreserveWhitespace(t *testing.T) {
	original := CredentialPayload{User: " user ", Password: " pass "}
	encoded, err := EncodePayload(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodePayload(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.User != " user " {
		t.Errorf("user leading/trailing space preserved: got %q", decoded.User)
	}
	if decoded.Password != " pass " {
		t.Errorf("password leading/trailing space preserved: got %q", decoded.Password)
	}
}

// ---- Unicode 保留原值 ----

func TestPayload_PreserveUnicode(t *testing.T) {
	original := CredentialPayload{User: "José", Password: "密码🔐"}
	encoded, err := EncodePayload(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodePayload(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.User != "José" {
		t.Errorf("user: got %q", decoded.User)
	}
	if decoded.Password != "密码🔐" {
		t.Errorf("password: got %q", decoded.Password)
	}
}

// ---- 明文不暴露 ----

func TestPayload_NoPlaintextInError(t *testing.T) {
	_, err := EncodePayload(CredentialPayload{User: "", Password: "secret_pw"})
	if err == nil {
		t.Fatal("expected error for empty user")
	}
	if strings.Contains(err.Error(), "secret_pw") {
		t.Error("password should not appear in error message")
	}
}

// ---- per-field 长度限制（PAY-06/PAY-07，UTF-8 字节数）----

func TestPayload_UserMax255Bytes_Valid(t *testing.T) {
	// 恰好 255 字节（ASCII）应通过
	user := strings.Repeat("a", 255)
	if _, err := EncodePayload(CredentialPayload{User: user, Password: "p"}); err != nil {
		t.Fatalf("expected 255-byte user to be valid, got: %v", err)
	}
}

func TestPayload_UserOver255Bytes_Rejected(t *testing.T) {
	// 256 字节（ASCII）应拒绝
	user := strings.Repeat("a", 256)
	if _, err := EncodePayload(CredentialPayload{User: user, Password: "p"}); err == nil {
		t.Fatal("expected error for user over 255 bytes")
	}
}

func TestPayload_UserMultiByteBoundary_Valid(t *testing.T) {
	// 多字节 UTF-8：127 个 "é"（2 字节）= 254 字节，加 1 ASCII = 255 字节，应通过
	user := strings.Repeat("é", 127) + "a"
	if len([]byte(user)) != 255 {
		t.Fatalf("test setup: expected 255 bytes, got %d", len([]byte(user)))
	}
	if _, err := EncodePayload(CredentialPayload{User: user, Password: "p"}); err != nil {
		t.Fatalf("expected 255-byte multi-byte user to be valid, got: %v", err)
	}
}

func TestPayload_PasswordMax1024Bytes_Valid(t *testing.T) {
	// 恰好 1024 字节（ASCII）应通过
	pw := strings.Repeat("p", 1024)
	if _, err := EncodePayload(CredentialPayload{User: "u", Password: pw}); err != nil {
		t.Fatalf("expected 1024-byte password to be valid, got: %v", err)
	}
}

func TestPayload_PasswordOver1024Bytes_Rejected(t *testing.T) {
	// 1025 字节（ASCII）应拒绝
	pw := strings.Repeat("p", 1025)
	if _, err := EncodePayload(CredentialPayload{User: "u", Password: pw}); err == nil {
		t.Fatal("expected error for password over 1024 bytes")
	}
}

func TestPayload_PasswordMultiByteBoundary_Valid(t *testing.T) {
	// 多字节 UTF-8：341 个 "密"（3 字节）= 1023 字节，加 1 ASCII = 1024 字节，应通过
	pw := strings.Repeat("密", 341) + "a"
	if len([]byte(pw)) != 1024 {
		t.Fatalf("test setup: expected 1024 bytes, got %d", len([]byte(pw)))
	}
	if _, err := EncodePayload(CredentialPayload{User: "u", Password: pw}); err != nil {
		t.Fatalf("expected 1024-byte multi-byte password to be valid, got: %v", err)
	}
}
