package execution

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/fujiabao89/webdb/internal/queryplan"
	"github.com/google/uuid"
)

// paginationSetup 构造启用分页的 Pipeline 与 fakes。
// paginationFixture 构造分页测试的公共 fixture（principal/conn/policy/resolver/client）。
// paginationSetup 与 paginationAuditSetup 共用，避免复制漂移（CodeRabbit 复审）。
func paginationFixture() (AuthenticatedPrincipal, *metadata.Connection, *metadata.ConnectionPolicy, *fakeResolver, *fakeAdapterClient) {
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
		MaxRows:            500,
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
		nextResult: &adapter.QueryResult{HasMore: false, TotalReturned: 3, ReturnedRows: 1,
			Columns: []adapter.ColumnInfo{{Name: "id"}},
			Rows:    [][]any{{int32(3)}},
		},
		meta: &queryplan.TableMetadata{
			Schema: "public", Table: "employees",
			Columns:    []queryplan.Column{{Name: "id", Ordinal: 1, Nullable: false}},
			PrimaryKey: &queryplan.PrimaryKey{Columns: []string{"id"}},
		},
		currentSchema: "public",
	}
	client := &fakeAdapterClient{handle: handle}
	return principal, conn, policy, resolver, client
}

func paginationSetup(t *testing.T) (*Pipeline, AuthenticatedPrincipal, *metadata.Connection, *metadata.ConnectionPolicy, *fakeAdapterClient, *fakeResolver) {
	t.Helper()
	principal, conn, policy, resolver, client := paginationFixture()
	pipeline := testPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		&fakeMemberReader{member: &metadata.WorkspaceMember{WorkspaceID: principal.WorkspaceID, UserID: principal.UserID, Role: metadata.RoleViewer}},
		resolver, client,
	)
	return pipeline, principal, conn, policy, client, resolver
}

func firstPageRequest(principal AuthenticatedPrincipal, connID uuid.UUID) ExecuteRequest {
	return ExecuteRequest{
		Principal:    principal,
		ConnectionID: connID,
		SQL:          "SELECT id FROM employees",
		Engine:       EnginePostgreSQL,
		SortKeys:     []queryplan.SortKey{{Column: "id", Direction: queryplan.SortAsc}},
		PageSize:     2,
	}
}

func TestExecuteCreatesContinuationWhenPaginationNeeded(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, _, _ := paginationSetup(t)
	result, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatalf("Execute() error = %v, code=%q", err, result.ErrorCode)
	}
	if result.NextPageToken == nil || *result.NextPageToken == "" {
		t.Fatal("expected continuation token when pagination required and more rows")
	}
}

func TestExecuteUnsupportedQueryWithoutSortKeys(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, client, _ := paginationSetup(t)
	req := firstPageRequest(principal, conn.ID)
	req.SortKeys = nil // 需要分页但无排序键 → unsupported_query
	result, err := pipeline.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("Execute() error = nil, want unsupported_query")
	}
	if result.ErrorCode != ErrUnsupportedQuery {
		t.Fatalf("code = %q, want %q", result.ErrorCode, ErrUnsupportedQuery)
	}
	if client.handle.calls != 0 {
		t.Fatalf("Adapter.Query called %d times, want 0 (用户查询未执行)", client.handle.calls)
	}
}

func TestExecuteUnqualifiedTableUsesCurrentSchema(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, client, _ := paginationSetup(t)
	// PG 未限定表名 → verifySortPlan 必须经 CurrentSchema 解析可信 schema
	client.handle.currentSchema = "app_schema"
	result, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatalf("Execute() error = %v, code=%q", err, result.ErrorCode)
	}
	if client.handle.metaSchema != "app_schema" {
		t.Fatalf("LoadTableMetadata schema = %q, want %q (from CurrentSchema)", client.handle.metaSchema, "app_schema")
	}
	if result.NextPageToken == nil {
		t.Fatal("expected continuation token")
	}
}

