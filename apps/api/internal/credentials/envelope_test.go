package credentials

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/google/uuid"
)

func mustBuildAAD(t *testing.T, workspaceID, secretRef uuid.UUID, secretVersion int, suite string, kekVersion int) []byte {
	t.Helper()
	aad, err := BuildAAD(workspaceID, secretRef, secretVersion, suite, kekVersion)
	if err != nil {
		t.Fatalf("BuildAAD: %v", err)
	}
	return aad
}

// ---- AAD 测试 ----------------------------------------------------------------

func TestAAD_EncodeDecode(t *testing.T) {
	ws := uuid.New()
	ref := uuid.New()
	aad := mustBuildAAD(t, ws, ref, 1, SuiteAES256GCMv1, 1)

	if len(aad) != AADSize {
		t.Errorf("expected %d bytes, got %d", AADSize, len(aad))
	}

	if !bytes.Equal(aad[0:4], []byte{0x00, 0x00, 0x00, 0x01}) {
		t.Errorf("version_tag mismatch: %x", aad[0:4])
	}
}

func TestAAD_DataAndWrapAreIndependentBuffers(t *testing.T) {
	ws := uuid.New()
	ref := uuid.New()

	dataAAD := mustBuildAAD(t, ws, ref, 1, SuiteAES256GCMv1, 1)
	wrapAAD := mustBuildAAD(t, ws, ref, 1, SuiteAES256GCMv1, 1)

	if !bytes.Equal(dataAAD, wrapAAD) {
		t.Error("data AAD and wrap AAD must bind the same approved fields")
	}
	dataAAD[0] ^= 0xff
	if bytes.Equal(dataAAD, wrapAAD) {
		t.Error("data AAD and wrap AAD must be independently allocated")
	}
}

func TestAAD_SizeAlways48(t *testing.T) {
	ws := uuid.New()
	ref := uuid.New()

	for i := 0; i < 2; i++ {
		aad := mustBuildAAD(t, ws, ref, 5, SuiteAES256GCMv1, 3)
		if len(aad) != AADSize {
			t.Errorf("AAD size should always be %d, got %d", AADSize, len(aad))
		}
	}
}

func TestAAD_CrossWorkspaceFails(t *testing.T) {
	ws1 := uuid.New()
	ws2 := uuid.New()
	ref := uuid.New()

	aad1 := mustBuildAAD(t, ws1, ref, 1, SuiteAES256GCMv1, 1)
	aad2 := mustBuildAAD(t, ws2, ref, 1, SuiteAES256GCMv1, 1)

	if bytes.Equal(aad1, aad2) {
		t.Error("different workspace IDs should produce different AADs")
	}
}

func TestAAD_CrossSecretRefFails(t *testing.T) {
	ws := uuid.New()
	ref1 := uuid.New()
	ref2 := uuid.New()

	aad1 := mustBuildAAD(t, ws, ref1, 1, SuiteAES256GCMv1, 1)
	aad2 := mustBuildAAD(t, ws, ref2, 1, SuiteAES256GCMv1, 1)

	if bytes.Equal(aad1, aad2) {
		t.Error("different secret_ref should produce different AADs")
	}
}

func TestAAD_CrossVersionFails(t *testing.T) {
	ws := uuid.New()
	ref := uuid.New()

	aad1 := mustBuildAAD(t, ws, ref, 1, SuiteAES256GCMv1, 1)
	aad2 := mustBuildAAD(t, ws, ref, 2, SuiteAES256GCMv1, 1)

	if bytes.Equal(aad1, aad2) {
		t.Error("different versions should produce different AADs")
	}
}

// ---- Envelope round-trip -----------------------------------------------------

func TestEnvelope_RoundTrip(t *testing.T) {
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}

	payload := CredentialPayload{User: "testuser", Password: "testpass"}
	ws := uuid.New()
	ref := uuid.New()

	env, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek, rand.Reader)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	decoded, err := OpenEnvelope(env, ws, ref, kek)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if decoded.User != "testuser" || decoded.Password != "testpass" {
		t.Errorf("payload mismatch: %+v", decoded)
	}
}

func TestEnvelope_WrongKEK(t *testing.T) {
	kek1 := make([]byte, 32)
	kek2 := make([]byte, 32)
	rand.Read(kek1)
	rand.Read(kek2)

	payload := CredentialPayload{User: "u", Password: "p"}
	ws := uuid.New()
	ref := uuid.New()

	env, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek1, rand.Reader)
	_, err = OpenEnvelope(env, ws, ref, kek2)
	if err == nil {
		t.Fatal("expected decryption failure with wrong KEK")
	}
}

func TestEnvelope_WrongDataAAD(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)

	payload := CredentialPayload{User: "u", Password: "p"}
	ws := uuid.New()
	ref := uuid.New()

	env, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek, rand.Reader)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	wrongWS := uuid.New()
	_, err = OpenEnvelope(env, wrongWS, ref, kek)
	if err == nil {
		t.Fatal("expected failure with wrong workspace (AAD mismatch)")
	}
}

