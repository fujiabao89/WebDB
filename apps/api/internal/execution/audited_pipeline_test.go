package execution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- mocks（审计感知管线）---------------------------------------------------

type fakeMetadataTx struct {
	createdExecs []*metadata.Execution
	updatedExecs []*metadata.Execution
	auditEvents  []*metadata.AuditEvent
	failUpdate   error
	failAudit    error
	failCommit   error
	committed    bool
	rolledBack   bool
}

func (t *fakeMetadataTx) CreateExecution(_ context.Context, e *metadata.Execution) error {
	e.ID = uuid.New()
	now := time.Now()
	e.StartedAt = now
	e.CreatedAt = now
	t.createdExecs = append(t.createdExecs, e)
	return nil
}

func (t *fakeMetadataTx) UpdateExecution(_ context.Context, _ uuid.UUID, e *metadata.Execution) error {
	if t.failUpdate != nil {
		return t.failUpdate
	}
	t.updatedExecs = append(t.updatedExecs, e)
	return nil
}

func (t *fakeMetadataTx) AppendAudit(_ context.Context, e *metadata.AuditEvent) error {
	if t.failAudit != nil {
		return t.failAudit
	}
	e.ID = uuid.New()
	t.auditEvents = append(t.auditEvents, e)
	return nil
}

func (t *fakeMetadataTx) Commit() error {
	if t.failCommit != nil {
		return t.failCommit
	}
	t.committed = true
	return nil
}
func (t *fakeMetadataTx) Rollback() error { t.rolledBack = true; return nil }

type fakeTxStore struct {
	txs            []*fakeMetadataTx
	failBegin      error
	failAudit      error
	failUpdate     error
	failCommit     error
	failCommitTxID int // 从 1 开始：仅第 N 个事务的 Commit 失败（区分阶段 B 与失败分支/D-0）
}

func (f *fakeTxStore) Begin(context.Context) (metadata.MetadataTx, error) {
	if f.failBegin != nil {
		return nil, f.failBegin
	}
	idx := len(f.txs) + 1
	var fc error
	if f.failCommitTxID > 0 && idx == f.failCommitTxID {
		fc = f.failCommit
	}
	tx := &fakeMetadataTx{failAudit: f.failAudit, failUpdate: f.failUpdate, failCommit: fc}
	f.txs = append(f.txs, tx)
	return tx, nil
}

func (f *fakeTxStore) allAuditEvents() []*metadata.AuditEvent {
	var out []*metadata.AuditEvent
	for _, tx := range f.txs {
		out = append(out, tx.auditEvents...)
	}
	return out
}

func (f *fakeTxStore) allUpdatedExecs() []*metadata.Execution {
	var out []*metadata.Execution
	for _, tx := range f.txs {
		out = append(out, tx.updatedExecs...)
	}
	return out
}

type fakeAuditStore struct {
	events []*metadata.AuditEvent
	fail   error
}

func (f *fakeAuditStore) AppendAudit(_ context.Context, e *metadata.AuditEvent) error {
	if f.fail != nil {
		return f.fail
	}
	e.ID = uuid.New()
	f.events = append(f.events, e)
	return nil
}

func (f *fakeAuditStore) QueryAudit(context.Context, metadata.AuditQuery) ([]metadata.AuditEvent, error) {
	return nil, nil
}

type fakeAlarm struct {
	events []SecurityAlertEvent
}

func (f *fakeAlarm) Alarm(_ context.Context, e SecurityAlertEvent) {
	f.events = append(f.events, e)
}

// auditedPipeline 构建带审计的管线。
func auditedPipeline(
	connReader ConnectionReader,
	policyReader ConnectionPolicyReader,
	members WorkspaceMemberReader,
	resolver credentials.CredentialResolver,
	adapterClient AdapterClient,
	txStore *fakeTxStore,
	auditStore *fakeAuditStore,
	alarm *fakeAlarm,
	clock func() time.Time,
) *Pipeline {
	return NewPipeline(PipelineConfig{
		Store:       connReader,
		PolicyStore: policyReader,
		Members:     members,
		Resolver:    resolver,
		Adapter:     adapterClient,
		Tx:          txStore,
		Audit:       auditStore,
		Alarm:       alarm,
		Clock:       clock,
	})
}

