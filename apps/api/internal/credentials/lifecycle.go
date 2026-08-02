package credentials

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// LifecycleManager 管理凭证创建、解析、轮换和退役。
type LifecycleManager struct {
	store  metadata.CredentialTXStore
	conns  metadata.ConnectionTXStore
	kek    KEKProvider
	db     *sql.DB
	logger *slog.Logger
}

// NewLifecycleManager 创建 LifecycleManager。
// db 用于开启事务。logger 默认 slog.Default()，供脱敏返回前记录根因（vpvC7）。
func NewLifecycleManager(db *sql.DB, store metadata.CredentialTXStore, conns metadata.ConnectionTXStore, kek KEKProvider) *LifecycleManager {
	return &LifecycleManager{store: store, conns: conns, kek: kek, db: db, logger: slog.Default()}
}

// logStorageFailure 记录底层存储/KEK 故障的根因（服务端日志，不含凭证/KEK/明文），
// 保持对外返回的稳定错误码脱敏（vpvC7）。
func (m *LifecycleManager) logStorageFailure(op string, workspaceID, secretRef uuid.UUID, err error) {
	m.logger.Error("credential lifecycle failure",
		"op", op, "workspace_id", workspaceID.String(), "secret_ref", secretRef.String(),
		"error", err)
}

// Create 创建新凭证并返回 Envelope。
func (m *LifecycleManager) Create(ctx context.Context, workspaceID uuid.UUID, payload CredentialPayload) (*metadata.CredentialEnvelope, error) {
	secretRef := uuid.New()
	secretVersion := 1

	ver, kekKey, err := m.kek.ActiveKEK()
	if err != nil {
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	if err := m.kek.ReserveWrap(ver); err != nil {
		return nil, err
	}

	env, err := SealEnvelope(payload, workspaceID, secretRef, secretVersion, SuiteAES256GCMv1, ver, kekKey, rand.Reader)
	if err != nil {
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
		if errors.Is(err, metadata.ErrEnvelopeNotFound) {
			return CredentialPayload{}, fmt.Errorf("%w: credential not found", ErrCredentialNotFound)
		}
		// 根因仅进服务端日志，返回保持脱敏（vpvC7）。
		m.logStorageFailure("resolve.envelope_by_ref", workspaceID, secretRef, err)
		return CredentialPayload{}, fmt.Errorf("%w: internal failure", ErrInternalError)
	}

	if env.RetiredAt != nil {
		return CredentialPayload{}, fmt.Errorf("%w: version %d retired", ErrCredentialRetired, secretVersion)
	}

	kek, err := m.kek.GetKEK(env.KEKVersion)
	if err != nil {
		// 携带 KEK 版本号，供 E16 审计记录 kek_version（Codex P1）。
		// 不把底层 KEK 提供器错误文本拼进错误链（Qodo #1），根因仅进服务端日志（vpvC7）。
		m.logStorageFailure("resolve.get_kek", workspaceID, secretRef, err)
		return CredentialPayload{}, &KEKVersionError{
			Version: env.KEKVersion,
			err:     fmt.Errorf("%w: unknown kek version", ErrUnknownKEKVersion),
		}
	}

	return OpenEnvelope(env, workspaceID, secretRef, kek)
}

// Rotate 轮换凭证：在事务中创建新版本并更新所有连接引用。
func (m *LifecycleManager) Rotate(ctx context.Context, workspaceID, secretRef uuid.UUID, expectedVersion int, newPayload CredentialPayload) (*metadata.CredentialEnvelope, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		m.logStorageFailure("rotate.begin_tx", workspaceID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	defer tx.Rollback()

	// 1. 锁定 Envelope 行
	env, err := m.store.LockEnvelopeForUpdate(ctx, tx, workspaceID, secretRef)
	if err != nil {
		if errors.Is(err, metadata.ErrEnvelopeNotFound) {
			return nil, fmt.Errorf("%w: credential not found", ErrCredentialNotFound)
		}
		m.logStorageFailure("rotate.lock_envelope", workspaceID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}

	// 2. expected_version 检查
	if env.Version != expectedVersion {
		// 携带 expected/actual 版本，供 E5 审计记录 actual_version（Qodo #4）。
		return nil, &VersionConflictError{
			Expected: expectedVersion,
			Actual:   env.Version,
			err:      fmt.Errorf("%w", ErrVersionConflict),
		}
	}

	// 2a. 最新版本已退役时拒绝轮换。
	if env.RetiredAt != nil {
		return nil, fmt.Errorf("%w: version %d retired", ErrCredentialRetired, env.Version)
	}

	// 3. 新版本号
	newVersion := env.Version + 1

	// 4. 使用 active KEK 加密
	ver, kekKey, err := m.kek.ActiveKEK()
	if err != nil {
		m.logStorageFailure("rotate.active_kek", workspaceID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	if err := m.kek.ReserveWrap(ver); err != nil {
		return nil, err
	}

	newEnv, err := SealEnvelope(newPayload, workspaceID, secretRef, newVersion, SuiteAES256GCMv1, ver, kekKey, rand.Reader)
	if err != nil {
		// payload 验证类错误（ErrInvalidPayload/ErrPayloadTooLarge）保留错误码，不降级。
		return nil, err
	}

	// 5. INSERT 新版本
	if err := m.store.InsertEnvelopeTx(ctx, tx, newEnv); err != nil {
		m.logStorageFailure("rotate.insert_envelope", workspaceID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}

	// 6. UPDATE connections
	if err := m.conns.UpdateConnectionVersion(ctx, tx, workspaceID, secretRef, newVersion); err != nil {
		m.logStorageFailure("rotate.update_connection_version", workspaceID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}

	if err := tx.Commit(); err != nil {
		m.logStorageFailure("rotate.commit", workspaceID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}

	return newEnv, nil
}

// Retire 退役指定版本。被连接引用时拒绝。
func (m *LifecycleManager) Retire(ctx context.Context, workspaceID, secretRef uuid.UUID, version int) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		m.logStorageFailure("retire.begin_tx", workspaceID, secretRef, err)
		return fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	defer tx.Rollback()

	// 1. 锁定目标版本
	env, err := m.store.LockEnvelopeVersion(ctx, tx, workspaceID, secretRef, version)
	if err != nil {
		if errors.Is(err, metadata.ErrEnvelopeNotFound) {
			return fmt.Errorf("%w: credential not found", ErrCredentialNotFound)
		}
		m.logStorageFailure("retire.lock_envelope_version", workspaceID, secretRef, err)
		return fmt.Errorf("%w: internal failure", ErrInternalError)
	}

	if env.RetiredAt != nil {
		// 幂等：已退役
		return tx.Commit()
	}

	// 2. 检查引用
	count, err := m.conns.CountConnectionsByVersion(ctx, tx, workspaceID, secretRef, version)
	if err != nil {
		m.logStorageFailure("retire.count_connections", workspaceID, secretRef, err)
		return fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	if count > 0 {
		return fmt.Errorf("%w: %d connections still reference version %d", ErrCredentialInUse, count, version)
	}

	// 3. 设置 retired_at
	if err := m.store.UpdateRetiredAt(ctx, tx, workspaceID, secretRef, version); err != nil {
		m.logStorageFailure("retire.update_retired_at", workspaceID, secretRef, err)
		return fmt.Errorf("%w: internal failure", ErrInternalError)
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
