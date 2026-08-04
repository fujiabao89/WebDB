package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ---- OperationContext ---------------------------------------------------------

// OperationContext 不可变操作上下文：标识一次原子化 mutation（唯一 mutation ID + 规范化
// ResourceID + 身份/结果/trace 标识）。字段私有，仅通过 NewOperationContext 构造、通过
// 只读访问器读取；构造时复制 ConnectionID（不保存调用方指针），Begin 后修改原始连接
// ID 或调用方上下文不会改变 AppendAudit/Commit 的校验。设计文档 WEB-25 §3。
type OperationContext struct {
	mutationID   string
	workspaceID  uuid.UUID
	resource     string // "credential" | "connection"
	resourceID   string // 规范化资源 id：secret_ref 或 connection_id 字符串
	action       string // create / rotate / retire / update
	connectionID *uuid.UUID
	actorID      uuid.UUID
	actorType    string
	outcome      string
	traceID      string
}

// NewOperationContext 构造并校验 OperationContext。
func NewOperationContext(
	mutationID string,
	wsID uuid.UUID,
	resource, resourceID, action string,
	connID *uuid.UUID,
	actorID uuid.UUID,
	actorType, outcome, traceID string,
) (*OperationContext, error) {
	c := &OperationContext{
		mutationID:  mutationID,
		workspaceID: wsID,
		resource:    resource,
		resourceID:  resourceID,
		action:      action,
		actorID:     actorID,
		actorType:   actorType,
		outcome:     outcome,
		traceID:     traceID,
	}
	if connID != nil {
		v := *connID // 复制值，不保存指针
		c.connectionID = &v
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate 对 OperationContext 做统一 fail-closed 校验。
func (c *OperationContext) Validate() error {
	if c == nil {
		return errors.New("operation context: nil")
	}
	if c.mutationID == "" {
		return errors.New("operation context: empty mutation id")
	}
	if c.workspaceID == uuid.Nil {
		return errors.New("operation context: zero workspace")
	}
	if c.resource != "credential" && c.resource != "connection" {
		return fmt.Errorf("operation context: invalid resource %q", c.resource)
	}
	if c.resourceID == "" {
		return errors.New("operation context: empty resource id")
	}
	if c.action == "" {
		return errors.New("operation context: empty action")
	}
	if c.actorType == "" {
		return errors.New("operation context: empty actor type")
	}
	if c.outcome == "" {
		return errors.New("operation context: empty outcome")
	}
	if c.resource == "connection" && (c.connectionID == nil || *c.connectionID == uuid.Nil) {
		return errors.New("operation context: connection-scoped but missing or zero connection id")
	}
	if c.actorID == uuid.Nil || c.traceID == "" {
		return errors.New("operation context: missing actor or trace")
	}
	return nil
}

// MutationID 返回唯一 mutation ID。
func (c *OperationContext) MutationID() string { return c.mutationID }

// WorkspaceID 返回工作区 ID。
func (c *OperationContext) WorkspaceID() uuid.UUID { return c.workspaceID }

// Resource 返回资源域（"credential" | "connection"）。
func (c *OperationContext) Resource() string { return c.resource }

// ResourceID 返回规范化资源 id。
func (c *OperationContext) ResourceID() string { return c.resourceID }

// Action 返回 action。
func (c *OperationContext) Action() string { return c.action }

// ConnectionID 返回防御性副本（value-and-presence），不暴露内部指针。
func (c *OperationContext) ConnectionID() (uuid.UUID, bool) {
	if c.connectionID == nil {
		return uuid.Nil, false
	}
	return *c.connectionID, true
}

// ActorID 返回 actor ID。
func (c *OperationContext) ActorID() uuid.UUID { return c.actorID }

// ActorType 返回 actor 类型。
func (c *OperationContext) ActorType() string { return c.actorType }

// Outcome 返回 outcome。
func (c *OperationContext) Outcome() string { return c.outcome }

// TraceID 返回 trace ID。
func (c *OperationContext) TraceID() string { return c.traceID }

// matchOperationContext 校验两个操作上下文在全部身份字段上完全一致
// （含 mutationID），用于 AppendAudit 的上下文绑定校验。
func matchOperationContext(a, b *OperationContext) bool {
	if a == nil || b == nil {
		return false
	}
	ac, aok := a.ConnectionID()
	bc, bok := b.ConnectionID()
	return a.mutationID == b.mutationID &&
		a.workspaceID == b.workspaceID &&
		a.resource == b.resource &&
		a.resourceID == b.resourceID &&
		a.action == b.action &&
		a.actorID == b.actorID &&
		a.actorType == b.actorType &&
		a.outcome == b.outcome &&
		a.traceID == b.traceID &&
		aok == bok && (!aok || ac == bc)
}

// ---- AuditMatchFields ----------------------------------------------------------

// auditMatchFields 是包内私有的精确匹配字段契约（数组，外部不可重赋值/修改）。
// AppendAudit、Commit 闸门与负向测试通过私有比较逻辑复用；跨包仅通过
// AuditMatchFields() 防御性副本访问。
var auditMatchFields = [...]string{
	"workspace_id", "resource", "resource_id", "action",
	"connection_id", "mutation_id", "actor_id", "actor_type",
	"outcome", "trace_id",
}

// AuditMatchFields 返回精确匹配字段的防御性副本。
func AuditMatchFields() []string {
	out := make([]string, len(auditMatchFields))
	copy(out, auditMatchFields[:])
	return out
}

// matchAuditEventOp 校验 AuditEvent 在审计允许的身份/结果字段上与 op 完全匹配。
// mutation_id 不落在 AuditEvent 模型（不新增 migration），由 wrapper 绑定 op 时校验
// （AppendAudit 必须携带同一 op，见 matchOperationContext）。
func matchAuditEventOp(op *OperationContext, e *AuditEvent) error {
	if op == nil {
		return errors.New("audit gate: nil operation context")
	}
	if e == nil {
		return errors.New("audit gate: nil audit event")
	}
	if e.WorkspaceID != op.WorkspaceID() {
		return errors.New("audit gate: workspace mismatch")
	}
	if string(e.ActorType) != op.ActorType() {
		return errors.New("audit gate: actor type mismatch")
	}
	if e.ActorID == nil || *e.ActorID != op.ActorID() {
		return errors.New("audit gate: actor mismatch")
	}
	if e.Action != op.Action() {
		return errors.New("audit gate: action mismatch")
	}
	if e.ResourceType != op.Resource() {
		return errors.New("audit gate: resource mismatch")
	}
	if e.ResourceID != op.ResourceID() {
		return errors.New("audit gate: resource id mismatch")
	}
	if e.Outcome != AuditOutcome(op.Outcome()) {
		return errors.New("audit gate: outcome mismatch")
	}
	if e.TraceID != op.TraceID() {
		return errors.New("audit gate: trace mismatch")
	}
	opConn, _ := op.ConnectionID()
	if op.Resource() == "connection" {
		if e.ConnectionID == nil || *e.ConnectionID != opConn {
			return errors.New("audit gate: connection mismatch")
		}
	} else {
		// credential-only：事件应为 nil connection（E3-E6 契约）。
		if e.ConnectionID != nil {
			return errors.New("audit gate: credential event must not carry connection")
		}
	}
	return nil
}

// ---- 窄事务接口 ---------------------------------------------------------------

// AuditTx 事务内审计追加（仅追加；租户/脱敏/operation context 契约见设计 §2.4）。
type AuditTx interface {
	AppendAudit(ctx context.Context, op *OperationContext, e *AuditEvent) error
}

// CredentialMutationTx 凭证生命周期 mutation（不暴露 Commit/Rollback）。
type CredentialMutationTx interface {
	LockEnvelopeForUpdate(ctx context.Context, wsID, secretRef uuid.UUID) (*CredentialEnvelope, error)
	LockEnvelopeVersion(ctx context.Context, wsID, secretRef uuid.UUID, version int) (*CredentialEnvelope, error)
	InsertEnvelope(ctx context.Context, env *CredentialEnvelope) error
	UpdateRetiredAt(ctx context.Context, wsID, secretRef uuid.UUID, version int) error
}

// ConnectionMutationTx 连接 mutation（不暴露 Commit/Rollback）。
type ConnectionMutationTx interface {
	CreateConnection(ctx context.Context, conn *Connection) error
	UpdateConnection(ctx context.Context, wsID uuid.UUID, conn *Connection) error
	UpdateConnectionVersion(ctx context.Context, wsID, secretRef uuid.UUID, newVersion int) error
	CountConnectionsByVersion(ctx context.Context, wsID, secretRef uuid.UUID, version int) (int, error)
}

// ConnectionRefReadTx Retire 引用检查所需的锁读能力（仅 CountConnectionsByVersion，FOR SHARE）。
type ConnectionRefReadTx interface {
	CountConnectionsByVersion(ctx context.Context, wsID, secretRef uuid.UUID, version int) (int, error)
}

// ConnectionVersionWriter 轮换所需的连接引用版本更新（窄写能力）。
// 设计澄清（WEB-25 实施）：proposal §6.2.3 Rotate 步骤 9 要求在同一事务内
// UPDATE connections.secret_version（LIFE-08 已确认原子性），因此 CredentialAtomicTx
// 需要此窄写能力；不暴露完整 ConnectionMutationTx（Create/Update 仍由 connections 域独占）。
type ConnectionVersionWriter interface {
	UpdateConnectionVersion(ctx context.Context, wsID, secretRef uuid.UUID, newVersion int) error
}

// CredentialAtomicTx credential 原子化组合。内部接口，仅由 credentials.LifecycleManager
// （审计协调器）使用；协调器独占 Begin/Commit/Rollback 与 mutation，强制成对执行
// mutation + AppendAudit。Commit 是审计完成闸门：当前事务内必须已追加有且仅有一个
// 字段完全匹配当前 mutation 的 AuditEvent，否则拒绝提交并回滚。
type CredentialAtomicTx interface {
	AuditTx
	CredentialMutationTx
	ConnectionRefReadTx
	ConnectionVersionWriter
	Commit() error
	Rollback() error
}

// ConnectionAtomicTx connection 原子化组合。内部接口，仅由 connections.Service 使用。
// Commit 语义同 CredentialAtomicTx。
type ConnectionAtomicTx interface {
	AuditTx
	ConnectionMutationTx
	Commit() error
	Rollback() error
}

// AtomicTxStore 供 credential/connection 审计协调器开启原子化事务。
type AtomicTxStore interface {
	BeginCredential(ctx context.Context, op *OperationContext) (CredentialAtomicTx, error)
	BeginConnection(ctx context.Context, op *OperationContext) (ConnectionAtomicTx, error)
}

// ---- 域绑定校验 ---------------------------------------------------------------

// eventAllowed 校验 (resource, action, outcome) 是否在 E1-E6 事件矩阵内。
func eventAllowed(resource, action, outcome string) bool {
	switch resource + "/" + action + "/" + outcome {
	case "connection/connection.create/succeeded", // E1
		"connection/connection.update/succeeded", // E2
		"credential/credential.create/succeeded", // E3
		"credential/credential.rotate/succeeded", // E4
		"credential/credential.rotate/failed",    // E5
		"credential/credential.retire/succeeded": // E6
		return true
	}
	return false
}

// validateOpForCredential 供 BeginCredential 使用：先 nil/Validate，再强制
// resource=="credential"，且 action/outcome 在 E3-E6 允许组合内。
func validateOpForCredential(op *OperationContext) error {
	if op == nil {
		return errors.New("operation context: nil")
	}
	if err := op.Validate(); err != nil {
		return err
	}
	if op.Resource() != "credential" {
		return fmt.Errorf("operation context: BeginCredential requires resource=credential, got %q", op.Resource())
	}
	if !eventAllowed(op.Resource(), op.Action(), op.Outcome()) {
		return fmt.Errorf("operation context: invalid credential event %q/%q", op.Action(), op.Outcome())
	}
	return nil
}

// validateOpForConnection 供 BeginConnection 使用：先 nil/Validate，再强制
// resource=="connection"，且 action/outcome 在 E1-E2 允许组合内。
func validateOpForConnection(op *OperationContext) error {
	if op == nil {
		return errors.New("operation context: nil")
	}
	if err := op.Validate(); err != nil {
		return err
	}
	if op.Resource() != "connection" {
		return fmt.Errorf("operation context: BeginConnection requires resource=connection, got %q", op.Resource())
	}
	if !eventAllowed(op.Resource(), op.Action(), op.Outcome()) {
		return fmt.Errorf("operation context: invalid connection event %q/%q", op.Action(), op.Outcome())
	}
	return nil
}

// ---- 实现 ---------------------------------------------------------------------

// insertAuditEvent 在指定事务上执行审计 INSERT（与 pgMetadataTx.AppendAudit 共享）。
func insertAuditEvent(ctx context.Context, tx *sql.Tx, e *AuditEvent) error {
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("audit: occurred_at 不得为零值")
	}
	if err := ValidateAuditEventMetadata(e.Action, e.Outcome, e.Metadata); err != nil {
		return err
	}
	const q = `
		INSERT INTO audit_events
			(workspace_id, actor_type, actor_id, connection_id,
			 action, resource_type, resource_id, outcome,
			 metadata, trace_id, execution_id, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id, created_at`
	return tx.QueryRowContext(ctx, q,
		e.WorkspaceID, string(e.ActorType), e.ActorID, e.ConnectionID,
		e.Action, e.ResourceType, e.ResourceID, string(e.Outcome),
		e.Metadata, e.TraceID, e.ExecutionID, e.OccurredAt,
	).Scan(&e.ID, &e.CreatedAt)
}

// pgCredentialAtomicTx 基于 *sql.Tx 的 CredentialAtomicTx 实现。
// 与 pgMetadataTx 是不同 wrapper 类型：不跨域实现 ConnectionMutationTx，
// 也不暴露跨域 mutation 接口（设计 §3）。
type pgCredentialAtomicTx struct {
	store         *PGStore
	tx            *sql.Tx
	op            *OperationContext
	auditAppended bool
}

// AppendAudit 追加审计；校验 op 与事务绑定上下文一致、事件与 op 完全匹配，
// 且每个 mutation 只能追加一个匹配事件（Commit 闸门要求有且仅有一个）。
func (t *pgCredentialAtomicTx) AppendAudit(ctx context.Context, op *OperationContext, e *AuditEvent) error {
	if t.op == nil || !matchOperationContext(op, t.op) {
		return errors.New("audit gate: operation context mismatch")
	}
	if err := matchAuditEventOp(t.op, e); err != nil {
		return err
	}
	if t.auditAppended {
		return errors.New("audit gate: duplicate audit event for mutation")
	}
	if err := insertAuditEvent(ctx, t.tx, e); err != nil {
		return err
	}
	t.auditAppended = true
	return nil
}

func (t *pgCredentialAtomicTx) LockEnvelopeForUpdate(ctx context.Context, wsID, secretRef uuid.UUID) (*CredentialEnvelope, error) {
	return t.store.LockEnvelopeForUpdate(ctx, t.tx, wsID, secretRef)
}

func (t *pgCredentialAtomicTx) LockEnvelopeVersion(ctx context.Context, wsID, secretRef uuid.UUID, version int) (*CredentialEnvelope, error) {
	return t.store.LockEnvelopeVersion(ctx, t.tx, wsID, secretRef, version)
}

func (t *pgCredentialAtomicTx) InsertEnvelope(ctx context.Context, env *CredentialEnvelope) error {
	return t.store.InsertEnvelopeTx(ctx, t.tx, env)
}

func (t *pgCredentialAtomicTx) UpdateRetiredAt(ctx context.Context, wsID, secretRef uuid.UUID, version int) error {
	return t.store.UpdateRetiredAt(ctx, t.tx, wsID, secretRef, version)
}

func (t *pgCredentialAtomicTx) CountConnectionsByVersion(ctx context.Context, wsID, secretRef uuid.UUID, version int) (int, error) {
	return t.store.CountConnectionsByVersion(ctx, t.tx, wsID, secretRef, version)
}

// UpdateConnectionVersion 在 credential 原子事务内更新连接引用版本（Rotate 所需）。
func (t *pgCredentialAtomicTx) UpdateConnectionVersion(ctx context.Context, wsID, secretRef uuid.UUID, newVersion int) error {
	return t.store.UpdateConnectionVersion(ctx, t.tx, wsID, secretRef, newVersion)
}

// Commit 审计闸门：未追加匹配 AuditEvent 时拒绝提交并回滚。
func (t *pgCredentialAtomicTx) Commit() error {
	if !t.auditAppended {
		_ = t.tx.Rollback()
		return errors.New("audit gate: no matching audit event appended before commit")
	}
	if err := t.tx.Commit(); err != nil {
		return fmt.Errorf("commit credential atomic tx: %w", err)
	}
	return nil
}

func (t *pgCredentialAtomicTx) Rollback() error {
	if err := t.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("rollback credential atomic tx: %w", err)
	}
	return nil
}

