package credentials

import (
	"encoding/base64"
	"os"
	"strings"
	"sync"
	"testing"
)

func testKEKBase64() string {
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(kek)
}

func setEnv(t *testing.T, key, val string) {
	t.Helper()
	if err := os.Setenv(key, val); err != nil {
		t.Fatalf("setenv: %v", err)
	}
}

func unsetWebDBEnv(t *testing.T) {
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "WEBDB_KEK_V") || e == "WEBDB_ACTIVE_KEK_VERSION=" || strings.HasPrefix(e, "WEBDB_ACTIVE_KEK_VERSION=") {
			kv := strings.SplitN(e, "=", 2)
			os.Unsetenv(kv[0])
		}
	}
}

func TestKEK_ValidSingleVersion(t *testing.T) {
	unsetWebDBEnv(t)
	setEnv(t, "WEBDB_KEK_V1", testKEKBase64())
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	provider, err := NewEnvKEKProvider()
	if err != nil {
		t.Fatalf("NewEnvKEKProvider: %v", err)
	}

	ver, key, err := provider.ActiveKEK()
	if err != nil {
		t.Fatalf("ActiveKEK: %v", err)
	}
	if ver != 1 {
		t.Errorf("expected version 1, got %d", ver)
	}
	if len(key) != 32 {
		t.Errorf("expected 32-byte key, got %d", len(key))
	}

	kek, err := provider.GetKEK(1)
	if err != nil {
		t.Fatalf("GetKEK: %v", err)
	}
	if len(kek) != 32 {
		t.Errorf("expected 32-byte key, got %d", len(kek))
	}
}

func TestKEK_MultiVersion(t *testing.T) {
	unsetWebDBEnv(t)
	setEnv(t, "WEBDB_KEK_V1", testKEKBase64())
	// V2: different key
	kek2 := make([]byte, 32)
	for i := range kek2 {
		kek2[i] = byte(255 - i)
	}
	setEnv(t, "WEBDB_KEK_V2", base64.StdEncoding.EncodeToString(kek2))
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	provider, err := NewEnvKEKProvider()
	if err != nil {
		t.Fatalf("NewEnvKEKProvider: %v", err)
	}

	ver, _, err := provider.ActiveKEK()
	if err != nil {
		t.Fatalf("ActiveKEK: %v", err)
	}
	if ver != 1 {
		t.Errorf("active version should be explicitly configured as 1, got %d", ver)
	}

	kek1, _ := provider.GetKEK(1)
	kek2, _ = provider.GetKEK(2)
	if len(kek1) != 32 || len(kek2) != 32 {
		t.Error("both versions should be available")
	}
}

func TestKEK_ActiveVersionMustBeConfigured(t *testing.T) {
	unsetWebDBEnv(t)
	setEnv(t, "WEBDB_KEK_V1", testKEKBase64())
	setEnv(t, "WEBDB_KEK_V2", testKEKBase64())
	os.Unsetenv("WEBDB_ACTIVE_KEK_VERSION")

	_, err := NewEnvKEKProvider()
	if err == nil {
		t.Fatal("expected error when ACTIVE_KEK_VERSION is missing")
	}
}

func TestKEK_ActiveVersionMustExist(t *testing.T) {
	unsetWebDBEnv(t)
	setEnv(t, "WEBDB_KEK_V1", testKEKBase64())
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "99")

	_, err := NewEnvKEKProvider()
	if err == nil {
		t.Fatal("expected error when active version doesn't exist")
	}
}

func TestKEK_UnknownVersion(t *testing.T) {
	unsetWebDBEnv(t)
	setEnv(t, "WEBDB_KEK_V1", testKEKBase64())
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	provider, _ := NewEnvKEKProvider()
	_, err := provider.GetKEK(99)
	if err == nil {
		t.Fatal("expected error for unknown version")
	}
}

