package credentials

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// LifecycleManager 管理凭证创建、解析、轮换和退役。
type LifecycleManager struct {
	store metadata.CredentialTXStore
	conns metadata.ConnectionTXStore
	kek   KEKProvider
	db    *sql.DB
}

// NewLifecycleManager 创建 LifecycleManager。
// db 用于开启事务。
func NewLifecycleManager(db *sql.DB, store metadata.CredentialTXStore, conns metadata.ConnectionTXStore, kek KEKProvider) *LifecycleManager {
	return &LifecycleManager{store: store, conns: conns, kek: kek, db: db}
}

// Create 创建新凭证并返回 Envelope。
func (m *LifecycleManager) Create(ctx context.Context, workspaceID uuid.UUID, payload CredentialPayload) (*metadata.CredentialEnvelope, error) {
	secretRef := uuid.New()
	secretVersion := 1

	ver, kekKey, err := m.kek.ActiveKEK()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInternalError, err)
	}

	env, err := SealEnvelope(payload, workspaceID, secretRef, secretVersion, SuiteAES256GCMv1, ver, kekKey, rand.Reader)
	if err != nil {
		return nil, err
	}

	// Record wrapping
	if err := m.kek.RecordWrap(ver); err != nil {
		return nil, err
	}

	if err := m.store.CreateEnvelope(ctx, env); err != nil {
		return nil, err
	}

	return env, nil
}

// Resolve 解析指定版本的凭证。
// 普通执行路径：retired_at 非空时返回 credential_retired。
func (m *LifecycleManager) Resolve(ctx context.Context, workspaceID, secretRef uuid.UUID, secretVersion int) (CredentialPayload, error) {
	env, err := m.store.EnvelopeByRef(ctx, workspaceID, secretRef, secretVersion)
	if err != nil {
		return CredentialPayload{}, fmt.Errorf("%w: %v", ErrCredentialNotFound, err)
	}

	if env.RetiredAt != nil {
		return CredentialPayload{}, fmt.Errorf("%w: version %d retired", ErrCredentialRetired, secretVersion)
	}

	kek, err := m.kek.GetKEK(env.KEKVersion)
	if err != nil {
		return CredentialPayload{}, fmt.Errorf("%w: %v", ErrUnknownKEKVersion, err)
	}

	return OpenEnvelope(env, workspaceID, secretRef, kek)
}

// Rotate 轮换凭证：在事务中创建新版本并更新所有连接引用。
func (m *LifecycleManager) Rotate(ctx context.Context, workspaceID, secretRef uuid.UUID, expectedVersion int, newPayload CredentialPayload) (*metadata.CredentialEnvelope, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: begin tx: %v", ErrInternalError, err)
	}
	defer tx.Rollback()

	// 1. 锁定 Envelope 行
	env, err := m.store.LockEnvelopeForUpdate(ctx, tx, workspaceID, secretRef)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCredentialNotFound, err)
	}

	// 2. expected_version 检查
	if env.Version != expectedVersion {
		return nil, fmt.Errorf("%w: expected %d, actual %d", ErrVersionConflict, expectedVersion, env.Version)
	}

	// 3. 新版本号
	newVersion := env.Version + 1

	// 4. 使用 active KEK 加密
	ver, kekKey, err := m.kek.ActiveKEK()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInternalError, err)
	}

	newEnv, err := SealEnvelope(newPayload, workspaceID, secretRef, newVersion, SuiteAES256GCMv1, ver, kekKey, rand.Reader)
	if err != nil {
		return nil, err
	}

	if err := m.kek.RecordWrap(ver); err != nil {
		return nil, err
	}

	// 5. INSERT 新版本
	if err := m.store.InsertEnvelopeTx(ctx, tx, newEnv); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInternalError, err)
	}

	// 6. UPDATE connections
	if err := m.conns.UpdateConnectionVersion(ctx, tx, workspaceID, secretRef, newVersion); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInternalError, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("%w: commit: %v", ErrInternalError, err)
	}

	return newEnv, nil
}

// Retire 退役指定版本。被连接引用时拒绝。
func (m *LifecycleManager) Retire(ctx context.Context, workspaceID, secretRef uuid.UUID, version int) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin tx: %v", ErrInternalError, err)
	}
	defer tx.Rollback()

	// 1. 锁定目标版本
	env, err := m.store.LockEnvelopeVersion(ctx, tx, workspaceID, secretRef, version)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCredentialNotFound, err)
	}

	if env.RetiredAt != nil {
		// 幂等：已退役
		return tx.Commit()
	}

	// 2. 检查引用
	count, err := m.conns.CountConnectionsByVersion(ctx, tx, workspaceID, secretRef, version)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInternalError, err)
	}
	if count > 0 {
		return fmt.Errorf("%w: %d connections still reference version %d", ErrCredentialInUse, count, version)
	}

	// 3. 设置 retired_at
	if err := m.store.UpdateRetiredAt(ctx, tx, workspaceID, secretRef, version); err != nil {
		return fmt.Errorf("%w: %v", ErrInternalError, err)
	}

	return tx.Commit()
}

// ---- 与 execution/adapter 的集成接口 ------------------------------------------

// CredentialResolver 为执行管线提供凭证解析能力。
type CredentialResolver interface {
	ResolveCredential(ctx context.Context, workspaceID, secretRef uuid.UUID, secretVersion int) (CredentialPayload, error)
}

// ResolveCredential 实现 CredentialResolver 接口。
func (m *LifecycleManager) ResolveCredential(ctx context.Context, workspaceID, secretRef uuid.UUID, secretVersion int) (CredentialPayload, error) {
	return m.Resolve(ctx, workspaceID, secretRef, secretVersion)
}

// 编译时检查
var _ CredentialResolver = (*LifecycleManager)(nil)
