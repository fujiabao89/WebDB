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
func paginationSetup(t *testing.T) (*Pipeline, AuthenticatedPrincipal, *metadata.Connection, *metadata.ConnectionPolicy, *fakeAdapterClient, *fakeResolver) {
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