func auditedPipelineInputs() (
	AuthenticatedPrincipal,
	*metadata.Connection,
	*metadata.ConnectionPolicy,
	*fakeResolver,
	*fakeAdapterClient,
	*fakeTxStore,
	*fakeAuditStore,
	*fakeAlarm,
) {
	principal, conn, policy, resolver, client := validPipelineInputs()
	txStore := &fakeTxStore{}
	auditStore := &fakeAuditStore{}
	alarm := &fakeAlarm{}
	return principal, conn, policy, resolver, client, txStore, auditStore, alarm
}

func auditedMember(principal AuthenticatedPrincipal) *fakeMemberReader {
	return &fakeMemberReader{
		member: &metadata.WorkspaceMember{WorkspaceID: principal.WorkspaceID, UserID: principal.UserID, Role: metadata.RoleViewer},
	}
}

// realClock 返回动态时钟（time.Now），使 finished_at 采样不早于 execution started_at
// 且发生于 adapter 工作之后（Codex P1 修复 4 的验证基础）。
func realClock() func() time.Time {
	return func() time.Time { return time.Now() }
}

// ---- 生命周期事件测试 --------------------------------------------------------

func TestAuditedExecute_Succeeded(t *testing.T) {
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	client.handle.result = &adapter.QueryResult{TotalReturned: 7}

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.Result == nil {
		t.Fatal("expected query result")
	}
	if result.ExecutionID == nil {
		t.Fatal("expected execution id")
	}

	// execution 终态 completed
	updates := txStore.allUpdatedExecs()
	if len(updates) == 0 {
		t.Fatal("expected execution updates")
	}
	last := updates[len(updates)-1]
	if last.Status != metadata.ExecStatusCompleted {
		t.Fatalf("execution status = %s, want completed", last.Status)
	}
	if last.ErrorCode != nil {
		t.Fatalf("completed execution error_code = %v, want nil", *last.ErrorCode)
	}
	// finished_at 必须在 adapter 工作之后采样，不得早于 started_at（Codex P1）。
	if last.FinishedAt == nil || last.FinishedAt.Before(last.StartedAt) {
		t.Fatalf("finished_at=%v must not be before started_at=%v", last.FinishedAt, last.StartedAt)
	}
	// executions.row_count 必须回写，便于与审计 metadata 交叉核对（CodeRabbit #19）。
	if last.RowCount == nil || *last.RowCount != 7 {
		t.Fatalf("execution row_count = %v, want 7", last.RowCount)
	}

	// audit sql.execute succeeded（独立写入）
	if len(auditStore.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(auditStore.events))
	}
	ev := auditStore.events[0]
	assertAuditEvent(t, ev, metadata.ActionSQLExecute, metadata.OutcomeSucceeded, principal, conn, result.ExecutionID)
	if ev.Metadata == nil {
		t.Fatal("expected metadata")
	}
	var md map[string]any
	if err := json.Unmarshal(ev.Metadata, &md); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if md["engine"] != "postgresql" {
		t.Errorf("engine = %v, want postgresql", md["engine"])
	}
	if md["row_count"] != float64(7) {
		t.Errorf("row_count = %v, want 7", md["row_count"])
	}
	if _, ok := md["statement_hash"]; !ok {
		t.Error("statement_hash missing")
	}

	// 告警不应触发
	if len(alarm.events) != 0 {
		t.Fatalf("alarm events = %d, want 0", len(alarm.events))
	}
}

func TestAuditedExecute_PolicyDenied(t *testing.T) {
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "DELETE FROM users",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error for denied SQL")
	}
	if result.ErrorCode != StableErrorCode("statement_not_allowed") {
		t.Fatalf("error code = %q, want statement_not_allowed", result.ErrorCode)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolver.calls)
	}
	if client.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", client.calls)
	}

	// execution failed
	updates := txStore.allUpdatedExecs()
	if len(updates) == 0 {
		t.Fatal("expected execution update")
	}
	last := updates[len(updates)-1]
	if last.Status != metadata.ExecStatusFailed {
		t.Fatalf("execution status = %s, want failed", last.Status)
	}

	// 执行前事务必须已提交（Codex P1），而非被 defer Rollback 丢弃；
	// 阶段 B（pending 创建）与失败分支（failed + audit）均为短事务并提交（finding 2）。
	if len(txStore.txs) != 2 || !txStore.txs[0].committed || !txStore.txs[1].committed {
		t.Fatal("denied path must commit both the pending-create and fail-record transactions")
	}

	// audit sql.execute denied（mtx 内原子写入）
	events := txStore.allAuditEvents()
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	assertAuditEvent(t, events[0], metadata.ActionSQLExecute, metadata.OutcomeDenied, principal, conn, result.ExecutionID)
	var md map[string]any
	if err := json.Unmarshal(events[0].Metadata, &md); err != nil {
		t.Fatal(err)
	}
	if md["reason_code"] != "statement_not_allowed" {
		t.Errorf("reason_code = %v, want statement_not_allowed", md["reason_code"])
	}
}

