package credentials

import (
	"encoding/binary"

	"github.com/google/uuid"
)

// ---- AAD 常量 ----------------------------------------------------------------

const (
	AADSize           = 48
	versionTag uint32 = 0x00000001
	DataAADTag uint32 = 1
	WrapAADTag uint32 = 2
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
// 格式: version_tag(3B) || domain_tag(1B) || workspace_id(16B) || secret_ref(16B) ||
//
//	secret_version(4B) || envelope_suite_tag(4B) || kek_version(4B)
//
// domain_tag 占据 version_tag 的第 4 字节（DataAADTag=1, WrapAADTag=2），实现 data/wrap 域分离。
// 调用方（SealEnvelope / OpenEnvelope）在上游校验 suite 有效性，BuildAAD 本身不返回 error。
func BuildAAD(domainTag uint32, workspaceID, secretRef uuid.UUID, secretVersion int, suite string, kekVersion int) []byte {
	suiteTag, _ := SuiteTagFromString(suite)

	aad := make([]byte, AADSize)
	offset := 0

	binary.BigEndian.PutUint32(aad[offset:], versionTag)
	aad[3] = byte(domainTag)
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

	return aad
}
