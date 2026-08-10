package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/fujiabao89/webdb/internal/pagination"
	"github.com/fujiabao89/webdb/internal/queryplan"
	"github.com/google/uuid"
)

// paginationAuditedSetup 构造启用分页 + 审计的 Pipeline，注入可观测 registry。
// 返回可注入审计失败的 txStore/auditStore 与 registry，便于验证审计失败后 token 撤销。
func paginationAuditedSetup(t *testing.T) (
	*Pipeline, AuthenticatedPrincipal, *metadata.Connection, *pagination.Registry, *fakeTxStore, *fakeAuditStore, *fakeAlarm,
) {
	t.Helper()
	principal := AuthenticatedPrincipal{UserID: uuid.New(), WorkspaceID: uuid.New()}
	conn := &metadata.Connection{
		ID:            uuid.New(),
		WorkspaceID:   principal.WorkspaceID,
		Engine:        metadata.EnginePostgreSQL,
		Host:          "db.example.invalid",
		Port:          5432,
		Database:      "synthetic",
		SecretRef:     uuid.New(),
		SecretVersion: 1,
		UpdatedAt:     time.Unix(1_700_000_000, 123_000),
	}
	policy := &metadata.ConnectionPolicy{
		WorkspaceID:        principal.WorkspaceID,
		ConnectionID:       conn.ID,
		AllowRead:          boolPtr(true),
		StatementTimeoutMs: 5_000,
		MaxRows:            500, // > 默认 PageSize=100 → 触发分页证明
		UpdatedAt:          time.Unix(1_700_000_000, 456_000),
	}
	resolver := &fakeResolver{
		payload: credentials.CredentialPayload{User: "synthetic_user", Password: "synthetic_password"},
	}
	handle := &fakeAdapterHandle{
		result: &adapter.QueryResult{HasMore: true, TotalReturned: 2, ReturnedRows: 2,
			Columns: []adapter.ColumnInfo{{Name: "id"}},
			Rows:    [][]any{{int32(1)}, {int32(2)}},
		},
		meta: &queryplan.TableMetadata{
			Schema: "public", Table: "employees",
			Columns:    []queryplan.Column{{Name: "id", Ordinal: 1, Nullable: false}},
			PrimaryKey: &queryplan.PrimaryKey{Columns: []string{"id"}},
		},
		currentSchema: "public",
	}
	client := &fakeAdapterClient{handle: handle}
	txStore := &fakeTxStore{}
	auditStore := &fakeAuditStore{}
	alarm := &fakeAlarm{}
	reg := pagination.New(pagination.DefaultConfig())
	t.Cleanup(reg.Close)
	pipeline := NewPipeline(PipelineConfig{
		Store:       &fakeConnectionReader{connections: []*metadata.Connection{conn}},
		PolicyStore: &fakePolicyReader{policy: policy},
		Members: &fakeMemberReader{
			member: &metadata.WorkspaceMember{WorkspaceID: principal.WorkspaceID, UserID: principal.UserID, Role: metadata.RoleViewer},
		},
		Resolver:   resolver,
		Adapter:    client,
		Tx:         txStore,
		Audit:      auditStore,
		Alarm:      alarm,
		Pagination: reg,
	})
	return pipeline, principal, conn, reg, txStore, auditStore, alarm
}

func paginationRequest(principal AuthenticatedPrincipal, connID uuid.UUID) ExecuteRequest {
	return ExecuteRequest{
		Principal:    principal,
		ConnectionID: connID,
		SQL:          "SELECT id FROM employees",
		Engine:       EnginePostgreSQL,
		SortKeys:     []queryplan.SortKey{{Column: "id", Direction: queryplan.SortAsc}},
		PageSize:     2,
	}
}

// assertContinuationRevokedAfterAuditFailure 断言审计失败后：结果不含 token、结果清空、
// registry 无可用/滞留 token、计数与字节容量全部释放。
func assertContinuationRevokedAfterAuditFailure(t *testing.T, result *ExecuteResult, reg *pagination.Registry) {
	t.Helper()
	if result.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed", result.ErrorCode)
	}
	if result.Result != nil {
		t.Fatal("audit failure must not return query result")
	}
	if result.NextPageToken != nil {
		t.Fatal("audit failure must not expose a continuation token")
	}
	s := reg.Stats()
	if s.ActiveTokens != 0 || s.InFlightTokens != 0 || s.GlobalBytes != 0 {
		t.Fatalf("registry not fully released after audit failure: %+v", s)
	}
}

func TestExecutePostExecutionAuditAppendFailureRevokesContinuation(t *testing.T) {
	// audit append 失败：首页 token 已创建但未发布，审计失败必须撤销 registry token，
	// 不向调用方暴露，且不泄漏容量槽位（Codex P1-B 场景 2/3/4/5）。
	pipeline, principal, conn, reg, txStore, auditStore, alarm := paginationAuditedSetup(t)
	auditStore.fail = errors.New("injected post audit failure")

	result, err := pipeline.Execute(context.Background(), paginationRequest(principal, conn.ID))
	if err == nil {
		t.Fatal("Execute() error = nil, want audit_failed")
	}
	assertContinuationRevokedAfterAuditFailure(t, result, reg)
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
	// execution 终态应先于审计独立提交（recordPostExecution 先提交终态再写审计）。
	updates := txStore.allUpdatedExecs()
	if len(updates) == 0 {
		t.Fatal("expected execution updates")
	}
	last := updates[len(updates)-1]
	if last.Status != metadata.ExecStatusCompleted {
		t.Fatalf("execution status = %s, want completed", last.Status)
	}
}

func TestExecutePostExecutionTerminalPersistenceFailureRevokesContinuation(t *testing.T) {
	// terminal execution 持久化失败（recordPostExecution 事务的 UpdateExecution 失败）：
	// 与审计写入失败同等 fail-closed，token 必须撤销且不发布（Codex P1-B 场景 1/3/4/5）。
	// 分页路径事务：tx1=阶段B pending、tx2=阶段D-0 running、tx3=recordPostExecution 终态。
	pipeline, principal, conn, reg, txStore, _, alarm := paginationAuditedSetup(t)
	txStore.failUpdate = errors.New("injected terminal update failure")
	txStore.failUpdateTxID = 3

	result, err := pipeline.Execute(context.Background(), paginationRequest(principal, conn.ID))
	if err == nil {
		t.Fatal("Execute() error = nil, want audit_failed")
	}
	assertContinuationRevokedAfterAuditFailure(t, result, reg)
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
}

func TestExecuteSuccessPublishesClaimableContinuation(t *testing.T) {
	// 正向对照：审计成功后 token 才发布且 registry 中可 Claim，
	// 证明断言方法能区分成功（可 Claim）与失败（不可 Claim）。
	pipeline, principal, conn, reg, _, _, _ := paginationAuditedSetup(t)
	result, err := pipeline.Execute(context.Background(), paginationRequest(principal, conn.ID))
	if err != nil {
		t.Fatalf("Execute() error = %v, code=%q", err, result.ErrorCode)
	}
	if result.NextPageToken == nil || *result.NextPageToken == "" {
		t.Fatal("expected continuation token on success")
	}
	if _, err := reg.Claim(*result.NextPageToken); err != nil {
		t.Fatalf("successful token not claimable: %v", err)
	}
}
