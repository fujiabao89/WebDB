package metadata

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

// ErrEnvelopeNotFound 凭证 Envelope 未找到的 sentinel 错误。
var ErrEnvelopeNotFound = errors.New("credential envelope not found")

// CredentialTXStore 定义凭证生命周期所需的带事务仓储操作。
type CredentialTXStore interface {
	CredentialEnvelopeStore
	// LockEnvelopeForUpdate 锁定 Envelope 行用于轮换/退役。
	LockEnvelopeForUpdate(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID) (*CredentialEnvelope, error)
	// LockEnvelopeVersion 锁定指定版本 Envelope。
	LockEnvelopeVersion(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID, version int) (*CredentialEnvelope, error)
	// InsertEnvelopeTx 在事务中插入 Envelope。
	InsertEnvelopeTx(ctx context.Context, tx *sql.Tx, env *CredentialEnvelope) error
	// ListEnvelopesByRef 按 secret_ref 列出所有版本（含已退役）。
	ListEnvelopesByRef(ctx context.Context, wsID, secretRef uuid.UUID) ([]CredentialEnvelope, error)
	// UpdateRetiredAt 在事务中设置 retired_at。
	UpdateRetiredAt(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID, version int) error
}

// ConnectionTXStore 定义连接引用的批量更新操作。
type ConnectionTXStore interface {
	ConnectionStore
	// UpdateConnectionVersion 在事务中更新连接引用的 secret_version。
	UpdateConnectionVersion(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID, newVersion int) error
	// CountConnectionsByVersion 在事务中统计引用指定版本的连接数。
	CountConnectionsByVersion(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID, version int) (int, error)
}