func TestAuditedExecute_CredentialFailure(t *testing.T) {
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	resolver.err = credentials.ErrDecryptionFailed

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != StableErrorCode(credentials.ErrDecryptionFailed) {
		t.Fatalf("error code = %q, want decryption_failed", result.ErrorCode)
	}
	if client.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", client.calls)
	}

	// E15 credential.decrypt failed（system actor）
	events := txStore.allAuditEvents()
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Action != metadata.ActionCredentialDecrypt {
		t.Fatalf("action = %q, want credential.decrypt", ev.Action)
	}
	if ev.Outcome != metadata.OutcomeFailed {
		t.Fatalf("outcome = %q, want failed", ev.Outcome)
	}
	if ev.ActorType != metadata.ActorTypeSystem || ev.ActorID != nil {
		t.Fatalf("actor type = %q actor_id = %v, want system/nil", ev.ActorType, ev.ActorID)
	}
	if ev.ConnectionID == nil || *ev.ConnectionID != conn.ID {
		t.Fatalf("connection id = %v, want %v", ev.ConnectionID, conn.ID)
	}
	// E14-E16 事件矩阵要求 execution_id 为 NULL（proposal §8.1，CodeRabbit #18）。
	if ev.ExecutionID != nil {
		t.Fatalf("execution id = %v, want nil (E14-E16 matrix)", *ev.ExecutionID)
	}

	// 安全告警触发（凭证解密失败）
	if len(alarm.events) != 1 {
		t.Fatalf("alarm events = %d, want 1", len(alarm.events))
	}
	if alarm.events[0].Code != string(credentials.ErrDecryptionFailed) {
		t.Fatalf("alarm code = %q, want decryption_failed", alarm.events[0].Code)
	}
}

func TestAuditedExecute_Timeout(t *testing.T) {
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	client.handle.err = &adapter.AdapterError{Code: adapter.ErrQueryTimeout}

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrExecutionTimeout {
		t.Fatalf("error code = %q, want execution_timeout", result.ErrorCode)
	}
	if result.AdapterCalled != true {
		t.Fatal("adapter should have been called")
	}

	// execution failed + audit sql.execute failed (query_timeout)
	updates := txStore.allUpdatedExecs()
	last := updates[len(updates)-1]
	if last.Status != metadata.ExecStatusFailed {
		t.Fatalf("execution status = %s, want failed", last.Status)
	}
	if len(auditStore.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(auditStore.events))
	}
	ev := auditStore.events[0]
	assertAuditEvent(t, ev, metadata.ActionSQLExecute, metadata.OutcomeFailed, principal, conn, result.ExecutionID)
	var md map[string]any
	if err := json.Unmarshal(ev.Metadata, &md); err != nil {
		t.Fatal(err)
	}
	if md["error_code"] != "query_timeout" {
		t.Errorf("error_code = %v, want query_timeout", md["error_code"])
	}
}

func TestAuditedExecute_Cancelled(t *testing.T) {
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	client.handle.err = &adapter.AdapterError{Code: adapter.ErrQueryCanceled}

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrExecutionCancelled {
		t.Fatalf("error code = %q, want execution_cancelled", result.ErrorCode)
	}

	updates := txStore.allUpdatedExecs()
	last := updates[len(updates)-1]
	if last.Status != metadata.ExecStatusCancelled {
		t.Fatalf("execution status = %s, want cancelled", last.Status)
	}
	if len(auditStore.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(auditStore.events))
	}
	assertAuditEvent(t, auditStore.events[0], metadata.ActionSQLExecute, metadata.OutcomeCancelled, principal, conn, result.ExecutionID)
}