func TestExecuteCurrentSchemaErrorFailsClosed(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, client, _ := paginationSetup(t)
	client.handle.currentSchemaErr = errors.New("current_schema unavailable")
	result, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err == nil {
		t.Fatal("Execute() error = nil, want unsupported_query")
	}
	if result.ErrorCode != ErrUnsupportedQuery {
		t.Fatalf("code = %q, want unsupported_query", result.ErrorCode)
	}
	if client.handle.calls != 0 {
		t.Fatalf("Adapter.Query called %d times, want 0 (fail before query)", client.handle.calls)
	}
}

func TestExecuteCurrentSchemaEmptyFailsClosed(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, client, _ := paginationSetup(t)
	client.handle.currentSchema = ""
	result, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err == nil {
		t.Fatal("Execute() error = nil, want unsupported_query for empty current_schema")
	}
	if result.ErrorCode != ErrUnsupportedQuery {
		t.Fatalf("code = %q, want unsupported_query", result.ErrorCode)
	}
}

func TestExecuteUnsupportedQueryWhenSchemaUnavailable(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, client, _ := paginationSetup(t)
	client.handle.metaErr = errors.New("metadata unavailable")
	result, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err == nil {
		t.Fatal("Execute() error = nil, want unsupported_query")
	}
	if result.ErrorCode != ErrUnsupportedQuery {
		t.Fatalf("code = %q, want unsupported_query", result.ErrorCode)
	}
	if client.handle.calls != 0 {
		t.Fatalf("Adapter.Query called %d times, want 0", client.handle.calls)
	}
}

func TestNextPageReauthorizesAndRotates(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, client, _ := paginationSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	r2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	if err != nil {
		t.Fatalf("ExecuteNextPage() error = %v, code=%q", err, r2.ErrorCode)
	}
	if client.handle.nextCalls != 1 {
		t.Fatalf("Adapter.NextPage calls = %d, want 1", client.handle.nextCalls)
	}
	if r2.NextPageToken != nil {
		t.Fatal("no further token expected when has_more=false")
	}
}

func TestNextPageReplayRejected(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, _, _ := paginationSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken}); err != nil {
		t.Fatalf("first NextPage: %v", err)
	}
	r2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	if err == nil {
		t.Fatal("replay NextPage succeeded, want invalid_page_token")
	}
	if r2.ErrorCode != ErrInvalidPageToken {
		t.Fatalf("replay code = %q, want %q", r2.ErrorCode, ErrInvalidPageToken)
	}
}

func TestNextPageDifferentUserRejected(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, _, _ := paginationSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatal(err)
	}
	// 同工作区异用户 B 持有 A 的 handle → 必须拒绝（ADR-015 §4 绑定 principal）
	other := principal
	other.UserID = uuid.New()
	r2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: other, Token: *r1.NextPageToken})
	if err == nil {
		t.Fatal("different user in same workspace: NextPage succeeded, want invalid_page_token")
	}
	if r2.ErrorCode != ErrInvalidPageToken {
		t.Fatalf("code = %q, want %q", r2.ErrorCode, ErrInvalidPageToken)
	}
	// 被拒后 A 的旧 token 不可复用
	if _, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken}); err == nil {
		t.Fatal("token reusable after cross-user rejection")
	}
}

func TestNextPageDifferentWorkspaceRejected(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, _, _ := paginationSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatal(err)
	}
	other := principal
	other.WorkspaceID = uuid.New()
	other.UserID = uuid.New()
	r2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: other, Token: *r1.NextPageToken})
	if err == nil {
		t.Fatal("different workspace: NextPage succeeded, want invalid_page_token")
	}
	if r2.ErrorCode != ErrInvalidPageToken {
		t.Fatalf("code = %q, want %q", r2.ErrorCode, ErrInvalidPageToken)
	}
	// 被拒后旧 token 不可复用
	if _, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken}); err == nil {
		t.Fatal("token reusable after cross-workspace rejection")
	}
}

