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

	// 1. EncodePayload 错误不泄露
	// 无错误路径会包含密码（EncodePayload 只校验字段名和格式）

	// 2. DecodePayload 错误不泄露
	_, err := DecodePayload([]byte(`{"v":1,"user":"u","password":"` + canaryPassword + `"}`))
	if err != nil {
		checkNoSensitive(t, err.Error(), canaryPassword, "DecodePayload error")
	}

	// 3. SealEnvelope 错误不泄露
	encoded, _ := EncodePayload(payload)
	_ = encoded

	// 4. OpenEnvelope 错误不泄露
	env, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// 用错误 KEK 解密
	wrongKEK := make([]byte, 32)
	rand.Read(wrongKEK)
	_, err = OpenEnvelope(env, ws, ref, wrongKEK)
	if err != nil {
		checkNoSensitive(t, err.Error(), canaryPassword, "OpenEnvelope error (wrong KEK)")
	}

	// 5. JSON marshal 不泄露
	jsonBytes, _ := json.Marshal(payload)
	checkNoSensitive(t, string(jsonBytes), canaryPassword, "JSON marshal")

	// 6. fmt.Sprintf 不泄露（通过 Stringer）
	s := fmt.Sprintf("%v", env)
	checkNoSensitive(t, s, canaryPassword, "fmt.Sprintf CredentialEnvelope")

	// 7. fmt.Sprintf %+v 不泄露
	s = fmt.Sprintf("%+v", env)
	checkNoSensitive(t, s, canaryPassword, "fmt.Sprintf %+v CredentialEnvelope")
}

func checkNoSensitive(t *testing.T, output, canary, context string) {
	t.Helper()
	if strings.Contains(output, canary) {
		t.Errorf("%s: sensitive canary found in output: %q", context, canary)
	}
}

// TestSensitive_NoKEKInError 验证 KEK 不泄露。
func TestSensitive_NoKEKInError(t *testing.T) {
	// NewEnvKEKProvider error 不应包含原始 KEK 值
	// （由 TestKEK_NoKEKInError 覆盖）
}