func TestAuditedExecute_CancelledContextStillPersists(t *testing.T) {
	// 调用方取消 ctx 后，execution 仍必须标记 cancelled 且 E13 追加（Codex P1：
	// recordPostExecution 使用 context.WithoutCancel 继续持久化）。
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	client.handle.err = context.Canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(ctx, ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrExecutionCancelled {
		t.Fatalf("error code = %q, want execution_cancelled", result.ErrorCode)
	}

	updates := txStore.allUpdatedExecs()
	if len(updates) == 0 {
		t.Fatal("expected execution update despite cancelled context")
	}
	last := updates[len(updates)-1]
	if last.Status != metadata.ExecStatusCancelled {
		t.Fatalf("execution status = %s, want cancelled (must persist on cancelled ctx)", last.Status)
	}
	if len(auditStore.events) != 1 {
		t.Fatalf("audit events = %d, want 1 (E13 must be appended on cancelled ctx)", len(auditStore.events))
	}
	assertAuditEvent(t, auditStore.events[0], metadata.ActionSQLExecute, metadata.OutcomeCancelled, principal, conn, result.ExecutionID)
}

// ---- 审计失败注入（AUDIT-01~AUDIT-04）---------------------------------------

func TestAuditedExecute_PreExecutionAuditFailure(t *testing.T) {
	// 阶段 C 拒绝时审计写入失败 → fail-closed，Adapter 0 次，返回 audit_failed，告警触发。
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	txStore.failAudit = errors.New("injected audit failure")

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "DELETE FROM users",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed", result.ErrorCode)
	}
	if client.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0 (pre-execution audit failure)", client.calls)
	}
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
}

func TestAuditedExecute_PostExecutionAuditFailure(t *testing.T) {
	// 阶段 D 后审计写入失败 → 不返回结果、execution 已 completed、返回 audit_failed。
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	client.handle.result = &adapter.QueryResult{TotalReturned: 3}
	auditStore.fail = errors.New("injected post audit failure")

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed", result.ErrorCode)
	}
	if result.Result != nil {
		t.Fatal("post-execution audit failure must not return query result")
	}
	// execution 已记录为 completed
	updates := txStore.allUpdatedExecs()
	last := updates[len(updates)-1]
	if last.Status != metadata.ExecStatusCompleted {
		t.Fatalf("execution status = %s, want completed (post-execution audit failure)", last.Status)
	}
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
}

func TestAuditedExecute_CredentialAuditFailure(t *testing.T) {
	// AUDIT-04: credential.decrypt 失败审计失败 → audit_failed，Adapter 0 次。
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	resolver.err = credentials.ErrDecryptionFailed
	txStore.failAudit = errors.New("injected audit failure")

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed", result.ErrorCode)
	}
	if client.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", client.calls)
	}
	// 告警至少包含 decrypt 失败（先告警）和 audit_failed（后告警）
	if len(alarm.events) < 2 {
		t.Fatalf("alarm events = %d, want >= 2 (decrypt + audit_failed)", len(alarm.events))
	}
}

// ---- 关联正确性 --------------------------------------------------------------

func TestAuditedExecute_AssociationCorrectness(t *testing.T) {
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	client.handle.result = &adapter.QueryResult{TotalReturned: 1}

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	// execution 关联
	updates := txStore.allUpdatedExecs()
	for _, u := range updates {
		if u.WorkspaceID != conn.WorkspaceID {
			t.Errorf("execution workspace = %v, want %v", u.WorkspaceID, conn.WorkspaceID)
		}
		if u.ConnectionID != conn.ID {
			t.Errorf("execution connection = %v, want %v", u.ConnectionID, conn.ID)
		}
		if u.ActorID != principal.UserID {
			t.Errorf("execution actor = %v, want %v", u.ActorID, principal.UserID)
		}
	}

	// audit 关联
	if len(auditStore.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(auditStore.events))
	}
	ev := auditStore.events[0]
	if ev.WorkspaceID != conn.WorkspaceID {
		t.Errorf("audit workspace = %v, want %v", ev.WorkspaceID, conn.WorkspaceID)
	}
	if ev.ConnectionID == nil || *ev.ConnectionID != conn.ID {
		t.Errorf("audit connection = %v, want %v", ev.ConnectionID, conn.ID)
	}
	if ev.ExecutionID == nil || *ev.ExecutionID != *result.ExecutionID {
		t.Errorf("audit execution = %v, want %v", ev.ExecutionID, result.ExecutionID)
	}
	if ev.ActorID == nil || *ev.ActorID != principal.UserID {
		t.Errorf("audit actor = %v, want %v", ev.ActorID, principal.UserID)
	}
}