func TestNextPageMemberRevokedInvalidatesToken(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, _, _ := paginationSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatal(err)
	}
	members := pipeline.members.(*fakeMemberReader)
	members.err = sql.ErrNoRows
	r2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	if err == nil {
		t.Fatal("member revoked: NextPage succeeded")
	}
	if r2.ErrorCode != ErrForbidden {
		t.Fatalf("code = %q, want %q", r2.ErrorCode, ErrForbidden)
	}
	r3, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	if err == nil {
		t.Fatal("revoked token reusable after failure")
	}
	_ = r3
}

func TestNextPagePolicyVersionChangeInvalidatesToken(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, policy, _, _ := paginationSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatal(err)
	}
	policy.UpdatedAt = policy.UpdatedAt.Add(time.Microsecond)
	r2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	if err == nil {
		t.Fatal("policy version change: NextPage succeeded")
	}
	if r2.ErrorCode != ErrInvalidPageToken {
		t.Fatalf("code = %q, want invalid_page_token", r2.ErrorCode)
	}
}

func TestNextPageSchemaGenerationChangeInvalidatesToken(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, client, _ := paginationSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatal(err)
	}
	client.handle.meta.Columns = append(client.handle.meta.Columns, queryplan.Column{Name: "extra", Ordinal: 2, Nullable: true})
	r2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	if err == nil {
		t.Fatal("schema generation change: NextPage succeeded")
	}
	if r2.ErrorCode != ErrInvalidPageToken {
		t.Fatalf("code = %q, want invalid_page_token", r2.ErrorCode)
	}
}

func TestNextPagePoolGenerationChangeInvalidatesToken(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, client, _ := paginationSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatal(err)
	}
	client.handle.poolGeneration = 99
	r2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	if err == nil {
		t.Fatal("pool generation change: NextPage succeeded")
	}
	if r2.ErrorCode != ErrInvalidPageToken {
		t.Fatalf("code = %q, want invalid_page_token", r2.ErrorCode)
	}
}

func TestNextPageClaimAbortedOnPanic(t *testing.T) {
	// claim 后 adapter 调用 panic 时，幂等 deferred claim.Abort() 必须释放 in-flight
	// token（容量/计数归零），否则 token 一直保持 in-flight 直到 TTL（Greptile P2）。
	pipeline, principal, conn, _, client, _ := paginationSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	client.handle.panicNextPage = true

	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected panic from adapter NextPage")
			}
		}()
		_, _ = pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	}()

	s := pipeline.registry.Stats()
	if s.ActiveTokens != 0 || s.InFlightTokens != 0 || s.GlobalBytes != 0 {
		t.Fatalf("registry not cleaned after panic: active=%d in_flight=%d bytes=%d",
			s.ActiveTokens, s.InFlightTokens, s.GlobalBytes)
	}
}

func TestNextPageAdapterErrorAbortsToken(t *testing.T) {
	t.Parallel()
	pipeline, principal, conn, _, client, _ := paginationSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatal(err)
	}
	client.handle.err = &adapter.AdapterError{Code: adapter.ErrDatabaseError}
	r2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	if err == nil {
		t.Fatal("adapter error: NextPage succeeded")
	}
	client.handle.err = nil
	r3, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	if err == nil {
		t.Fatal("token reusable after adapter failure")
	}
	if r3.ErrorCode != ErrInvalidPageToken {
		t.Fatalf("post-failure code = %q, want invalid_page_token", r3.ErrorCode)
	}
	_ = r2
}

