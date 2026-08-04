package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MetadataTx 元数据库事务：execution 创建/状态更新 + 审计追加在同一事务中原子提交。
// proposal §9.1（Owner D11 批准）：Execution 状态更新与 AuditEvent append 原子提交。
type MetadataTx interface {
	CreateExecution(ctx context.Context, e *Execution) error
	UpdateExecution(ctx context.Context, wsID uuid.UUID, e *Execution) error
	AppendAudit(ctx context.Context, e *AuditEvent) error
	Commit() error
	Rollback() error
}

// TxStore 开启元数据库事务。
type TxStore interface {
	Begin(ctx context.Context) (MetadataTx, error)
}

// pgMetadataTx 基于 *sql.Tx 的 MetadataTx 实现。
type pgMetadataTx struct {
	tx *sql.Tx
}

// Begin 开启一个元数据库事务。
func (s *PGStore) Begin(ctx context.Context) (MetadataTx, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin metadata tx: %w", err)
	}
	return &pgMetadataTx{tx: tx}, nil
}

// CreateExecution 在事务中创建 execution。
func (t *pgMetadataTx) CreateExecution(ctx context.Context, e *Execution) error {
	const q = `
		INSERT INTO executions
			(workspace_id, connection_id, actor_id, document_id, query_version_id,
			 statement_hash, status, trace_id, result_ref, result_expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id, started_at, created_at`
	st := string(e.Status)
	if st == "" {
		st = string(ExecStatusPending)
	}
	// ADR-010: result_ref 非空且无过期时间时，默认 7 天后过期。
	// 与 PGStore.CreateExecution 保持一致，回写 e.ResultExpiresAt（CodeRabbit #23）。
	expiry := e.ResultExpiresAt
	if e.ResultRef != nil && *e.ResultRef != "" && expiry == nil {
		tp := time.Now().UTC().Add(7 * 24 * time.Hour)
		expiry = &tp
	}
	e.ResultExpiresAt = expiry
	if err := t.tx.QueryRowContext(ctx, q,
		e.WorkspaceID, e.ConnectionID, e.ActorID,
		e.DocumentID, e.QueryVersionID,
		e.StatementHash, st, e.TraceID,
		e.ResultRef, expiry,
	).Scan(&e.ID, &e.StartedAt, &e.CreatedAt); err != nil {
		return err
	}
	e.Status = ExecutionStatus(st)
	return nil
}

// UpdateExecution 在事务中更新 execution 状态。
// 成功后回写 e.ResultExpiresAt，使内存对象与数据库行一致（VuXZi）。
func (t *pgMetadataTx) UpdateExecution(ctx context.Context, wsID uuid.UUID, e *Execution) error {
	expiry := e.ResultExpiresAt
	if e.ResultRef != nil && *e.ResultRef != "" && expiry == nil {
		tp := time.Now().UTC().Add(7 * 24 * time.Hour)
		expiry = &tp
	}
	const q = `UPDATE executions SET status=$1, finished_at=$2, duration_ms=$3,
		row_count=$4, result_ref=$5, result_expires_at=$6,
		error_code=$7
		WHERE id=$8 AND workspace_id=$9`
	res, err := t.tx.ExecContext(ctx, q,
		string(e.Status), e.FinishedAt, e.DurationMs, e.RowCount,
		e.ResultRef, expiry, e.ErrorCode,
		e.ID, wsID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("execution %s not found in workspace", e.ID)
	}
	e.ResultExpiresAt = expiry
	return nil
}

// AppendAudit 在事务中追加审计事件。fail-closed：metadata 必须符合事件 schema。
// 与 atomic wrapper（pgCredentialAtomicTx/pgConnectionAtomicTx）共享 insertAuditEvent。
func (t *pgMetadataTx) AppendAudit(ctx context.Context, e *AuditEvent) error {
	return insertAuditEvent(ctx, t.tx, e)
}

// Commit 提交事务。
func (t *pgMetadataTx) Commit() error {
	if err := t.tx.Commit(); err != nil {
		return fmt.Errorf("commit metadata tx: %w", err)
	}
	return nil
}

// Rollback 回滚事务。
func (t *pgMetadataTx) Rollback() error {
	if err := t.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("rollback metadata tx: %w", err)
	}
	return nil
}

// Compile-time checks
var _ TxStore = (*PGStore)(nil)
var _ MetadataTx = (*pgMetadataTx)(nil)
