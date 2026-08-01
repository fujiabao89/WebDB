package credentials

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// ---- 常量 --------------------------------------------------------------------

const (
	maxWrapsPerKEK = 1 << 24 // 2^24
)

// ---- KEKProvider 接口 --------------------------------------------------------

// KEKProvider 提供版本化 KEK 访问。
type KEKProvider interface {
	ActiveKEK() (version int, key []byte, err error)
	GetKEK(version int) ([]byte, error)
	RecordWrap(version int) error
	WrapCount(version int) uint64
}

// ---- 环境变量 Provider --------------------------------------------------------

type envKEKProvider struct {
	keys          map[int][]byte
	activeVersion int
	counters      map[int]*atomic.Uint64
	mu            sync.Mutex
}

// NewEnvKEKProvider 从环境变量创建 KEK Provider。
// 环境变量: WEBDB_KEK_V{N} (RFC 4648 padded Base64, 32 bytes)
//
//	WEBDB_ACTIVE_KEK_VERSION (正整数)
func NewEnvKEKProvider() (KEKProvider, error) {
	activeStr := os.Getenv("WEBDB_ACTIVE_KEK_VERSION")
	if activeStr == "" {
		return nil, fmt.Errorf("%w: WEBDB_ACTIVE_KEK_VERSION is not set", ErrInvalidKEK)
	}

	activeVersion, err := strconv.Atoi(activeStr)
	if err != nil || activeVersion <= 0 {
		return nil, fmt.Errorf("%w: WEBDB_ACTIVE_KEK_VERSION must be a positive integer, got %q", ErrInvalidKEK, activeStr)
	}

	keys := make(map[int][]byte)

	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "WEBDB_KEK_V") {
			continue
		}
		kv := strings.SplitN(env, "=", 2)
		if len(kv) != 2 {
			continue
		}
		name := kv[0]
		value := kv[1]

		verStr := strings.TrimPrefix(name, "WEBDB_KEK_V")
		ver, err := strconv.Atoi(verStr)
		if err != nil || ver <= 0 {
			return nil, fmt.Errorf("%w: invalid KEK version in %s", ErrInvalidKEK, name)
		}

		if value == "" {
			return nil, fmt.Errorf("%w: empty KEK value for %s", ErrInvalidKEK, name)
		}

		// 严格 Base64 解码 (RFC 4648 padded)
		key, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid base64 for %s", ErrInvalidKEK, name)
		}

		if len(key) != 32 {
			return nil, fmt.Errorf("%w: %s must be 32 bytes, got %d", ErrInvalidKEK, name, len(key))
		}

		if _, exists := keys[ver]; exists {
			return nil, fmt.Errorf("%w: duplicate KEK version %d", ErrInvalidKEK, ver)
		}

		keys[ver] = key
	}

	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no WEBDB_KEK_V{N} environment variables found", ErrInvalidKEK)
	}

	if _, exists := keys[activeVersion]; !exists {
		return nil, fmt.Errorf("%w: active KEK version %d has no WEBDB_KEK_V%d variable", ErrInvalidKEK, activeVersion, activeVersion)
	}

	// 交叉验证：不同版本不能使用相同密钥值
	for v1, k1 := range keys {
		for v2, k2 := range keys {
			if v1 < v2 && bytes.Equal(k1, k2) {
				return nil, fmt.Errorf("%w: KEK V%d and V%d have identical values", ErrInvalidKEK, v1, v2)
			}
		}
	}

	counters := make(map[int]*atomic.Uint64)
	for v := range keys {
		counters[v] = &atomic.Uint64{}
	}

	return &envKEKProvider{
		keys:          keys,
		activeVersion: activeVersion,
		counters:      counters,
	}, nil
}

func (p *envKEKProvider) ActiveKEK() (int, []byte, error) {
	key, ok := p.keys[p.activeVersion]
	if !ok {
		return 0, nil, fmt.Errorf("%w: active version %d", ErrUnknownKEKVersion, p.activeVersion)
	}
	out := make([]byte, len(key))
	copy(out, key)
	return p.activeVersion, out, nil
}

func (p *envKEKProvider) GetKEK(version int) ([]byte, error) {
	key, ok := p.keys[version]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknownKEKVersion, version)
	}
	out := make([]byte, len(key))
	copy(out, key)
	return out, nil
}

func (p *envKEKProvider) RecordWrap(version int) error {
	p.mu.Lock()
	ctr, ok := p.counters[version]
	if !ok {
		p.mu.Unlock()
		return fmt.Errorf("%w: %d", ErrUnknownKEKVersion, version)
	}
	p.mu.Unlock()

	// 递增并检查上限
	current := ctr.Add(1)
	if current > maxWrapsPerKEK {
		ctr.Add(^uint64(0)) // 回退（best-effort）
		return fmt.Errorf("%w: KEK version %d exceeded wrap limit of %d", ErrInternalError, version, maxWrapsPerKEK)
	}
	return nil
}

func (p *envKEKProvider) WrapCount(version int) uint64 {
	p.mu.Lock()
	ctr, ok := p.counters[version]
	p.mu.Unlock()
	if !ok {
		return 0
	}
	return ctr.Load()
}