func TestEnvelope_CiphertextTamper(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)

	payload := CredentialPayload{User: "u", Password: "p"}
	ws := uuid.New()
	ref := uuid.New()

	env, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek, rand.Reader)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	env.Ciphertext[0] ^= 0x01
	_, err = OpenEnvelope(env, ws, ref, kek)
	if err == nil {
		t.Fatal("expected failure with tampered ciphertext")
	}
}

func TestEnvelope_WrappedDEKTamper(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)

	payload := CredentialPayload{User: "u", Password: "p"}
	ws := uuid.New()
	ref := uuid.New()

	env, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek, rand.Reader)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	env.WrappedDEK[0] ^= 0x01
	_, err = OpenEnvelope(env, ws, ref, kek)
	if err == nil {
		t.Fatal("expected failure with tampered wrapped DEK")
	}
}

func TestEnvelope_DataNonceTamper(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)

	payload := CredentialPayload{User: "u", Password: "p"}
	ws := uuid.New()
	ref := uuid.New()

	env, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek, rand.Reader)
	env.DataNonce[0] ^= 0x01
	_, err = OpenEnvelope(env, ws, ref, kek)
	if err == nil {
		t.Fatal("expected failure with tampered data nonce")
	}
}

func TestEnvelope_WrapNonceTamper(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)

	payload := CredentialPayload{User: "u", Password: "p"}
	ws := uuid.New()
	ref := uuid.New()

	env, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek, rand.Reader)
	env.WrapNonce[0] ^= 0x01
	_, err = OpenEnvelope(env, ws, ref, kek)
	if err == nil {
		t.Fatal("expected failure with tampered wrap nonce")
	}
}

func TestEnvelope_NoncesIndependent(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)

	payload := CredentialPayload{User: "u", Password: "p"}
	ws := uuid.New()
	ref := uuid.New()

	seenData := make(map[string]bool)
	seenWrap := make(map[string]bool)

	for i := 0; i < 100; i++ {
		env, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		ds := string(env.DataNonce)
		wn := string(env.WrapNonce)
		if seenData[ds] {
			t.Error("data nonce reused")
		}
		if seenWrap[wn] {
			t.Error("wrap nonce reused")
		}
		seenData[ds] = true
		seenWrap[wn] = true
	}
}

func TestEnvelope_UnknownSuite(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)
	payload := CredentialPayload{User: "u", Password: "p"}
	ws := uuid.New()
	ref := uuid.New()

	_, err := SealEnvelope(payload, ws, ref, 1, "UNKNOWN-SUITE", 1, kek, rand.Reader)
	if err == nil {
		t.Fatal("expected error for unknown suite")
	}
}

func TestEnvelope_SuiteMismatchOnOpen(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)
	payload := CredentialPayload{User: "u", Password: "p"}
	ws := uuid.New()
	ref := uuid.New()

	env, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek, rand.Reader)
	env.EnvelopeSuite = "BROKEN-SUITE"
	_, err = OpenEnvelope(env, ws, ref, kek)
	if err == nil {
		t.Fatal("expected failure with suite mismatch")
	}
}

func TestEnvelope_KEKVersionMismatch(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)
	payload := CredentialPayload{User: "u", Password: "p"}
	ws := uuid.New()
	ref := uuid.New()

	env, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek, rand.Reader)
	env.KEKVersion = 99
	_, err = OpenEnvelope(env, ws, ref, kek)
	if err == nil {
		t.Fatal("expected failure with KEK version mismatch in AAD")
	}
}

// ---- 随机源失败 --------------------------------------------------------------

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errTestRandomFailure
}

var errTestRandomFailure = &testError{"random source failure"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func TestEnvelope_RandomSourceFailure(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)
	payload := CredentialPayload{User: "u", Password: "p"}
	ws := uuid.New()
	ref := uuid.New()

	_, err := SealEnvelope(payload, ws, ref, 1, SuiteAES256GCMv1, 1, kek, failingReader{})
	if err == nil {
		t.Fatal("expected error with failing random source")
	}
}

// ---- Golden vector ----------------------------------------------------------

func TestAAD_GoldenVector(t *testing.T) {
	ws := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	ref := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	aad := mustBuildAAD(t, ws, ref, 1, SuiteAES256GCMv1, 1)

	expected := []byte{
		0x00, 0x00, 0x00, 0x01,
		0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11,
		0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11,
		0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22,
		0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22,
		0x00, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x01,
	}

	if !bytes.Equal(aad, expected) {
		t.Errorf("golden vector mismatch\ngot:      %x\nexpected: %x", aad, expected)
	}
	if len(aad) != 48 {
		t.Errorf("golden vector length: got %d, want 48", len(aad))
	}
}