// ---- helpers ----------------------------------------------------------------

func assertAuditEvent(
	t *testing.T,
	ev *metadata.AuditEvent,
	action string,
	outcome metadata.AuditOutcome,
	principal AuthenticatedPrincipal,
	conn *metadata.Connection,
	execID *uuid.UUID,
) {
	t.Helper()
	if ev.Action != action {
		t.Errorf("action = %q, want %q", ev.Action, action)
	}
	if ev.Outcome != outcome {
		t.Errorf("outcome = %q, want %q", ev.Outcome, outcome)
	}
	if ev.WorkspaceID != principal.WorkspaceID {
		t.Errorf("workspace = %v, want %v", ev.WorkspaceID, principal.WorkspaceID)
	}
	if ev.ConnectionID == nil || *ev.ConnectionID != conn.ID {
		t.Errorf("connection = %v, want %v", ev.ConnectionID, conn.ID)
	}
	if execID != nil {
		if ev.ExecutionID == nil || *ev.ExecutionID != *execID {
			t.Errorf("execution = %v, want %v", ev.ExecutionID, execID)
		}
	}
	if ev.TraceID == "" {
		t.Error("trace_id must not be empty")
	}
	if ev.OccurredAt.IsZero() {
		t.Error("occurred_at must not be zero")
	}
}

// ---- 元数据库事务失败注入（CodeRabbit #17）----------------------------------

func TestAuditedExecute_CommitFailurePreExecution(t *testing.T) {
	// 失败分支的 commitPreExecution 提交失败（ADR-017 §6 关键安全路径）→ audit_failed + $SECURITY_ALERT；
	// 阶段 B（tx[1]）提交成功，失败分支（tx[2]）提交失败（finding 2 短事务）。
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	txStore.failCommit = errors.New("injected commit failure")
	txStore.failCommitTxID = 2

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "DELETE FROM users",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed (commit failure must fail-closed)", result.ErrorCode)
	}
	if client.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", client.calls)
	}
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
}

func TestAuditedExecute_BeginFailure(t *testing.T) {
	// Begin 失败 → ErrInternalError，Adapter 调用 0 次，不产生告警。
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	txStore.failBegin = errors.New("injected begin failure")

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrInternalError {
		t.Fatalf("error code = %q, want internal_error", result.ErrorCode)
	}
	if client.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0 (begin failure)", client.calls)
	}
	// 阶段 B 失败触发 $SECURITY_ALERT（proposal §9.1，VuXZO）。
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrInternalError) {
		t.Fatalf("expected internal_error alarm, got %+v", alarm.events)
	}
}

func TestAuditedExecute_UpdateFailurePreExecution(t *testing.T) {
	// UpdateExecution 失败（执行前 fail-closed）→ audit_failed + $SECURITY_ALERT，Adapter 0 次。
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	txStore.failUpdate = errors.New("injected update failure")

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "DELETE FROM users",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed (update failure must fail-closed)", result.ErrorCode)
	}
	if client.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", client.calls)
	}
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
}

// ---- 阶段 D-0 pending→running 失败（vpvC7 outside）---------------------------

func TestAuditedExecute_D0RunningUpdateFailure(t *testing.T) {
	// D-0 将 execution 更新为 running 失败 → audit_failed + $SECURITY_ALERT，Adapter 0 次。
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	client.handle.result = &adapter.QueryResult{TotalReturned: 1}
	txStore.failUpdate = errors.New("injected update failure")

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed (D-0 update failure must fail-closed)", result.ErrorCode)
	}
	if client.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", client.calls)
	}
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
}

func TestAuditedExecute_D0CommitFailure(t *testing.T) {
	// D-0 running 更新后提交失败 → audit_failed + $SECURITY_ALERT，Adapter 0 次；
	// 阶段 B（tx[1]）提交成功，D-0（tx[2]）提交失败（finding 2 短事务）。
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	client.handle.result = &adapter.QueryResult{TotalReturned: 1}
	txStore.failCommit = errors.New("injected commit failure")
	txStore.failCommitTxID = 2

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed (D-0 commit failure must fail-closed)", result.ErrorCode)
	}
	if client.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", client.calls)
	}
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
}