// pgConnectionAtomicTx 基于 *sql.Tx 的 ConnectionAtomicTx 实现。
type pgConnectionAtomicTx struct {
	store         *PGStore
	tx            *sql.Tx
	op            *OperationContext
	auditAppended bool
}

func (t *pgConnectionAtomicTx) AppendAudit(ctx context.Context, op *OperationContext, e *AuditEvent) error {
	if t.op == nil || !matchOperationContext(op, t.op) {
		return errors.New("audit gate: operation context mismatch")
	}
	if err := matchAuditEventOp(t.op, e); err != nil {
		return err
	}
	if t.auditAppended {
		return errors.New("audit gate: duplicate audit event for mutation")
	}
	if err := insertAuditEvent(ctx, t.tx, e); err != nil {
		return err
	}
	t.auditAppended = true
	return nil
}

// createConnectionOnTx 在指定事务上执行连接 INSERT（复用 CreateConnection 的
// active-envelope FOR KEY SHARE 语义）。
func createConnectionOnTx(ctx context.Context, tx *sql.Tx, c *Connection) error {
	const q = `
		INSERT INTO connections
			(workspace_id, name, engine, host, port, database, environment,
			 secret_ref, secret_version, created_by)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10
		FROM credential_envelopes AS active_credential
		WHERE active_credential.workspace_id = $1
		  AND active_credential.secret_ref = $8
		  AND active_credential.version = $9
		  AND active_credential.retired_at IS NULL
		FOR KEY SHARE OF active_credential
		RETURNING id, created_at, updated_at`
	err := tx.QueryRowContext(ctx, q,
		c.WorkspaceID, c.Name, string(c.Engine),
		c.Host, c.Port, c.Database, string(c.Environment),
		c.SecretRef, c.SecretVersion, c.CreatedBy,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf(
			"active credential envelope (%s, %s, %d): %w",
			c.WorkspaceID, c.SecretRef, c.SecretVersion, ErrEnvelopeNotFound,
		)
	}
	return err
}

