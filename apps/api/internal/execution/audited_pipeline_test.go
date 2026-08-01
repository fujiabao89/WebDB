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

func (t *fakeMetadataTx) Commit() error   { t.committed = true; return nil }
func (t *fakeMetadataTx) Rollback() error { t.rolledBack = true; return nil }

type fakeTxStore struct {
	txs        []*fakeMetadataTx
	failBegin  error
	failAudit  error
	failUpdate error
}

func (f *fakeTxStore) Begin(context.Context) (metadata.MetadataTx, error) {
	if f.failBegin != nil {
		return nil, f.failBegin
	}
	tx := &fakeMetadataTx{failAudit: f.failAudit, failUpdate: f.failUpdate}
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

func fixedClock() func() time.Time {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

// ---- 生命周期事件测试 --------------------------------------------------------

func TestAuditedExecute_Succeeded(t *testing.T) {
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	client.handle.result = &adapter.QueryResult{TotalReturned: 7}

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, fixedClock(),
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
		resolver, client, txStore, auditStore, alarm, fixedClock(),
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
		resolver, client, txStore, auditStore, alarm, fixedClock(),
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
		resolver, client, txStore, auditStore, alarm, fixedClock(),
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
		resolver, client, txStore, auditStore, alarm, fixedClock(),
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

// ---- 审计失败注入（AUDIT-01~AUDIT-04）---------------------------------------

func TestAuditedExecute_PreExecutionAuditFailure(t *testing.T) {
	// 阶段 C 拒绝时审计写入失败 → fail-closed，Adapter 0 次，返回 audit_failed，告警触发。
	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	txStore.failAudit = errors.New("injected audit failure")

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, fixedClock(),
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
		resolver, client, txStore, auditStore, alarm, fixedClock(),
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
		resolver, client, txStore, auditStore, alarm, fixedClock(),
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
		resolver, client, txStore, auditStore, alarm, fixedClock(),
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
