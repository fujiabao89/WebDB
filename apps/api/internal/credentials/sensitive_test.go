package credentials

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestSensitive_NoPasswordInError 验证各类错误路径不泄露密码。
func TestSensitive_NoPasswordInError(t *testing.T) {
	canaryPassword := "canary-pw-!@#$%^&*()"
	kek := make([]byte, 32)
	rand.Read(kek)

	payload := CredentialPayload{User: "canary-user", Password: canaryPassword}
	ws := uuid.New()
	ref := uuid.New()

	// 1. EncodePayload 错误不泄露（空 user 触发错误）
	_, err := EncodePayload(CredentialPayload{User: "", Password: canaryPassword})
	if err == nil {
		t.Fatal("expected error for empty user")
	}
	checkNoSensitive(t, err.Error(), canaryPassword, "EncodePayload error")

	// 2. DecodePayload 错误不泄露（未知字段触发错误）
	_, err = DecodePayload([]byte(`{"v":1,"user":"u","password":"` + canaryPassword + `","extra":1}`))
	if err == nil {
		t.Fatal("expected decode error for unknown field")
	}
	checkNoSensitive(t, err.Error(), canaryPassword, "DecodePayload error")

	// 3. SealEnvelope 错误不泄露（未知 suite 触发错误）
	_, err = SealEnvelope(payload, ws, ref, 1, "UNKNOWN-SUITE", 1, kek, rand.Reader)
	if err == nil {
		t.Fatal("expected seal error for unknown suite")
	}
	checkNoSensitive(t, err.Error(), canaryPassword, "SealEnvelope error")

	// 4. OpenEnvelope 错误不泄露（错误 KEK）
	env, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	wrongKEK := make([]byte, 32)
	rand.Read(wrongKEK)
	_, err = OpenEnvelope(env, ws, ref, wrongKEK)
	if err == nil {
		t.Fatal("expected error for wrong KEK")
	}
	checkNoSensitive(t, err.Error(), canaryPassword, "OpenEnvelope error (wrong KEK)")

	// 5. JSON marshal 不泄露（Password 字段 json:"-"）
	jsonBytes, _ := json.Marshal(payload)
	checkNoSensitive(t, string(jsonBytes), canaryPassword, "JSON marshal")

	// 6. fmt.Sprintf 不泄露
	checkNoSensitive(t, fmt.Sprintf("%v", payload), canaryPassword, "fmt.Sprintf %v CredentialPayload")
	checkNoSensitive(t, fmt.Sprintf("%+v", payload), canaryPassword, "fmt.Sprintf %+v CredentialPayload")

	// 7. CredentialEnvelope 格式化不泄露
	s := fmt.Sprintf("%v", env)
	checkNoSensitive(t, s, canaryPassword, "fmt.Sprintf CredentialEnvelope")
	s = fmt.Sprintf("%+v", env)
	checkNoSensitive(t, s, canaryPassword, "fmt.Sprintf %+v CredentialEnvelope")
}

func checkNoSensitive(t *testing.T, output, canary, context string) {
	t.Helper()
	if strings.Contains(output, canary) {
		t.Errorf("%s: sensitive canary found in output: %q", context, canary)
	}
}

// TestSensitive_NoKEKInError 验证 KEK 错误信息不泄露原始密钥值。
func TestSensitive_NoKEKInError(t *testing.T) {
	// 设置一个无效的 KEK（长度不对），验证错误信息不含密钥内容。
	t.Setenv("WEBDB_ACTIVE_KEK_VERSION", "1")
	t.Setenv("WEBDB_KEK_V1", "dG9vLXNob3J0") // 仅 9 bytes Base64，解析后不足 32 bytes

	_, err := NewEnvKEKProvider()
	if err == nil {
		t.Fatal("expected error for invalid KEK")
	}
	// 验证错误信息不含原始 KEK Base64 值
	if strings.Contains(err.Error(), "dG9vLXNob3J0") {
		t.Error("KEK value should not appear in error message")
	}
}
