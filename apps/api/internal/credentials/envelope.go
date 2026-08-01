package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

const (
	kekSize   = 32
	dekSize   = 32
	nonceSize = 12
)

// SealEnvelope 加密 CredentialPayload 并返回 Envelope。
// randomSource 用于生成 DEK 和 nonce；生产环境使用 crypto/rand.Reader。
func SealEnvelope(payload CredentialPayload, workspaceID, secretRef uuid.UUID, secretVersion int, suite string, kekVersion int, kek []byte, randomSource io.Reader) (*metadata.CredentialEnvelope, error) {
	if _, ok := SuiteTagFromString(suite); !ok {
		return nil, fmt.Errorf("%w: unknown suite %q", ErrUnknownSuite, suite)
	}

	if err := ValidateKEK(kek); err != nil {
		return nil, err
	}

	plaintext, err := EncodePayload(payload)
	if err != nil {
		return nil, err
	}

	dek := make([]byte, dekSize)
	if _, err := io.ReadFull(randomSource, dek); err != nil {
		return nil, fmt.Errorf("%w: generate DEK: %v", ErrInternalError, err)
	}

	dataNonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(randomSource, dataNonce); err != nil {
		return nil, fmt.Errorf("%w: generate data nonce: %v", ErrInternalError, err)
	}

	dataAAD, err := BuildAAD(workspaceID, secretRef, secretVersion, suite, kekVersion)
	if err != nil {
		return nil, err
	}

	ciphertext, err := aesGCMSeal(dek, dataNonce, plaintext, dataAAD)
	if err != nil {
		return nil, fmt.Errorf("%w: seal payload: %v", ErrInternalError, err)
	}

	wrapNonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(randomSource, wrapNonce); err != nil {
		return nil, fmt.Errorf("%w: generate wrap nonce: %v", ErrInternalError, err)
	}

	wrapAAD, err := BuildAAD(workspaceID, secretRef, secretVersion, suite, kekVersion)
	if err != nil {
		return nil, err
	}

	wrappedDEK, err := aesGCMSeal(kek, wrapNonce, dek, wrapAAD)
	if err != nil {
		return nil, fmt.Errorf("%w: wrap DEK: %v", ErrInternalError, err)
	}

	return &metadata.CredentialEnvelope{
		WorkspaceID:   workspaceID,
		SecretRef:     secretRef,
		Version:       secretVersion,
		Ciphertext:    ciphertext,
		DataNonce:     dataNonce,
		WrappedDEK:    wrappedDEK,
		WrapNonce:     wrapNonce,
		EnvelopeSuite: suite,
		KEKVersion:    kekVersion,
	}, nil
}

// OpenEnvelope 解密 CredentialEnvelope 并返回 Payload。
func OpenEnvelope(env *metadata.CredentialEnvelope, workspaceID, secretRef uuid.UUID, kek []byte) (CredentialPayload, error) {
	if _, ok := SuiteTagFromString(env.EnvelopeSuite); !ok {
		return CredentialPayload{}, fmt.Errorf("%w: %q", ErrUnknownSuite, env.EnvelopeSuite)
	}

	if err := ValidateKEK(kek); err != nil {
		return CredentialPayload{}, err
	}

	dataAAD, err := BuildAAD(workspaceID, secretRef, env.Version, env.EnvelopeSuite, env.KEKVersion)
	if err != nil {
		return CredentialPayload{}, err
	}
	wrapAAD, err := BuildAAD(workspaceID, secretRef, env.Version, env.EnvelopeSuite, env.KEKVersion)
	if err != nil {
		return CredentialPayload{}, err
	}

	dek, err := aesGCMOpen(kek, env.WrapNonce, env.WrappedDEK, wrapAAD)
	if err != nil {
		return CredentialPayload{}, fmt.Errorf("%w: unwrap DEK", ErrDecryptionFailed)
	}

	plaintext, err := aesGCMOpen(dek, env.DataNonce, env.Ciphertext, dataAAD)
	if err != nil {
		return CredentialPayload{}, fmt.Errorf("%w: decrypt payload", ErrDecryptionFailed)
	}

	payload, err := DecodePayload(plaintext)
	if err != nil {
		return CredentialPayload{}, err
	}

	return payload, nil
}

func aesGCMSeal(key, nonce, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("%w: invalid nonce length", ErrDecryptionFailed)
	}
	return gcm.Seal(nil, nonce, plaintext, aad), nil
}

func aesGCMOpen(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("%w: invalid nonce length", ErrDecryptionFailed)
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

func ValidateKEK(kek []byte) error {
	if len(kek) != kekSize {
		return fmt.Errorf("%w: expected %d bytes, got %d", ErrInvalidKEK, kekSize, len(kek))
	}
	return nil
}