// blockingAuditStore 在 block=true 时阻塞直到 ctx 到期，用于验证审计写入真实超时（VuXZW）。
type blockingAuditStore struct {
	events []*metadata.AuditEvent
	block  bool
}

func (f *blockingAuditStore) AppendAudit(ctx context.Context, e *metadata.AuditEvent) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	e.ID = uuid.New()
	f.events = append(f.events, e)
	return nil
}

func (f *blockingAuditStore) QueryAudit(context.Context, metadata.AuditQuery) ([]metadata.AuditEvent, error) {
	return nil, nil
}

// TestAuditedExecute_PostExecutionAuditWriteTimeout 验证 recordPostExecution 的审计写入
// 受 AuditWriteTimeout 约束：审计 store 阻塞到 ctx 到期 → 返回 audit_failed + $SECURITY_ALERT（VuXZW）。
func TestAuditedExecute_PostExecutionAuditWriteTimeout(t *testing.T) {
	principal, conn, policy, resolver, client, txStore, _, _ := auditedPipelineInputs()
	client.handle.result = &adapter.QueryResult{TotalReturned: 1}
	blocking := &blockingAuditStore{block: true}
	alarm := &fakeAlarm{}

	pipeline := NewPipeline(PipelineConfig{
		Store:             &fakeConnectionReader{connections: []*metadata.Connection{conn}},
		PolicyStore:       &fakePolicyReader{policy: policy},
		Members:           auditedMember(principal),
		Resolver:          resolver,
		Adapter:           client,
		Tx:                txStore,
		Audit:             blocking,
		Alarm:             alarm,
		Clock:             realClock(),
		AuditWriteTimeout: 50 * time.Millisecond,
	})

	start := time.Now()
	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed", result.ErrorCode)
	}
	if elapsed > 1*time.Second {
		t.Fatalf("audit write not cancelled within server timeout: %v", elapsed)
	}
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
}

// TestAuditedExecute_StageBCommitFailureClearsExecutionID 验证阶段 B pending 创建后
// Commit 失败时，未持久化的 execution 的 ExecutionID 从返回结果清除（finding 5）。
func TestAuditedExecute_StageBCommitFailureClearsExecutionID(t *testing.T) {
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	txStore.failCommit = errors.New("injected commit failure")
	txStore.failCommitTxID = 1 // 阶段 B（tx[1]）Commit 失败

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "DELETE FROM users",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrInternalError {
		t.Fatalf("error code = %q, want internal_error (stage B commit failure)", result.ErrorCode)
	}
	if result.ExecutionID != nil {
		t.Fatalf("ExecutionID must be nil when pending execution is not persisted, got %v", *result.ExecutionID)
	}
	// 阶段 B 失败触发 E17 告警（internal_error）。
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrInternalError) {
		t.Fatalf("expected internal_error alarm, got %+v", alarm.events)
	}
}

// ---- 安全告警通道自身失败（T28 / CodeRabbit #31）-----------------------------

type panicAlarm struct{}

func (panicAlarm) Alarm(context.Context, SecurityAlertEvent) { panic("alarm channel down") }

func TestAuditedExecute_AlarmFailureDoesNotPanic(t *testing.T) {
	// 告警通道 panic 不得改变 fail-closed 返回（T28）：Execute 仍返回 audit_failed，
	// 不 panic、不递归审计。
	principal, conn, policy, resolver, client, txStore, auditStore, _ := auditedPipelineInputs()
	client.handle.result = &adapter.QueryResult{TotalReturned: 3}
	auditStore.fail = errors.New("injected post audit failure")

	pipeline := NewPipeline(PipelineConfig{
		Store:       &fakeConnectionReader{connections: []*metadata.Connection{conn}},
		PolicyStore: &fakePolicyReader{policy: policy},
		Members:     auditedMember(principal),
		Resolver:    resolver,
		Adapter:     client,
		Tx:          txStore,
		Audit:       auditStore,
		Alarm:       panicAlarm{},
		Clock:       realClock(),
	})

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed (alarm failure must not alter fail-closed)", result.ErrorCode)
	}
	if result.Result != nil {
		t.Fatal("audit failure must not return query result")
	}
}
