package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

const (
	dekSize   = 32
	nonceSize = 12
)

// SealEnvelope 加密 CredentialPayload 并返回 Envelope。
// randomSource 用于生成 DEK 和 nonce；生产环境使用 crypto/rand.Reader。
func SealEnvelope(payload CredentialPayload, workspaceID, secretRef uuid.UUID, secretVersion int, suite string, kekVersion int, kek []byte, randomSource io.Reader) (*metadata.CredentialEnvelope, error) {
	suiteTag, ok := SuiteTagFromString(suite)
	if !ok {
		return nil, fmt.Errorf("%w: unknown suite %q", ErrUnknownSuite, suite)
	}
	_ = suiteTag

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

	dataAAD := BuildAAD(DataAADTag, workspaceID, secretRef, secretVersion, suite, kekVersion)

	ciphertext, err := aesGCMSeal(dek, dataNonce, plaintext, dataAAD)
	if err != nil {
		return nil, fmt.Errorf("%w: seal payload: %v", ErrInternalError, err)
	}

	wrapNonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(randomSource, wrapNonce); err != nil {
		return nil, fmt.Errorf("%w: generate wrap nonce: %v", ErrInternalError, err)
	}

	wrapAAD := BuildAAD(WrapAADTag, workspaceID, secretRef, secretVersion, suite, kekVersion)

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
	suiteTag, ok := SuiteTagFromString(env.EnvelopeSuite)
	if !ok {
		return CredentialPayload{}, fmt.Errorf("%w: %q", ErrUnknownSuite, env.EnvelopeSuite)
	}
	_ = suiteTag

	dataAAD := BuildAAD(DataAADTag, workspaceID, secretRef, env.Version, env.EnvelopeSuite, env.KEKVersion)
	wrapAAD := BuildAAD(WrapAADTag, workspaceID, secretRef, env.Version, env.EnvelopeSuite, env.KEKVersion)

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
	return gcm.Open(nil, nonce, ciphertext, aad)
}

func readRand(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

func generateDEK() ([]byte, error) {
	return readRand(dekSize)
}

func generateNonce() ([]byte, error) {
	return readRand(nonceSize)
}

func ValidateKEK(kek []byte) error {
	if len(kek) != dekSize {
		return fmt.Errorf("%w: expected %d bytes, got %d", ErrInvalidKEK, dekSize, len(kek))
	}
	return nil
}

func putUint32BE(b []byte, v uint32) {
	binary.BigEndian.PutUint32(b, v)
}
