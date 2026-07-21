package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PGStore 实现所有 P0 元数据仓储接口的 PostgreSQL 存储。
type PGStore struct {
	DB *sql.DB
}

// NewPGStore 创建 PostgreSQL 仓储。
func NewPGStore(db *sql.DB) *PGStore {
	return &PGStore{DB: db}
}

// ---- UserStore ------------------------------------------------------------

func (s *PGStore) CreateUser(ctx context.Context, u *User) error {
	const q = `
		INSERT INTO users (email, password_hash, status, identity_provider, external_subject, external_tenant)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, status, created_at, updated_at`
	st := string(u.Status)
	if st == "" {
		st = string(UserStatusActive)
	}
	if err := s.DB.QueryRowContext(ctx, q,
		u.Email, u.PasswordHash, st,
		u.IdentityProvider, u.ExternalSubject, u.ExternalTenant,
	).Scan(&u.ID, &u.Status, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return err
	}
	return nil
}

func (s *PGStore) UserByID(ctx context.Context, id uuid.UUID) (*User, error) {
	const q = `SELECT id, email, password_hash, status,
		identity_provider, external_subject, external_tenant,
		created_at, updated_at FROM users WHERE id = $1`
	u := &User{}
	err := s.DB.QueryRowContext(ctx, q, id).Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.Status,
		&u.IdentityProvider, &u.ExternalSubject, &u.ExternalTenant,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("user %s: %w", id, err)
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *PGStore) UserByEmail(ctx context.Context, email string) (*User, error) {
	const q = `SELECT id, email, password_hash, status,
		identity_provider, external_subject, external_tenant,
		created_at, updated_at FROM users WHERE lower(email) = lower($1)`
	u := &User{}
	err := s.DB.QueryRowContext(ctx, q, email).Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.Status,
		&u.IdentityProvider, &u.ExternalSubject, &u.ExternalTenant,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("email %s: %w", email, err)
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *PGStore) ListUsers(ctx context.Context, status UserStatus, limit, offset int) ([]User, error) {
	q := `SELECT id, email, password_hash, status,
		identity_provider, external_subject, external_tenant,
		created_at, updated_at FROM users`
	args := []any{}
	argN := 1
	if status != "" {
		q += fmt.Sprintf(" WHERE status = $%d", argN)
		args = append(args, string(status))
		argN++
	}
	q += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argN, argN+1)
	args = append(args, limit, offset)

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(
			&u.ID, &u.Email, &u.PasswordHash, &u.Status,
			&u.IdentityProvider, &u.ExternalSubject, &u.ExternalTenant,
			&u.CreatedAt, &u.UpdatedAt,
		); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *PGStore) UpdateUser(ctx context.Context, u *User) error {
	const q = `UPDATE users SET email=$1, password_hash=$2, status=$3, updated_at=now()
		WHERE id=$4`
	_, err := s.DB.ExecContext(ctx, q, u.Email, u.PasswordHash, string(u.Status), u.ID)
	return err
}

// ---- WorkspaceStore -------------------------------------------------------

func (s *PGStore) CreateWorkspace(ctx context.Context, ws *Workspace) error {
	const q = `INSERT INTO workspaces (name, settings) VALUES ($1, $2)
		RETURNING id, created_at, updated_at`
	settings := ws.Settings
	if settings == nil {
		settings = json.RawMessage("{}")
	}
	return s.DB.QueryRowContext(ctx, q, ws.Name, settings).Scan(&ws.ID, &ws.CreatedAt, &ws.UpdatedAt)
}

func (s *PGStore) WorkspaceByID(ctx context.Context, id uuid.UUID) (*Workspace, error) {
	const q = `SELECT id, name, settings, created_at, updated_at FROM workspaces WHERE id = $1`
	ws := &Workspace{}
	err := s.DB.QueryRowContext(ctx, q, id).Scan(
		&ws.ID, &ws.Name, &ws.Settings, &ws.CreatedAt, &ws.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("workspace %s: %w", id, err)
	}
	if err != nil {
		return nil, err
	}
	return ws, nil
}

