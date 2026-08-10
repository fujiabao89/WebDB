package execution

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/fujiabao89/webdb/internal/pagination"
	"github.com/fujiabao89/webdb/internal/queryplan"
	"github.com/google/uuid"
)

// TestAuditedExecute_MetadataNoCanary 验证审计 metadata 不含 SQL 正文、密码或结果。
func TestAuditedExecute_MetadataNoCanary(t *testing.T) {
	canarySQL := "SELECT 'sup3rsecret-pw-42' FROM users WHERE password = 'hunter2'"
	canaryPassword := "canary-db-password-!@#$%"

	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	resolver.payload = credentials.CredentialPayload{User: "synthetic_user", Password: canaryPassword}
	client.handle.result = &adapter.QueryResult{TotalReturned: 1}

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	_, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          canarySQL,
		Engine:       EnginePostgreSQL,
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	if len(auditStore.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(auditStore.events))
	}
	md := string(auditStore.events[0].Metadata)
	if strings.Contains(md, canarySQL) {
		t.Fatalf("audit metadata contains SQL body: %s", md)
	}
	if strings.Contains(md, canaryPassword) {
		t.Fatalf("audit metadata contains plaintext password: %s", md)
	}
	if strings.Contains(md, "hunter2") {
		t.Fatalf("audit metadata contains canary password fragment: %s", md)
	}
	if strings.Contains(md, "SELECT") {
		t.Fatalf("audit metadata must not contain SQL keywords: %s", md)
	}
}

// TestAuditedExecute_ErrorNoCanary 验证错误路径不含 SQL 正文或密码。
func TestAuditedExecute_ErrorNoCanary(t *testing.T) {
	canaryPassword := "canary-db-password-!@#$%"

	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	resolver.payload = credentials.CredentialPayload{User: "synthetic_user", Password: canaryPassword}
	client.handle.err = &adapter.AdapterError{Code: adapter.ErrDatabaseError}

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
	if strings.Contains(err.Error(), canaryPassword) {
		t.Fatalf("error message contains password: %v", err)
	}
	if strings.Contains(err.Error(), "SELECT 1") {
		t.Fatalf("error message contains SQL: %v", err)
	}
	if strings.Contains(string(result.ErrorCode), canaryPassword) {
		t.Fatalf("stable error code contains password: %v", result.ErrorCode)
	}
}

