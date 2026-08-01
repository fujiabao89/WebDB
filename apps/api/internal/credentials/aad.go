package credentials

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/google/uuid"
)

// ---- AAD 常量 ----------------------------------------------------------------

const (
	AADSize           = 48
	versionTag uint32 = 0x00000001
)

type SuiteTag uint32

const (
	SuiteTagAES256GCMv1 SuiteTag = 1
)

const SuiteAES256GCMv1 = "AES256GCM-v1"

var suiteTagMap = map[string]SuiteTag{
	SuiteAES256GCMv1: SuiteTagAES256GCMv1,
}

func SuiteTagFromString(s string) (SuiteTag, bool) {
	t, ok := suiteTagMap[s]
	return t, ok
}

// BuildAAD 构造 48-byte 确定性二进制 AAD（大端序）。
// 格式: version_tag(4B) || workspace_id(16B) || secret_ref(16B) ||
//
//	secret_version(4B) || envelope_suite_tag(4B) || kek_version(4B)
func BuildAAD(workspaceID, secretRef uuid.UUID, secretVersion int, suite string, kekVersion int) ([]byte, error) {
	suiteTag, ok := SuiteTagFromString(suite)
	if !ok {
		return nil, fmt.Errorf("%w: unsupported AAD suite", ErrUnknownSuite)
	}
	if !validAADVersion(secretVersion) {
		return nil, fmt.Errorf("%w: invalid secret version for AAD", ErrInternalError)
	}
	if !validAADVersion(kekVersion) {
		return nil, fmt.Errorf("%w: invalid KEK version for AAD", ErrInternalError)
	}

	aad := make([]byte, AADSize)
	offset := 0

	binary.BigEndian.PutUint32(aad[offset:], versionTag)
	offset += 4

	copy(aad[offset:], workspaceID[:])
	offset += 16

	copy(aad[offset:], secretRef[:])
	offset += 16

	binary.BigEndian.PutUint32(aad[offset:], uint32(secretVersion))
	offset += 4

	binary.BigEndian.PutUint32(aad[offset:], uint32(suiteTag))
	offset += 4

	binary.BigEndian.PutUint32(aad[offset:], uint32(kekVersion))

	return aad, nil
}

func validAADVersion(version int) bool {
	return version > 0 && uint64(version) <= math.MaxUint32
}