func TestKEK_InvalidBase64(t *testing.T) {
	unsetWebDBEnv(t)
	setEnv(t, "WEBDB_KEK_V1", "not!!base64!!!")
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	_, err := NewEnvKEKProvider()
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestKEK_NonPaddedBase64(t *testing.T) {
	unsetWebDBEnv(t)
	// Generate non-padded base64
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	nonPadded := base64.RawStdEncoding.EncodeToString(kek)
	setEnv(t, "WEBDB_KEK_V1", nonPadded)
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	_, err := NewEnvKEKProvider()
	if err == nil {
		t.Fatal("expected error for non-padded base64 (strict decoding required)")
	}
}

func TestKEK_WrongLength(t *testing.T) {
	unsetWebDBEnv(t)
	// 16 bytes instead of 32
	kek := make([]byte, 16)
	setEnv(t, "WEBDB_KEK_V1", base64.StdEncoding.EncodeToString(kek))
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	_, err := NewEnvKEKProvider()
	if err == nil {
		t.Fatal("expected error for wrong key length")
	}
}

func TestKEK_NoKEKConfigured(t *testing.T) {
	unsetWebDBEnv(t)
	os.Unsetenv("WEBDB_ACTIVE_KEK_VERSION")

	_, err := NewEnvKEKProvider()
	if err == nil {
		t.Fatal("expected error when no KEKs configured")
	}
}

func TestKEK_WrappingCounterIncrement(t *testing.T) {
	unsetWebDBEnv(t)
	setEnv(t, "WEBDB_KEK_V1", testKEKBase64())
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	provider, _ := NewEnvKEKProvider()
	ver, _, _ := provider.ActiveKEK()

	if ver != 1 {
		t.Fatal("version mismatch")
	}

	// 验证计数器增加
	initial := provider.WrapCount(ver)
	provider.RecordWrap(ver)
	if provider.WrapCount(ver) != initial+1 {
		t.Error("wrap counter should increment")
	}
}

func TestKEK_WrappingCounterLimit(t *testing.T) {
	unsetWebDBEnv(t)
	setEnv(t, "WEBDB_KEK_V1", testKEKBase64())
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	provider, _ := NewEnvKEKProvider()
	ver, _, _ := provider.ActiveKEK()

	// Set counter near limit
	provider.(*envKEKProvider).mu.Lock()
	provider.(*envKEKProvider).counters[ver].Store(maxWrapsPerKEK - 1)
	provider.(*envKEKProvider).mu.Unlock()

	// Should be allowed
	if err := provider.RecordWrap(ver); err != nil {
		t.Fatalf("expected wrap at near-limit to succeed: %v", err)
	}

	// Now at limit - should fail
	if err := provider.RecordWrap(ver); err == nil {
		t.Fatal("expected wrap at limit to be rejected")
	}
}

func TestKEK_WrappingCounterConcurrent(t *testing.T) {
	unsetWebDBEnv(t)
	setEnv(t, "WEBDB_KEK_V1", testKEKBase64())
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	provider, _ := NewEnvKEKProvider()
	ver, _, _ := provider.ActiveKEK()

	var wg sync.WaitGroup
	n := 100
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			provider.RecordWrap(ver)
		}()
	}
	wg.Wait()

	count := provider.WrapCount(ver)
	if count != uint64(n) {
		t.Errorf("expected count %d, got %d after %d concurrent increments", n, count, n)
	}
}

func TestKEK_NoDefaultWeakKEK(t *testing.T) {
	unsetWebDBEnv(t)
	setEnv(t, "WEBDB_KEK_V1", "Y2hhbmdlX21l") // base64 of "change_me" (9 bytes)
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	_, err := NewEnvKEKProvider()
	if err == nil {
		t.Fatal("expected short key to be rejected")
	}
}

func TestKEK_DuplicateKeyValuesRejected(t *testing.T) {
	unsetWebDBEnv(t)
	kek := testKEKBase64()
	setEnv(t, "WEBDB_KEK_V1", kek)
	setEnv(t, "WEBDB_KEK_V2", kek)
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	_, err := NewEnvKEKProvider()
	if err == nil {
		t.Fatal("expected duplicate KEK values to be rejected")
	}
}

func TestKEK_NoKEKInError(t *testing.T) {
	unsetWebDBEnv(t)
	setEnv(t, "WEBDB_KEK_V1", "!!!!!!not-valid-base64!!!!!!")
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	_, err := NewEnvKEKProvider()
	if err == nil {
		t.Fatal("expected error")
	}
	errStr := err.Error()
	if strings.Contains(errStr, "!!!!!!not-valid-base64!!!!!!") {
		t.Error("error message should not contain raw KEK value")
	}
}

func TestKEK_IllegalVersionFormat(t *testing.T) {
	unsetWebDBEnv(t)
	setEnv(t, "WEBDB_KEK_Vabc", testKEKBase64())
	setEnv(t, "WEBDB_ACTIVE_KEK_VERSION", "1")

	_, err := NewEnvKEKProvider()
	if err == nil {
		t.Fatal("expected error for non-numeric version")
	}
}
