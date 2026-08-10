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

// TestNextPageCancelledTerminatesExecutionAndAudits 验证续页查询被取消时（D13
// transport abort）：ctx 已取消，但 Execution 仍终结为 cancelled、E13 审计写入、
// 错误码保持 query_cancelled（P1-2 审查修复：auditNextPage 用 WithoutCancel auditCtx）。
func TestNextPageCancelledTerminatesExecutionAndAudits(t *testing.T) {
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
	resolver := &fakeResolver{payload: credentials.CredentialPayload{User: "synthetic_user", Password: "synthetic_password"}}
	// 查询中取消：首页 Query 成功；续页 NextPage 返回 context.Canceled
	//（模拟 transport abort 传播到目标库）。
	handle := &fakeAdapterHandle{
		result: &adapter.QueryResult{HasMore: true, TotalReturned: 2, ReturnedRows: 2,
			Columns: []adapter.ColumnInfo{{Name: "id"}}, Rows: [][]any{{int32(1)}, {int32(2)}}},
		meta:          &queryplan.TableMetadata{Schema: "public", Table: "employees", Columns: []queryplan.Column{{Name: "id", Ordinal: 1, Nullable: false}}, PrimaryKey: &queryplan.PrimaryKey{Columns: []string{"id"}}},
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
		Members: &fakeMemberReader{member: &metadata.WorkspaceMember{
			WorkspaceID: principal.WorkspaceID, UserID: principal.UserID, Role: metadata.RoleViewer}},
		Resolver:   resolver,
		Adapter:    client,
		Tx:         txStore,
		Audit:      auditStore,
		Alarm:      alarm,
		Pagination: reg,
	})

	// 首页成功取得 token。
	res1, err := pipeline.Execute(context.Background(), paginationRequest(principal, conn.ID))
	if err != nil || res1.NextPageToken == nil {
		t.Fatalf("首页应成功并返回 token: err=%v", err)
	}

	// 续页阶段注入取消（查询中 transport abort）。
	handle.err = context.Canceled

	// 请求 context 已取消（transport abort）。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res2, err := pipeline.ExecuteNextPage(ctx, NextPageRequest{Principal: principal, Token: *res1.NextPageToken})
	if err == nil {
		t.Fatal("续页查询被取消应返回错误")
	}
	if res2.ErrorCode != ErrQueryCancelled {
		t.Fatalf("error code = %q, want query_cancelled（P1-2：不得被审计失败覆盖为 audit_failed）", res2.ErrorCode)
	}
	// 未提交的 pending/running Execution 不得遗留：execution 必须终结为 cancelled。
	execs := txStore.allUpdatedExecs()
	if len(execs) == 0 {
		t.Fatal("续页取消后应有 Execution 终态记录")
	}
	last := execs[len(execs)-1]
	if last.Status != metadata.ExecStatusCancelled {
		t.Fatalf("execution status = %s, want cancelled（D13 义务 3）", last.Status)
	}
	// E13 审计（outcome=cancelled）必须写入（auditCtx 不受 request ctx 取消影响）。
	var cancelledEvent bool
	for _, ev := range auditStore.events {
		if ev.Outcome == metadata.OutcomeCancelled {
			cancelledEvent = true
			break
		}
	}
	if !cancelledEvent {
		t.Fatal("续页取消后应有 outcome=cancelled 的 E13 审计事件（D13 义务 4）")
	}
	// 旧 token 不恢复：claim 已被 abort。
	if s := reg.Stats(); s.ActiveTokens != 0 || s.InFlightTokens != 0 {
		t.Fatalf("续页取消后 token 不应恢复/滞留: %+v", s)
	}
}