// paginationAuditSetup 类似 paginationSetup，但返回可断言的 fakeTxStore/fakeAuditStore
// （供续页失败终态审计断言，Codex P1）。
func paginationAuditSetup(t *testing.T) (*Pipeline, AuthenticatedPrincipal, *metadata.Connection, *fakeTxStore, *fakeAuditStore, *fakeAdapterClient) {
	t.Helper()
	principal, conn, policy, resolver, client := paginationFixture()
	txStore := &fakeTxStore{}
	auditStore := &fakeAuditStore{}
	alarm := &fakeAlarm{}
	pipeline := NewPipeline(PipelineConfig{
		Store:       &fakeConnectionReader{connections: []*metadata.Connection{conn}},
		PolicyStore: &fakePolicyReader{policy: policy},
		Members: &fakeMemberReader{member: &metadata.WorkspaceMember{
			WorkspaceID: principal.WorkspaceID, UserID: principal.UserID, Role: metadata.RoleViewer}},
		Resolver: resolver,
		Adapter:  client,
		Tx:       txStore,
		Audit:    auditStore,
		Alarm:    alarm,
	})
	return pipeline, principal, conn, txStore, auditStore, client
}

// TestNextPageLoadTableMetadataFailureFinalizes 验证续页在 LoadTableMetadata 访问
// 目标库后失败时（Codex P1/D11）：Execution 已创建且为 failed 终态、AuditEvent 已追加、
// token 不可复用、不返回结果或新 token。
func TestNextPageLoadTableMetadataFailureFinalizes(t *testing.T) {
	pipeline, principal, conn, txStore, auditStore, client := paginationAuditSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if r1.NextPageToken == nil {
		t.Fatal("first page should issue continuation token")
	}

	// 注入 LoadTableMetadata 失败（续页目标库访问后失败）。
	client.handle.metaErr = errors.New("injected schema load failure")

	r2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	if err == nil || r2.ErrorCode != ErrInvalidPageToken {
		t.Fatalf("ExecuteNextPage err=%v code=%q, want invalid_page_token", err, r2.ErrorCode)
	}
	if r2.Result != nil {
		t.Fatal("failed page must not return result")
	}
	if r2.NextPageToken != nil {
		t.Fatal("failed page must not return new token")
	}
	// 失败路径不得调用 Adapter.NextPage（目标库执行 0 次；LoadTableMetadata 失败在
	// 执行前收敛，CodeRabbit）。
	if client.handle.nextCalls != 0 {
		t.Fatalf("Adapter.NextPage calls = %d, want 0（失败路径不得访问目标库执行）", client.handle.nextCalls)
	}

	execs := txStore.allUpdatedExecs()
	if len(execs) == 0 {
		t.Fatal("continuation execution must be created")
	}
	if last := execs[len(execs)-1]; last.Status != metadata.ExecStatusFailed {
		t.Fatalf("execution status = %q, want failed", last.Status)
	}

	if len(auditStore.events) == 0 {
		t.Fatal("continuation audit event must be appended")
	}
	if ev := auditStore.events[len(auditStore.events)-1]; ev.Outcome != metadata.OutcomeFailed {
		t.Fatalf("audit outcome = %q, want failed", ev.Outcome)
	}

	// token 不可复用（claim 后失败不恢复）。
	if _, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken}); err == nil {
		t.Fatal("replay after failure should be rejected")
	}
}

// TestNextPageCredentialFailureFinalizes verifies that re-authentication
// failures are fail-closed: they consume the continuation, create a failed
// execution, and append the credential audit event even after client cancel.
func TestNextPageCredentialFailureFinalizes(t *testing.T) {
	pipeline, principal, conn, txStore, _, client := paginationAuditSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if r1.NextPageToken == nil {
		t.Fatal("first page should issue continuation token")
	}

	resolver, ok := pipeline.resolver.(*fakeResolver)
	if !ok {
		t.Fatalf("resolver type = %T, want *fakeResolver", pipeline.resolver)
	}
	resolver.err = credentials.ErrCredentialRetired
	txStore.rejectCanceledContext = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r2, err := pipeline.ExecuteNextPage(ctx, NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	if err == nil || r2.ErrorCode != StableErrorCode(credentials.ErrCredentialRetired) {
		t.Fatalf("ExecuteNextPage err=%v code=%q, want credential_retired", err, r2.ErrorCode)
	}
	if r2.Result != nil || r2.NextPageToken != nil {
		t.Fatal("credential failure must not return a result or continuation token")
	}
	if client.handle.nextCalls != 0 {
		t.Fatalf("Adapter.NextPage calls = %d, want 0", client.handle.nextCalls)
	}

	updates := txStore.allUpdatedExecs()
	if len(updates) == 0 || updates[len(updates)-1].Status != metadata.ExecStatusFailed {
		t.Fatal("credential failure must finalize its continuation execution as failed")
	}
	events := txStore.allAuditEvents()
	if len(events) == 0 {
		t.Fatal("credential failure must append an audit event")
	}
	last := events[len(events)-1]
	if last.Action != metadata.ActionCredentialLookup || last.Outcome != metadata.OutcomeFailed || last.ActorType != metadata.ActorTypeSystem || last.ExecutionID != nil {
		t.Fatalf("credential audit = %#v, want failed system credential.lookup without execution ID", last)
	}

	if _, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken}); err == nil {
		t.Fatal("replay after credential failure should be rejected")
	}
}