// updateConnectionOnTx 在指定事务上执行连接 UPDATE（复用 UpdateConnection 语义）。
func updateConnectionOnTx(ctx context.Context, tx *sql.Tx, wsID uuid.UUID, c *Connection) error {
	const checkConn = `SELECT 1 FROM connections WHERE id = $1 AND workspace_id = $2`
	if err := tx.QueryRowContext(ctx, checkConn, c.ID, wsID).Scan(new(int)); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("connection %s not found in workspace %s", c.ID, wsID)
	} else if err != nil {
		return err
	}

	const q = `
		WITH active_credential AS MATERIALIZED (
			SELECT 1
			FROM credential_envelopes AS envelope
			WHERE envelope.workspace_id = $10
			  AND envelope.secret_ref = $7
			  AND envelope.version = $8
			  AND envelope.retired_at IS NULL
			FOR KEY SHARE OF envelope
		)
		UPDATE connections AS connection SET
			name=$1, engine=$2, host=$3, port=$4,
			database=$5, environment=$6, secret_ref=$7, secret_version=$8,
			updated_at=GREATEST(clock_timestamp(), connection.updated_at + interval '1 microsecond')
		FROM active_credential
		WHERE connection.id=$9 AND connection.workspace_id=$10
		RETURNING connection.updated_at`
	err := tx.QueryRowContext(ctx, q,
		c.Name, string(c.Engine), c.Host, c.Port, c.Database,
		string(c.Environment), c.SecretRef, c.SecretVersion, c.ID, wsID,
	).Scan(&c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf(
			"active credential envelope (%s, %s, %d): %w",
			wsID, c.SecretRef, c.SecretVersion, ErrEnvelopeNotFound,
		)
	}
	return err
}