// TestAuditedExecute_NextPageAuditNoCanary 验证续页（D11）审计通道的敏感 canary：
// 首页与续页的 AuditEvent metadata 均不得包含 SQL 明文、密码、token 或 host
// （P0-06A §11.2 / ADR-017 禁止字段；Owner P2-3 决策：本轮补充实现）。
func TestAuditedExecute_NextPageAuditNoCanary(t *testing.T) {
	canarySQL := "SELECT id, password FROM users WHERE password = 'hunter2' ORDER BY id"
	canaryPassword := "canary-db-password-!@#$%"
	canaryHost := "canary-db.example.invalid"

	principal := AuthenticatedPrincipal{UserID: uuid.New(), WorkspaceID: uuid.New()}
	conn := &metadata.Connection{
		ID: uuid.New(), WorkspaceID: principal.WorkspaceID, Engine: metadata.EnginePostgreSQL,
		Host: canaryHost, Port: 5432, Database: "synthetic",
		SecretRef: uuid.New(), SecretVersion: 1, UpdatedAt: time.Unix(1_700_000_000, 123_000),
	}
	policy := &metadata.ConnectionPolicy{
		WorkspaceID: principal.WorkspaceID, ConnectionID: conn.ID, AllowRead: boolPtr(true),
		StatementTimeoutMs: 5_000, MaxRows: 500, UpdatedAt: time.Unix(1_700_000_000, 456_000),
	}
	resolver := &fakeResolver{payload: credentials.CredentialPayload{User: "synthetic_user", Password: canaryPassword}}
	handle := &fakeAdapterHandle{
		result: &adapter.QueryResult{HasMore: true, TotalReturned: 2, ReturnedRows: 2,
			Columns: []adapter.ColumnInfo{{Name: "id"}}, Rows: [][]any{{int32(1)}, {int32(2)}}},
		nextResult: &adapter.QueryResult{HasMore: false, TotalReturned: 4, ReturnedRows: 2,
			Columns: []adapter.ColumnInfo{{Name: "id"}}, Rows: [][]any{{int32(3)}, {int32(4)}}},
		meta: &queryplan.TableMetadata{Schema: "public", Table: "users",
			Columns:    []queryplan.Column{{Name: "id", Ordinal: 1, Nullable: false}},
			PrimaryKey: &queryplan.PrimaryKey{Columns: []string{"id"}}},
		currentSchema: "public",
	}
	auditStore := &fakeAuditStore{}
	reg := pagination.New(pagination.DefaultConfig())
	t.Cleanup(reg.Close)
	pipeline := NewPipeline(PipelineConfig{
		Store:       &fakeConnectionReader{connections: []*metadata.Connection{conn}},
		PolicyStore: &fakePolicyReader{policy: policy},
		Members:     &fakeMemberReader{member: &metadata.WorkspaceMember{WorkspaceID: principal.WorkspaceID, UserID: principal.UserID, Role: metadata.RoleViewer}},
		Resolver:    resolver, Adapter: &fakeAdapterClient{handle: handle},
		Tx: &fakeTxStore{}, Audit: auditStore, Alarm: &fakeAlarm{}, Pagination: reg,
	})
	req := ExecuteRequest{
		Principal: principal, ConnectionID: conn.ID, SQL: canarySQL, Engine: EnginePostgreSQL,
		SortKeys: []queryplan.SortKey{{Column: "id", Direction: queryplan.SortAsc}}, PageSize: 2,
	}
	res1, err := pipeline.Execute(context.Background(), req)
	if err != nil || res1.NextPageToken == nil {
		t.Fatalf("首页应成功并返回 token: err=%v", err)
	}
	res2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *res1.NextPageToken})
	if err != nil {
		t.Fatalf("续页失败: %v", err)
	}
	if len(auditStore.events) < 2 {
		t.Fatalf("audit events = %d, want >=2（首页+续页各一个）", len(auditStore.events))
	}
	for i, ev := range auditStore.events {
		md := string(ev.Metadata)
		if strings.Contains(md, canarySQL) {
			t.Fatalf("audit[%d] metadata contains SQL body: %s", i, md)
		}
		if strings.Contains(md, canaryPassword) {
			t.Fatalf("audit[%d] metadata contains plaintext password: %s", i, md)
		}
		if strings.Contains(md, "hunter2") {
			t.Fatalf("audit[%d] metadata contains canary password fragment: %s", i, md)
		}
		if strings.Contains(md, canaryHost) {
			t.Fatalf("audit[%d] metadata contains host: %s", i, md)
		}
		if strings.Contains(md, "SELECT") {
			t.Fatalf("audit[%d] metadata must not contain SQL keywords: %s", i, md)
		}
	}
	// token 为 opaque handle，不进入审计（audit metadata 无 token 字段；statement_hash 为摘要）。
	_ = res2
}

// TestStderrAlarmOutput 验证 $SECURITY_ALERT 输出不含敏感输入且为结构化字段。
func TestStderrAlarmOutput(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	// 恢复原 writer，而非 SetOutput(nil)，避免后续 NewStderrAlarm 的 log.Print panic（Qodo #5 / CodeRabbit #20）。
	t.Cleanup(func() { log.SetOutput(old) })

	alarm := NewStderrAlarm()
	alarm.Alarm(context.Background(), metadata.SecurityAlertEvent{
		TraceID:     "trace-1",
		WorkspaceID: uuidV4(),
		Code:        "audit_failed",
		OccurredAt:  time.Unix(1_700_000_000, 0).UTC(),
	})

	out := buf.String()
	if !strings.Contains(out, "$SECURITY_ALERT") {
		t.Fatalf("stderr alarm missing marker: %q", out)
	}
	// 结构化 key=value 字段便于监控聚合（CodeRabbit #28）。
	for _, field := range []string{"trace=trace-1", "workspace=", "code=audit_failed", "occurred_at=2023-11-14T22:13:20Z"} {
		if !strings.Contains(out, field) {
			t.Fatalf("stderr alarm missing structured field %q: %q", field, out)
		}
	}
	for _, sensitive := range []string{"sup3rsecret", "hunter2", "password=", "kek="} {
		if strings.Contains(out, sensitive) {
			t.Fatalf("stderr alarm leaked sensitive data %q: %q", sensitive, out)
		}
	}
}

func uuidV4() uuid.UUID {
	return uuid.MustParse("123e4567-e89b-42d3-a456-426614174000")
}