// TestNextPageFailureAuditAppendFailureFailClosedToAuditFailed 验证续页失败页
// （查询取消）的审计追加也失败时，不得静默声称 query_cancelled 已完整审计：
// fail-closed 返回 audit_failed、触发安全告警、不返回结果/token（ADR-017 /
// P0-06A §11.2，Greptile P1 / CodeRabbit #10）。
func TestNextPageFailureAuditAppendFailureFailClosedToAuditFailed(t *testing.T) {
	principal := AuthenticatedPrincipal{UserID: uuid.New(), WorkspaceID: uuid.New()}
	conn := &metadata.Connection{
		ID: uuid.New(), WorkspaceID: principal.WorkspaceID, Engine: metadata.EnginePostgreSQL,
		Host: "db.example.invalid", Port: 5432, Database: "synthetic",
		SecretRef: uuid.New(), SecretVersion: 1, UpdatedAt: time.Unix(1_700_000_000, 123_000),
	}
	policy := &metadata.ConnectionPolicy{
		WorkspaceID: principal.WorkspaceID, ConnectionID: conn.ID, AllowRead: boolPtr(true),
		StatementTimeoutMs: 5_000, MaxRows: 500, UpdatedAt: time.Unix(1_700_000_000, 456_000),
	}
	resolver := &fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}}
	handle := &fakeAdapterHandle{
		result: &adapter.QueryResult{HasMore: true, TotalReturned: 2, ReturnedRows: 2,
			Columns: []adapter.ColumnInfo{{Name: "id"}}, Rows: [][]any{{int32(1)}, {int32(2)}}},
		meta:          &queryplan.TableMetadata{Schema: "public", Table: "employees", Columns: []queryplan.Column{{Name: "id", Ordinal: 1, Nullable: false}}, PrimaryKey: &queryplan.PrimaryKey{Columns: []string{"id"}}},
		currentSchema: "public",
	}
	txStore := &fakeTxStore{}
	auditStore := &fakeAuditStore{}
	alarm := &fakeAlarm{}
	reg := pagination.New(pagination.DefaultConfig())
	t.Cleanup(reg.Close)
	pipeline := NewPipeline(PipelineConfig{
		Store:       &fakeConnectionReader{connections: []*metadata.Connection{conn}},
		PolicyStore: &fakePolicyReader{policy: policy},
		Members:     &fakeMemberReader{member: &metadata.WorkspaceMember{WorkspaceID: principal.WorkspaceID, UserID: principal.UserID, Role: metadata.RoleViewer}},
		Resolver:    resolver, Adapter: &fakeAdapterClient{handle: handle},
		Tx: txStore, Audit: auditStore, Alarm: alarm, Pagination: reg,
	})
	res1, err := pipeline.Execute(context.Background(), paginationRequest(principal, conn.ID))
	if err != nil || res1.NextPageToken == nil {
		t.Fatalf("首页应成功返回 token: err=%v", err)
	}
	// 续页阶段注入查询取消 + 审计追加失败。
	handle.err = context.Canceled
	auditStore.fail = errors.New("injected audit append failure")
	res2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *res1.NextPageToken})
	if err == nil {
		t.Fatal("续页审计持久化失败必须返回错误")
	}
	if res2.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed（fail-closed，不得声称 query_cancelled 已完整审计）", res2.ErrorCode)
	}
	if res2.Result != nil || res2.NextPageToken != nil {
		t.Fatal("审计失败不得返回结果或 token")
	}
	// $SECURITY_ALERT 必须触发（审计失败的可观测信号）。
	if len(alarm.events) == 0 {
		t.Fatal("审计持久化失败必须触发安全告警")
	}
}

// TestNextPageFailureTerminalUpdateFailureFailClosedToAuditFailed 验证续页失败页
// 的 Execution 终态更新失败（元数据库故障）时同样 fail-closed 为 audit_failed，
// 而不是静默保持 query_cancelled 并遗留已提交的 pending Execution（Greptile P1）。
// 注入点语义定位：failUpdateN 按 UpdateExecution 调用次数（非事务序号）注入——
// 首页 running 更新=#1、首页终态更新=#2、续页终态更新=#3；增删非更新事务不会
// 漂移注入点（CodeRabbit 新 #4）。
func TestNextPageFailureTerminalUpdateFailureFailClosedToAuditFailed(t *testing.T) {
	principal := AuthenticatedPrincipal{UserID: uuid.New(), WorkspaceID: uuid.New()}
	conn := &metadata.Connection{
		ID: uuid.New(), WorkspaceID: principal.WorkspaceID, Engine: metadata.EnginePostgreSQL,
		Host: "db.example.invalid", Port: 5432, Database: "synthetic",
		SecretRef: uuid.New(), SecretVersion: 1, UpdatedAt: time.Unix(1_700_000_000, 123_000),
	}
	policy := &metadata.ConnectionPolicy{
		WorkspaceID: principal.WorkspaceID, ConnectionID: conn.ID, AllowRead: boolPtr(true),
		StatementTimeoutMs: 5_000, MaxRows: 500, UpdatedAt: time.Unix(1_700_000_000, 456_000),
	}
	resolver := &fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}}
	handle := &fakeAdapterHandle{
		result: &adapter.QueryResult{HasMore: true, TotalReturned: 2, ReturnedRows: 2,
			Columns: []adapter.ColumnInfo{{Name: "id"}}, Rows: [][]any{{int32(1)}, {int32(2)}}},
		meta:          &queryplan.TableMetadata{Schema: "public", Table: "employees", Columns: []queryplan.Column{{Name: "id", Ordinal: 1, Nullable: false}}, PrimaryKey: &queryplan.PrimaryKey{Columns: []string{"id"}}},
		currentSchema: "public",
	}
	txStore := &fakeTxStore{}
	auditStore := &fakeAuditStore{}
	alarm := &fakeAlarm{}
	reg := pagination.New(pagination.DefaultConfig())
	t.Cleanup(reg.Close)
	pipeline := NewPipeline(PipelineConfig{
		Store:       &fakeConnectionReader{connections: []*metadata.Connection{conn}},
		PolicyStore: &fakePolicyReader{policy: policy},
		Members:     &fakeMemberReader{member: &metadata.WorkspaceMember{WorkspaceID: principal.WorkspaceID, UserID: principal.UserID, Role: metadata.RoleViewer}},
		Resolver:    resolver, Adapter: &fakeAdapterClient{handle: handle},
		Tx: txStore, Audit: auditStore, Alarm: alarm, Pagination: reg,
	})
	res1, err := pipeline.Execute(context.Background(), paginationRequest(principal, conn.ID))
	if err != nil || res1.NextPageToken == nil {
		t.Fatalf("首页应成功返回 token: err=%v", err)
	}
	// 续页阶段注入查询取消 + 终态 Execution 更新失败（第 3 次 UpdateExecution 调用）。
	handle.err = context.Canceled
	txStore.failUpdateN = 3
	txStore.failUpdate = errors.New("injected terminal update failure")
	res2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *res1.NextPageToken})
	if err == nil {
		t.Fatal("续页终态更新失败必须返回错误")
	}
	if res2.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed（终态更新失败即审计持久化失败）", res2.ErrorCode)
	}
	if res2.Result != nil || res2.NextPageToken != nil {
		t.Fatal("终态更新失败不得返回结果或 token（与审计 append 失败路径一致，CodeRabbit 新 #4）")
	}
	if len(alarm.events) == 0 {
		t.Fatal("终态更新失败必须触发安全告警")
	}
}