func (s *PGStore) ListWorkspaces(ctx context.Context, limit, offset int) ([]Workspace, error) {
	q := `SELECT id, name, settings, created_at, updated_at FROM workspaces
		ORDER BY created_at DESC LIMIT $1 OFFSET $2`
	rows, err := s.DB.QueryContext(ctx, q, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var wss []Workspace
	for rows.Next() {
		var ws Workspace
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Settings, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
			return nil, err
		}
		wss = append(wss, ws)
	}
	return wss, rows.Err()
}

// ---- WorkspaceMemberStore -------------------------------------------------

func (s *PGStore) AddMember(ctx context.Context, m *WorkspaceMember) error {
	const q = `INSERT INTO workspace_members (workspace_id, user_id, role)
		VALUES ($1, $2, $3) RETURNING created_at`
	return s.DB.QueryRowContext(ctx, q, m.WorkspaceID, m.UserID, string(m.Role)).Scan(&m.CreatedAt)
}

func (s *PGStore) MemberByWorkspaceAndUser(ctx context.Context, wsID, userID uuid.UUID) (*WorkspaceMember, error) {
	const q = `SELECT workspace_id, user_id, role, created_at FROM workspace_members
		WHERE workspace_id = $1 AND user_id = $2`
	m := &WorkspaceMember{}
	err := s.DB.QueryRowContext(ctx, q, wsID, userID).Scan(
		&m.WorkspaceID, &m.UserID, &m.Role, &m.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("member (%s, %s): %w", wsID, userID, err)
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (s *PGStore) ListMembers(ctx context.Context, wsID uuid.UUID) ([]WorkspaceMember, error) {
	const q = `SELECT workspace_id, user_id, role, created_at FROM workspace_members
		WHERE workspace_id = $1 ORDER BY created_at ASC`
	rows, err := s.DB.QueryContext(ctx, q, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []WorkspaceMember
	for rows.Next() {
		var m WorkspaceMember
		if err := rows.Scan(&m.WorkspaceID, &m.UserID, &m.Role, &m.CreatedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (s *PGStore) RemoveMember(ctx context.Context, wsID, userID uuid.UUID) error {
	const q = `DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`
	_, err := s.DB.ExecContext(ctx, q, wsID, userID)
	return err
}

// ---- CredentialEnvelopeStore ----------------------------------------------

func (s *PGStore) CreateEnvelope(ctx context.Context, env *CredentialEnvelope) error {
	const q = `
		INSERT INTO credential_envelopes
			(workspace_id, secret_ref, version, ciphertext, data_nonce,
			 wrapped_dek, wrap_nonce, envelope_suite, kek_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING created_at`
	return s.DB.QueryRowContext(ctx, q,
		env.WorkspaceID, env.SecretRef, env.Version,
		env.Ciphertext, env.DataNonce, env.WrappedDEK, env.WrapNonce,
		env.EnvelopeSuite, env.KEKVersion,
	).Scan(&env.CreatedAt)
}

func (s *PGStore) EnvelopeByRef(ctx context.Context, wsID, secretRef uuid.UUID, version int) (*CredentialEnvelope, error) {
	const q = `SELECT workspace_id, secret_ref, version, ciphertext, data_nonce,
		wrapped_dek, wrap_nonce, envelope_suite, kek_version,
		created_at, retired_at FROM credential_envelopes
		WHERE workspace_id = $1 AND secret_ref = $2 AND version = $3`
	env := &CredentialEnvelope{}
	err := s.DB.QueryRowContext(ctx, q, wsID, secretRef, version).Scan(
		&env.WorkspaceID, &env.SecretRef, &env.Version,
		&env.Ciphertext, &env.DataNonce, &env.WrappedDEK, &env.WrapNonce,
		&env.EnvelopeSuite, &env.KEKVersion,
		&env.CreatedAt, &env.RetiredAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("credential envelope (%s, %s, %d): %w", wsID, secretRef, version, err)
	}
	if err != nil {
		return nil, err
	}
	return env, nil
}

func (s *PGStore) ListEnvelopes(ctx context.Context, wsID uuid.UUID) ([]CredentialEnvelope, error) {
	const q = `SELECT workspace_id, secret_ref, version, envelope_suite, kek_version,
		created_at, retired_at FROM credential_envelopes
		WHERE workspace_id = $1 ORDER BY created_at DESC`
	rows, err := s.DB.QueryContext(ctx, q, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var envs []CredentialEnvelope
	for rows.Next() {
		var env CredentialEnvelope
		if err := rows.Scan(&env.WorkspaceID, &env.SecretRef, &env.Version,
			&env.EnvelopeSuite, &env.KEKVersion, &env.CreatedAt, &env.RetiredAt,
		); err != nil {
			return nil, err
		}
		envs = append(envs, env)
	}
	return envs, rows.Err()
}

// ---- ConnectionStore ------------------------------------------------------

func (s *PGStore) CreateConnection(ctx context.Context, c *Connection) error {
	const q = `
		INSERT INTO connections
			(workspace_id, name, engine, host, port, database, environment,
			 secret_ref, secret_version, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id, created_at, updated_at`
	return s.DB.QueryRowContext(ctx, q,
		c.WorkspaceID, c.Name, string(c.Engine),
		c.Host, c.Port, c.Database, string(c.Environment),
		c.SecretRef, c.SecretVersion, c.CreatedBy,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
}

func (s *PGStore) ConnectionByID(ctx context.Context, wsID, id uuid.UUID) (*Connection, error) {
	const q = `SELECT id, workspace_id, name, engine, host, port, database,
		environment, secret_ref, secret_version, created_by,
		created_at, updated_at FROM connections WHERE id = $1 AND workspace_id = $2`
	c := &Connection{}
	err := s.DB.QueryRowContext(ctx, q, id, wsID).Scan(
		&c.ID, &c.WorkspaceID, &c.Name, &c.Engine,
		&c.Host, &c.Port, &c.Database, &c.Environment,
		&c.SecretRef, &c.SecretVersion, &c.CreatedBy,
		&c.CreatedAt, &c.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("connection %s: %w", id, err)
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *PGStore) ListConnections(ctx context.Context, wsID uuid.UUID) ([]Connection, error) {
	const q = `SELECT id, workspace_id, name, engine, host, port, database,
		environment, secret_ref, secret_version, created_by,
		created_at, updated_at FROM connections
		WHERE workspace_id = $1 ORDER BY created_at DESC`
	rows, err := s.DB.QueryContext(ctx, q, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var conns []Connection
	for rows.Next() {
		var c Connection
		if err := rows.Scan(
			&c.ID, &c.WorkspaceID, &c.Name, &c.Engine,
			&c.Host, &c.Port, &c.Database, &c.Environment,
			&c.SecretRef, &c.SecretVersion, &c.CreatedBy,
			&c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		conns = append(conns, c)
	}
	return conns, rows.Err()
}

func (s *PGStore) UpdateConnection(ctx context.Context, wsID uuid.UUID, c *Connection) error {
	const q = `UPDATE connections SET name=$1, engine=$2, host=$3, port=$4,
		database=$5, environment=$6, secret_ref=$7, secret_version=$8, updated_at=now()
		WHERE id=$9 AND workspace_id=$10`
	res, err := s.DB.ExecContext(ctx, q,
		c.Name, string(c.Engine), c.Host, c.Port, c.Database,
		string(c.Environment), c.SecretRef, c.SecretVersion, c.ID, wsID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("connection %s not found in workspace %s", c.ID, wsID)
	}
	return nil
}

// ---- ConnectionPolicyStore ------------------------------------------------

// CreatePolicy 创建连接策略，同一连接已有策略时显式拒绝（主键冲突）。
func (s *PGStore) CreatePolicy(ctx context.Context, p *ConnectionPolicy) error {
	// nil bool 在 INSERT 时使用 COALESCE 取默认值（allow_read=true, 其余=false），
	// 在 UPDATE 冲突时使用 COALESCE(EXCLUDED, connection_policies) 保留现有值。
	const q = `
		INSERT INTO connection_policies
			(workspace_id, connection_id, allow_read, allow_write, allow_export,
			 statement_timeout_ms, max_rows)
		VALUES ($1, $2, COALESCE($3, true), COALESCE($4, false), COALESCE($5, false), $6, $7)
		RETURNING created_at, updated_at`
	return s.DB.QueryRowContext(ctx, q,
		p.WorkspaceID, p.ConnectionID,
		p.AllowRead, p.AllowWrite, p.AllowExport,
		p.StatementTimeoutMs, p.MaxRows,
	).Scan(&p.CreatedAt, &p.UpdatedAt)
}

// UpdatePolicy 更新已有策略，nil bool 保留现有值，无匹配行时报错。
func (s *PGStore) UpdatePolicy(ctx context.Context, p *ConnectionPolicy) error {
	const q = `
		UPDATE connection_policies SET
			allow_read = COALESCE($3, allow_read),
			allow_write = COALESCE($4, allow_write),
			allow_export = COALESCE($5, allow_export),
			statement_timeout_ms = $6,
			max_rows = $7,
			updated_at = now()
		WHERE workspace_id = $1 AND connection_id = $2
		RETURNING created_at, updated_at`
	err := s.DB.QueryRowContext(ctx, q,
		p.WorkspaceID, p.ConnectionID,
		p.AllowRead, p.AllowWrite, p.AllowExport,
		p.StatementTimeoutMs, p.MaxRows,
	).Scan(&p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return fmt.Errorf("policy not found for connection %s in workspace %s", p.ConnectionID, p.WorkspaceID)
	}
	return err
}

// PolicyByConnection 返回 nil 表示未配置策略（调用方应默认拒绝）。
func (s *PGStore) PolicyByConnection(ctx context.Context, wsID, connID uuid.UUID) (*ConnectionPolicy, error) {
	const q = `SELECT workspace_id, connection_id, allow_read, allow_write, allow_export,
		statement_timeout_ms, max_rows, created_at, updated_at
		FROM connection_policies WHERE workspace_id = $1 AND connection_id = $2`
	p := &ConnectionPolicy{}
	var allowRead, allowWrite, allowExport sql.NullBool
	err := s.DB.QueryRowContext(ctx, q, wsID, connID).Scan(
		&p.WorkspaceID, &p.ConnectionID,
		&allowRead, &allowWrite, &allowExport,
		&p.StatementTimeoutMs, &p.MaxRows,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil // 缺失策略 → 调用方默认拒绝
	}
	if err != nil {
		return nil, err
	}
	if allowRead.Valid {
		p.AllowRead = &allowRead.Bool
	}
	if allowWrite.Valid {
		p.AllowWrite = &allowWrite.Bool
	}
	if allowExport.Valid {
		p.AllowExport = &allowExport.Bool
	}
	return p, nil
}

// ---- ExecutionStore -------------------------------------------------------

func (s *PGStore) CreateExecution(ctx context.Context, e *Execution) error {
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
	// ADR-010: result_ref 非空且无过期时间时，默认 7 天后过期
	expiry := e.ResultExpiresAt
	if e.ResultRef != nil && *e.ResultRef != "" && expiry == nil {
		t := time.Now().UTC().Add(7 * 24 * time.Hour)
		expiry = &t
	}
	e.ResultExpiresAt = expiry
	if err := s.DB.QueryRowContext(ctx, q,
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

func (s *PGStore) ExecutionByID(ctx context.Context, wsID, id uuid.UUID) (*Execution, error) {
	const q = `SELECT id, workspace_id, connection_id, actor_id,
		document_id, query_version_id, statement_hash, status, trace_id,
		started_at, finished_at, duration_ms, row_count,
		result_ref, result_expires_at, error_code, created_at
		FROM executions WHERE id = $1 AND workspace_id = $2`
	e := &Execution{}
	err := s.DB.QueryRowContext(ctx, q, id, wsID).Scan(
		&e.ID, &e.WorkspaceID, &e.ConnectionID, &e.ActorID,
		&e.DocumentID, &e.QueryVersionID,
		&e.StatementHash, &e.Status, &e.TraceID,
		&e.StartedAt, &e.FinishedAt, &e.DurationMs, &e.RowCount,
		&e.ResultRef, &e.ResultExpiresAt,
		&e.ErrorCode, &e.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("execution %s: %w", id, err)
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (s *PGStore) ExecutionByTraceID(ctx context.Context, wsID uuid.UUID, traceID string) (*Execution, error) {
	const q = `SELECT id, workspace_id, connection_id, actor_id,
		document_id, query_version_id, statement_hash, status, trace_id,
		started_at, finished_at, duration_ms, row_count,
		result_ref, result_expires_at, error_code, created_at
		FROM executions WHERE trace_id = $1 AND workspace_id = $2 ORDER BY started_at DESC LIMIT 1`
	e := &Execution{}
	err := s.DB.QueryRowContext(ctx, q, traceID, wsID).Scan(
		&e.ID, &e.WorkspaceID, &e.ConnectionID, &e.ActorID,
		&e.DocumentID, &e.QueryVersionID,
		&e.StatementHash, &e.Status, &e.TraceID,
		&e.StartedAt, &e.FinishedAt, &e.DurationMs, &e.RowCount,
		&e.ResultRef, &e.ResultExpiresAt,
		&e.ErrorCode, &e.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("execution trace %s: %w", traceID, err)
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (s *PGStore) UpdateExecution(ctx context.Context, wsID uuid.UUID, e *Execution) error {
	// ADR-010: result_ref 非空且无过期时间时，默认 7 天后过期
	expiry := e.ResultExpiresAt
	if e.ResultRef != nil && *e.ResultRef != "" && expiry == nil {
		t := time.Now().UTC().Add(7 * 24 * time.Hour)
		expiry = &t
	}
	e.ResultExpiresAt = expiry
	const q = `UPDATE executions SET status=$1, finished_at=$2, duration_ms=$3,
		row_count=$4, result_ref=$5, result_expires_at=$6,
		error_code=$7
		WHERE id=$8 AND workspace_id=$9`
	res, err := s.DB.ExecContext(ctx, q,
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
	return nil
}

// ---- AuditEventStore ------------------------------------------------------

// sanitizeAuditMetadata 按允许列表过滤审计元数据，移除 SQL 正文、凭证、KEK、
// 目标库结果、原始错误等敏感字段。ADR-013: 审计 metadata 必须是脱敏对象。
func sanitizeAuditMetadata(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}

	// 拒绝 JSON null 和非对象类型（交由 DB CHECK 约束拒绝）
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return raw
	}

	// P0 允许列表：仅保留明确安全的键，且值必须为 string/float64/bool
	allowed := map[string]bool{
		"summary":       true,
		"rows_affected": true,
		"cached":        true,
	}

	filtered := make(map[string]any, len(m))
	for k, v := range m {
		if !allowed[k] {
			continue
		}
		// 仅允许安全的标量类型（string、float64、bool），字符串截断防 SQL 泄露
		switch val := v.(type) {
		case string:
			if len(val) > 500 {
				val = val[:500]
			}
			if looksLikeSQL(val) || looksLikeCredential(val) {
				filtered[k] = "[redacted: sensitive content]"
			} else {
				filtered[k] = val
			}
		case float64, bool:
			filtered[k] = val
		}
	}

	result, err := json.Marshal(filtered)
	if err != nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(result)
}

// AppendAudit 追加一条审计事件。数据库触发器阻止 UPDATE/DELETE/TRUNCATE。
// 写入前按允许列表脱敏 metadata（ADR-013），并拒绝零值时间戳。
func (s *PGStore) AppendAudit(ctx context.Context, e *AuditEvent) error {
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("audit: occurred_at 不得为零值")
	}
	const q = `
		INSERT INTO audit_events
			(workspace_id, actor_type, actor_id, connection_id,
			 action, resource_type, resource_id, outcome,
			 metadata, trace_id, execution_id, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id, created_at`
	md := sanitizeAuditMetadata(e.Metadata)
	e.Metadata = md
	return s.DB.QueryRowContext(ctx, q,
		e.WorkspaceID, string(e.ActorType), e.ActorID, e.ConnectionID,
		e.Action, e.ResourceType, e.ResourceID, string(e.Outcome),
		md, e.TraceID, e.ExecutionID, e.OccurredAt,
	).Scan(&e.ID, &e.CreatedAt)
}

// QueryAudit 按工作区和可选条件查询审计事件。不含 update/delete 方法。
func (s *PGStore) QueryAudit(ctx context.Context, q AuditQuery) ([]AuditEvent, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 1000 {
		q.Limit = 1000
	}
	query := `SELECT id, workspace_id, actor_type, actor_id, connection_id,
		action, resource_type, resource_id, outcome,
		metadata, trace_id, execution_id, occurred_at, created_at
		FROM audit_events WHERE workspace_id = $1`
	args := []any{q.WorkspaceID}
	argN := 2

	if q.ConnectionID != nil {
		query += fmt.Sprintf(" AND connection_id = $%d", argN)
		args = append(args, *q.ConnectionID)
		argN++
	}
	if q.Action != nil {
		query += fmt.Sprintf(" AND action = $%d", argN)
		args = append(args, *q.Action)
		argN++
	}
	if q.ResourceType != nil {
		query += fmt.Sprintf(" AND resource_type = $%d", argN)
		args = append(args, *q.ResourceType)
		argN++
	}
	if q.From != nil {
		query += fmt.Sprintf(" AND occurred_at >= $%d", argN)
		args = append(args, *q.From)
		argN++
	}
	if q.To != nil {
		query += fmt.Sprintf(" AND occurred_at <= $%d", argN)
		args = append(args, *q.To)
		argN++
	}

	query += fmt.Sprintf(" ORDER BY occurred_at DESC LIMIT $%d OFFSET $%d", argN, argN+1)
	args = append(args, q.Limit, q.Offset)

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(
			&e.ID, &e.WorkspaceID, &e.ActorType, &e.ActorID, &e.ConnectionID,
			&e.Action, &e.ResourceType, &e.ResourceID, &e.Outcome,
			&e.Metadata, &e.TraceID, &e.ExecutionID,
			&e.OccurredAt, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// Compile-time interface checks
var (
	_ UserStore               = (*PGStore)(nil)
	_ WorkspaceStore          = (*PGStore)(nil)
	_ WorkspaceMemberStore    = (*PGStore)(nil)
	_ CredentialEnvelopeStore = (*PGStore)(nil)
	_ ConnectionStore         = (*PGStore)(nil)
	_ ConnectionPolicyStore   = (*PGStore)(nil)
	_ ExecutionStore          = (*PGStore)(nil)
	_ AuditEventStore         = (*PGStore)(nil)
)

// sqlKeywordRE 匹配 SQL DML/DDL 关键词（忽略大小写，后跟空白、注释或换行）
var sqlKeywordRE = regexp.MustCompile(`(?i)\b(SELECT|INSERT|UPDATE|DELETE|DROP|ALTER|CREATE|TRUNCATE|EXEC(UTE)?)\b`)

// looksLikeSQL 检测字符串是否包含 SQL 语句特征（P0 审计脱敏辅助）。
func looksLikeSQL(s string) bool {
	return sqlKeywordRE.MatchString(s)
}

// credPatternRE 匹配 DSN URL 凭证和 Bearer token（P0 审计脱敏辅助）
var credPatternRE = regexp.MustCompile(`(?i)(://[^@\s]+@|bearer\s+[\w-]+)`)

// looksLikeCredential 检测字符串是否包含凭证模式（P0 审计脱敏辅助）。
func looksLikeCredential(s string) bool {
	if credPatternRE.MatchString(s) {
		return true
	}
	for _, pattern := range []string{"password", "secret", "token", "key=", "pass="} {
		if strings.Contains(strings.ToLower(s), pattern) {
			return true
		}
	}
	return false
}
