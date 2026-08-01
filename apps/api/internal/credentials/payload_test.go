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

// ---- 4096 bytes 边界 ----

func TestPayload_Exactly4096Bytes(t *testing.T) {
	overhead := len(`{"v":1,"user":"","password":""}`)
	remaining := 4096 - overhead
	if remaining < 3 {
		t.Skip("overhead too large")
	}
	userLen := remaining - 1
	payload := CredentialPayload{
		User:     strings.Repeat("x", userLen),
		Password: "p",
	}
	encoded, err := EncodePayload(payload)
	if err != nil {
		t.Fatalf("encode 4096 bytes: %v", err)
	}
	if len(encoded) != 4096 {
		t.Errorf("expected exactly 4096 bytes, got %d", len(encoded))
	}
	decoded, err := DecodePayload(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.User != payload.User {
		t.Error("user mismatch")
	}
}

func TestPayload_Exceeds4096Bytes_Rejected(t *testing.T) {
	overhead := len(`{"v":1,"user":"","password":""}`)
	remaining := 4097 - overhead
	payload := CredentialPayload{
		User:     strings.Repeat("x", remaining),
		Password: "p",
	}
	_, err := EncodePayload(payload)
	if err == nil {
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