func (t *pgConnectionAtomicTx) CreateConnection(ctx context.Context, conn *Connection) error {
	return createConnectionOnTx(ctx, t.tx, conn)
}

func (t *pgConnectionAtomicTx) UpdateConnection(ctx context.Context, wsID uuid.UUID, conn *Connection) error {
	return updateConnectionOnTx(ctx, t.tx, wsID, conn)
}

func (t *pgConnectionAtomicTx) UpdateConnectionVersion(ctx context.Context, wsID, secretRef uuid.UUID, newVersion int) error {
	return t.store.UpdateConnectionVersion(ctx, t.tx, wsID, secretRef, newVersion)
}

func (t *pgConnectionAtomicTx) CountConnectionsByVersion(ctx context.Context, wsID, secretRef uuid.UUID, version int) (int, error) {
	return t.store.CountConnectionsByVersion(ctx, t.tx, wsID, secretRef, version)
}

// Commit 审计闸门：未追加匹配 AuditEvent 时拒绝提交并回滚。
func (t *pgConnectionAtomicTx) Commit() error {
	if !t.auditAppended {
		_ = t.tx.Rollback()
		return errors.New("audit gate: no matching audit event appended before commit")
	}
	if err := t.tx.Commit(); err != nil {
		return fmt.Errorf("commit connection atomic tx: %w", err)
	}
	return nil
}