// TestNextPageSuccessAuditFailureWithholdsResultAndToken 验证续页成功页审计失败时
// 扣留结果与 token 并返回 audit_failed（P1-R1 修复：不得返回 200 + 结果 + 空 receipt）。
func TestNextPageSuccessAuditFailureWithholdsResultAndToken(t *testing.T) {
	principal := AuthenticatedPrincipal{UserID: uuid.New(), WorkspaceID: uuid.New()}
	conn := &metadata.Connection{
		ID: uuid.New(), WorkspaceID: principal.WorkspaceID, Engine: metadata.EnginePostgreSQL,
		Host: "db.example.invalid", Port: 5432, Database: "synthetic",
		SecretRef: uuid.New(), SecretVersion: 1, UpdatedAt: time.Unix(1_700_000_000, 123_000),
	}
	policy := &metadata.ConnectionPolicy{
		WorkspaceID: principal.WorkspaceID, ConnectionID: conn.ID, AllowRead: boolPtr(true),
		StatementTimeoutMs: 5_000, MaxRows: 500, UpdatedAt: time.Unix(1_700_000_000, 456_000),
	}
	resolver := &fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}}
	// 首页与续页查询均成功；续页审计写入失败。
	handle := &fakeAdapterHandle{
		result: &adapter.QueryResult{HasMore: true, TotalReturned: 2, ReturnedRows: 2,
			Columns: []adapter.ColumnInfo{{Name: "id"}}, Rows: [][]any{{int32(1)}, {int32(2)}}},
		nextResult: &adapter.QueryResult{HasMore: false, TotalReturned: 4, ReturnedRows: 2,
			Columns: []adapter.ColumnInfo{{Name: "id"}}, Rows: [][]any{{int32(3)}, {int32(4)}}},
		meta:          &queryplan.TableMetadata{Schema: "public", Table: "employees", Columns: []queryplan.Column{{Name: "id", Ordinal: 1, Nullable: false}}, PrimaryKey: &queryplan.PrimaryKey{Columns: []string{"id"}}},
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
	res1, err := pipeline.Execute(context.Background(), paginationRequest(principal, conn.ID))
	if err != nil || res1.NextPageToken == nil {
		t.Fatalf("首页应成功返回 token: err=%v", err)
	}
	// 续页成功查询，但审计写入失败 → 必须扣留结果/token 并返回 audit_failed（P1-R1）。
	auditStore.fail = errors.New("injected audit failure")
	res2, err := pipeline.ExecuteNextPage(context.Background(), NextPageRequest{Principal: principal, Token: *res1.NextPageToken})
	if err == nil {
		t.Fatal("成功页审计失败必须返回错误")
	}
	if res2.ErrorCode != ErrAuditFailed {
		t.Fatalf("error code = %q, want audit_failed（ADR-017 扣留）", res2.ErrorCode)
	}
	if res2.Result != nil {
		t.Fatal("审计失败不得返回查询结果")
	}
	if res2.NextPageToken != nil {
		t.Fatal("审计失败不得返回新的续页 token")
	}
	if s := reg.Stats(); s.ActiveTokens != 0 || s.InFlightTokens != 0 {
		t.Fatalf("审计失败后 registry 不应滞留 token: %+v", s)
	}
}