// TestNextPageLoadTableMetadataPanicFinalizes 验证续页预检 LoadTableMetadata panic 后
// （CodeRabbit P1）：统一 panic finalizer 终结 Execution（failed）+ 追加失败 AuditEvent，
// token 不可复用。
func TestNextPageLoadTableMetadataPanicFinalizes(t *testing.T) {
	pipeline, principal, conn, txStore, auditStore, client := paginationAuditSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if r1.NextPageToken == nil {
		t.Fatal("first page should issue continuation token")
	}

	// 注入 LoadTableMetadata panic（预检阶段）。
	client.handle.panicMeta = true

	func() {
		defer func() {
			if rec := recover(); rec == nil {
				t.Fatal("LoadTableMetadata panic 应向上传播")
			}
		}()
		_, _ = pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	}()

	execs := txStore.allUpdatedExecs()
	if len(execs) == 0 {
		t.Fatal("continuation execution must be created")
	}
	if last := execs[len(execs)-1]; last.Status != metadata.ExecStatusFailed {
		t.Fatalf("execution status = %q, want failed", last.Status)
	}
	if len(auditStore.events) == 0 {
		t.Fatal("continuation audit event must be appended")
	}
	if ev := auditStore.events[len(auditStore.events)-1]; ev.Outcome != metadata.OutcomeFailed {
		t.Fatalf("audit outcome = %q, want failed", ev.Outcome)
	}
	if _, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken}); err == nil {
		t.Fatal("replay after panic should be rejected")
	}
}

// TestNextPageConfigRevisionFailureFinalizes 验证续页 configRevision 失败后（CodeRabbit
// P1）：Execution 已创建且为 failed 终态、失败 AuditEvent 已追加。
func TestNextPageConfigRevisionFailureFinalizes(t *testing.T) {
	pipeline, principal, conn, txStore, auditStore, _ := paginationAuditSetup(t)
	r1, err := pipeline.Execute(context.Background(), firstPageRequest(principal, conn.ID))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if r1.NextPageToken == nil {
		t.Fatal("first page should issue continuation token")
	}

	// connectionConfigRevision 失败：conn.UpdatedAt 设为零值。
	conn.UpdatedAt = time.Time{}

	r2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *r1.NextPageToken})
	if err == nil || r2.ErrorCode != ErrInternalError {
		t.Fatalf("ExecuteNextPage err=%v code=%q, want internal_error", err, r2.ErrorCode)
	}
	execs := txStore.allUpdatedExecs()
	if len(execs) == 0 {
		t.Fatal("continuation execution must be created")
	}
	if last := execs[len(execs)-1]; last.Status != metadata.ExecStatusFailed {
		t.Fatalf("execution status = %q, want failed", last.Status)
	}
	if len(auditStore.events) == 0 {
		t.Fatal("continuation audit event must be appended")
	}
	if ev := auditStore.events[len(auditStore.events)-1]; ev.Outcome != metadata.OutcomeFailed {
		t.Fatalf("audit outcome = %q, want failed", ev.Outcome)
	}
}