func (t *pgConnectionAtomicTx) Rollback() error {
	if err := t.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("rollback connection atomic tx: %w", err)
	}
	return nil
}

// ---- PGStore 原子事务入口 -------------------------------------------------------

// BeginCredential 开启 credential 原子化事务；先校验 op（nil / 域 / E3-E6 矩阵），
// 校验失败不开启事务、不执行 mutation。
func (s *PGStore) BeginCredential(ctx context.Context, op *OperationContext) (CredentialAtomicTx, error) {
	if err := validateOpForCredential(op); err != nil {
		return nil, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin credential atomic tx: %w", err)
	}
	return &pgCredentialAtomicTx{store: s, tx: tx, op: op}, nil
}

// BeginConnection 开启 connection 原子化事务；先校验 op（nil / 域 / E1-E2 矩阵）。
func (s *PGStore) BeginConnection(ctx context.Context, op *OperationContext) (ConnectionAtomicTx, error) {
	if err := validateOpForConnection(op); err != nil {
		return nil, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin connection atomic tx: %w", err)
	}
	return &pgConnectionAtomicTx{store: s, tx: tx, op: op}, nil
}

// Compile-time checks
var _ AtomicTxStore = (*PGStore)(nil)
var _ CredentialAtomicTx = (*pgCredentialAtomicTx)(nil)
var _ ConnectionAtomicTx = (*pgConnectionAtomicTx)(nil)
