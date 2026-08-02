package connections

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- mocks ----------------------------------------------------------------

type fakeConnectionStore struct {
	conns       []*metadata.Connection
	createErr   error
	updateErr   error
	createCalls int
}

func (f *fakeConnectionStore) CreateConnection(_ context.Context, c *metadata.Connection) error {
	f.createCalls++
	if f.createErr != nil {
		return f.createErr
	}
	c.ID = uuid.New()
	c.CreatedAt = time.Now()
	c.UpdatedAt = time.Now()
	return nil
}

func (f *fakeConnectionStore) ConnectionByID(_ context.Context, wsID uuid.UUID, id uuid.UUID) (*metadata.Connection, error) {
	for _, c := range f.conns {
		// 按工作区过滤：跨工作区连接必须被拒绝（CodeRabbit #6）。
		if c.ID == id && c.WorkspaceID == wsID {
			return c, nil
		}
	}
	// 与真实 PGStore 一致：不存在时返回 sql.ErrNoRows，供 Service 映射 connection_not_found（vpvC6）。
	return nil, sql.ErrNoRows
}

func (f *fakeConnectionStore) ListConnections(context.Context, uuid.UUID) ([]metadata.Connection, error) {
	return nil, nil
}

func (f *fakeConnectionStore) UpdateConnection(_ context.Context, _ uuid.UUID, _ *metadata.Connection) error {
	return f.updateErr
}

type fakeMemberStore struct {
	role   metadata.MemberRole
	userID uuid.UUID // 非零时按 userID 过滤（CodeRabbit #6）
	wsID   uuid.UUID // 非零时按 wsID 过滤（CodeRabbit #6）
	err    error
}

func (f *fakeMemberStore) AddMember(context.Context, *metadata.WorkspaceMember) error { return nil }
func (f *fakeMemberStore) MemberByWorkspaceAndUser(_ context.Context, wsID, userID uuid.UUID) (*metadata.WorkspaceMember, error) {
	if f.err != nil {
		return nil, f.err
	}
	// 配置过滤条件时只返回匹配成员，否则无条件返回（向后兼容既有测试）。
	if f.wsID != uuid.Nil && wsID != f.wsID {
		return nil, errors.New("member not found")
	}
	if f.userID != uuid.Nil && userID != f.userID {
		return nil, errors.New("member not found")
	}
	return &metadata.WorkspaceMember{WorkspaceID: wsID, UserID: userID, Role: f.role}, nil
}
func (f *fakeMemberStore) ListMembers(context.Context, uuid.UUID) ([]metadata.WorkspaceMember, error) {
	return nil, nil
}
func (f *fakeMemberStore) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakeAuditSink struct {
	events []*metadata.AuditEvent
	fail   error
}

func (f *fakeAuditSink) AppendAudit(_ context.Context, e *metadata.AuditEvent) error {
	if f.fail != nil {
		return f.fail
	}
	e.ID = uuid.New()
	f.events = append(f.events, e)
	return nil
}

func (f *fakeAuditSink) QueryAudit(context.Context, metadata.AuditQuery) ([]metadata.AuditEvent, error) {
	return nil, nil
}

// blockingAuditSink 在 block=true 时阻塞直到 ctx 到期，用于验证审计写入真实超时（vtiLM）。
type blockingAuditSink struct {
	events []*metadata.AuditEvent
	block  bool
}

func (f *blockingAuditSink) AppendAudit(ctx context.Context, e *metadata.AuditEvent) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	e.ID = uuid.New()
	f.events = append(f.events, e)
	return nil
}

func (f *blockingAuditSink) QueryAudit(context.Context, metadata.AuditQuery) ([]metadata.AuditEvent, error) {
	return nil, nil
}

type fakeAlarmSink struct {
	events []metadata.SecurityAlertEvent
}

func (f *fakeAlarmSink) Alarm(_ context.Context, e metadata.SecurityAlertEvent) {
	f.events = append(f.events, e)
}

type fakeResolver struct {
	payload credentials.CredentialPayload
	err     error
}

func (f *fakeResolver) ResolveCredential(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
	return f.payload, f.err
}

type fakeTester struct {
	err error
}

func (f *fakeTester) Ping(context.Context, adapter.ConnectConfig) error {
	return f.err
}

func testService(
	conns *fakeConnectionStore,
	members *fakeMemberStore,
	audit *fakeAuditSink,
	alarm *fakeAlarmSink,
	resolver *fakeResolver,
	tester *fakeTester,
) (*Service, *fakeAuditSink, *fakeAlarmSink) {
	s := NewService(conns, members, audit, alarm, resolver, tester)
	return s, audit, alarm
}

func testPrincipal() Principal {
	return Principal{UserID: uuid.New(), WorkspaceID: uuid.New()}
}

// ---- E1/E2 ----------------------------------------------------------------

func TestConnection_CreateWritesE1(t *testing.T) {
	p := testPrincipal()
	conns := &fakeConnectionStore{}
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	s, _, _ := testService(conns, &fakeMemberStore{role: metadata.RoleOwner}, audit, alarm, &fakeResolver{}, &fakeTester{})

	conn := &metadata.Connection{
		WorkspaceID:   p.WorkspaceID,
		Name:          "conn-1",
		Engine:        metadata.EnginePostgreSQL,
		Host:          "db.invalid",
		Port:          5432,
		Database:      "synthetic",
		Environment:   metadata.EnvDevelopment,
		SecretRef:     uuid.New(),
		SecretVersion: 1,
	}
	created, err := s.Create(context.Background(), p, conn)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == uuid.Nil {
		t.Fatal("expected generated connection id")
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(audit.events))
	}
	ev := audit.events[0]
	if ev.Action != metadata.ActionConnectionCreate || ev.Outcome != metadata.OutcomeSucceeded {
		t.Fatalf("event = %s/%s, want connection.create/succeeded", ev.Action, ev.Outcome)
	}
	if ev.ConnectionID == nil || *ev.ConnectionID != created.ID {
		t.Fatalf("connection = %v, want %v", ev.ConnectionID, created.ID)
	}
	if ev.WorkspaceID != p.WorkspaceID {
		t.Fatalf("workspace = %v, want %v", ev.WorkspaceID, p.WorkspaceID)
	}
}

func TestConnection_CreateNonManagerRejected(t *testing.T) {
	p := testPrincipal()
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	s, _, _ := testService(&fakeConnectionStore{}, &fakeMemberStore{role: metadata.RoleViewer}, audit, alarm, &fakeResolver{}, &fakeTester{})

	_, err := s.Create(context.Background(), p, &metadata.Connection{})
	if err == nil {
		t.Fatal("expected forbidden error")
	}
	if len(audit.events) != 0 {
		t.Fatalf("audit events = %d, want 0 (rejected before audit)", len(audit.events))
	}
}

func TestConnection_UpdateWritesE2(t *testing.T) {
	p := testPrincipal()
	conns := &fakeConnectionStore{}
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	s, _, _ := testService(conns, &fakeMemberStore{role: metadata.RoleAdmin}, audit, alarm, &fakeResolver{}, &fakeTester{})

	conn := &metadata.Connection{
		ID:            uuid.New(),
		WorkspaceID:   p.WorkspaceID,
		Environment:   metadata.EnvProduction,
		SecretRef:     uuid.New(),
		SecretVersion: 1,
	}
	if err := s.Update(context.Background(), p, conn); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(audit.events))
	}
	ev := audit.events[0]
	if ev.Action != metadata.ActionConnectionUpdate || ev.Outcome != metadata.OutcomeSucceeded {
		t.Fatalf("event = %s/%s, want connection.update/succeeded", ev.Action, ev.Outcome)
	}
	var md map[string]any
	if err := json.Unmarshal(ev.Metadata, &md); err != nil {
		t.Fatal(err)
	}
	if md["environment"] != "production" {
		t.Errorf("environment = %v, want production", md["environment"])
	}
}

// ---- E7/E8 ----------------------------------------------------------------

func TestConnection_TestSucceededWritesE7(t *testing.T) {
	p := testPrincipal()
	connID := uuid.New()
	conns := &fakeConnectionStore{
		conns: []*metadata.Connection{{
			ID:            connID,
			WorkspaceID:   p.WorkspaceID,
			Engine:        metadata.EnginePostgreSQL,
			Environment:   metadata.EnvDevelopment,
			SecretRef:     uuid.New(),
			SecretVersion: 1,
		}},
	}
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	s, _, _ := testService(conns, &fakeMemberStore{role: metadata.RoleOwner}, audit, alarm,
		&fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}},
		&fakeTester{})

	if err := s.Test(context.Background(), p, connID); err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(audit.events))
	}
	ev := audit.events[0]
	if ev.Action != metadata.ActionConnectionTest || ev.Outcome != metadata.OutcomeSucceeded {
		t.Fatalf("event = %s/%s, want connection.test/succeeded", ev.Action, ev.Outcome)
	}
}

func TestConnection_TestFailedWritesE8(t *testing.T) {
	p := testPrincipal()
	connID := uuid.New()
	conns := &fakeConnectionStore{
		conns: []*metadata.Connection{{
			ID:            connID,
			WorkspaceID:   p.WorkspaceID,
			Engine:        metadata.EngineMySQL,
			Environment:   metadata.EnvDevelopment,
			SecretRef:     uuid.New(),
			SecretVersion: 1,
		}},
	}
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	s, _, _ := testService(conns, &fakeMemberStore{role: metadata.RoleOwner}, audit, alarm,
		&fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}},
		&fakeTester{err: &adapter.AdapterError{Code: adapter.ErrConnectionFailed}})

	if err := s.Test(context.Background(), p, connID); err == nil {
		t.Fatal("expected test failure")
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(audit.events))
	}
	ev := audit.events[0]
	if ev.Action != metadata.ActionConnectionTest || ev.Outcome != metadata.OutcomeFailed {
		t.Fatalf("event = %s/%s, want connection.test/failed", ev.Action, ev.Outcome)
	}
	var md map[string]any
	if err := json.Unmarshal(ev.Metadata, &md); err != nil {
		t.Fatal(err)
	}
	if md["error_code"] == nil || md["error_code"] == "" {
		t.Error("expected error_code in failed test metadata")
	}
}

func TestConnection_TestAuditFailureReturnsAuditFailed(t *testing.T) {
	p := testPrincipal()
	connID := uuid.New()
	conns := &fakeConnectionStore{
		conns: []*metadata.Connection{{
			ID:            connID,
			WorkspaceID:   p.WorkspaceID,
			Engine:        metadata.EnginePostgreSQL,
			Environment:   metadata.EnvDevelopment,
			SecretRef:     uuid.New(),
			SecretVersion: 1,
		}},
	}
	audit := &fakeAuditSink{fail: errors.New("injected audit failure")}
	alarm := &fakeAlarmSink{}
	s, _, _ := testService(conns, &fakeMemberStore{role: metadata.RoleOwner}, audit, alarm,
		&fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}},
		&fakeTester{})

	err := s.Test(context.Background(), p, connID)
	if err == nil {
		t.Fatal("expected audit failure error")
	}
	if !errors.Is(err, ErrAuditFailed) {
		t.Fatalf("error = %v, want audit_failed", err)
	}
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
}

func TestConnection_TestNonMemberRejected(t *testing.T) {
	// Test 必须像 Create/Update 一样先做成员/角色授权（Codex P1），
	// 非 owner/admin 不得触发凭证解析或外发 DB ping。
	p := testPrincipal()
	connID := uuid.New()
	conns := &fakeConnectionStore{
		conns: []*metadata.Connection{{
			ID:            connID,
			WorkspaceID:   p.WorkspaceID,
			Engine:        metadata.EnginePostgreSQL,
			Environment:   metadata.EnvDevelopment,
			SecretRef:     uuid.New(),
			SecretVersion: 1,
		}},
	}
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	pinged := false
	s := NewService(conns, &fakeMemberStore{role: metadata.RoleViewer}, audit, alarm,
		&fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}},
		fakeTesterFunc(func(context.Context, adapter.ConnectConfig) error {
			pinged = true
			return nil
		}))

	err := s.Test(context.Background(), p, connID)
	if err == nil {
		t.Fatal("expected forbidden error for non-manager")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("error = %v, want forbidden", err)
	}
	if pinged {
		t.Fatal("non-manager must not trigger outbound DB ping")
	}
	if len(audit.events) != 0 {
		t.Fatalf("audit events = %d, want 0 (rejected before audit)", len(audit.events))
	}
}

// ---- 负向：跨工作区 / 成员查询错误（CodeRabbit #6）---------------------------

func TestConnection_TestCrossWorkspaceConnectionRejected(t *testing.T) {
	// 连接属于其他工作区时 Test 返回 connection_not_found 且不写审计（CodeRabbit #6）。
	p := testPrincipal()
	connID := uuid.New()
	otherWS := uuid.New()
	conns := &fakeConnectionStore{
		conns: []*metadata.Connection{{
			ID:            connID,
			WorkspaceID:   otherWS, // 其他工作区
			Engine:        metadata.EnginePostgreSQL,
			Environment:   metadata.EnvDevelopment,
			SecretRef:     uuid.New(),
			SecretVersion: 1,
		}},
	}
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	pinged := false
	s := NewService(conns, &fakeMemberStore{role: metadata.RoleOwner}, audit, alarm,
		&fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}},
		fakeTesterFunc(func(context.Context, adapter.ConnectConfig) error {
			pinged = true
			return nil
		}))

	err := s.Test(context.Background(), p, connID)
	if !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("error = %v, want connection_not_found", err)
	}
	if pinged {
		t.Fatal("cross-workspace connection must not trigger ping")
	}
	if len(audit.events) != 0 {
		t.Fatalf("audit events = %d, want 0 (rejected before audit)", len(audit.events))
	}
}

func TestConnection_TestMemberLookupErrorRejected(t *testing.T) {
	// 成员查询返回错误时返回 forbidden 且不写审计（CodeRabbit #6）。
	p := testPrincipal()
	connID := uuid.New()
	conns := &fakeConnectionStore{
		conns: []*metadata.Connection{{
			ID:            connID,
			WorkspaceID:   p.WorkspaceID,
			Engine:        metadata.EnginePostgreSQL,
			Environment:   metadata.EnvDevelopment,
			SecretRef:     uuid.New(),
			SecretVersion: 1,
		}},
	}
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	pinged := false
	s := NewService(conns, &fakeMemberStore{role: metadata.RoleOwner, err: errors.New("member store failure")},
		audit, alarm,
		&fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}},
		fakeTesterFunc(func(context.Context, adapter.ConnectConfig) error {
			pinged = true
			return nil
		}))

	err := s.Test(context.Background(), p, connID)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("error = %v, want forbidden", err)
	}
	if pinged {
		t.Fatal("member lookup error must not trigger ping")
	}
	if len(audit.events) != 0 {
		t.Fatalf("audit events = %d, want 0", len(audit.events))
	}
}

// ---- 连接测试超时/取消场景（CodeRabbit #7、#10）-------------------------------

func TestConnection_TestTimeoutContext(t *testing.T) {
	// ping 超时（连接级 deadline）→ 返回 execution_timeout 稳定错误码，写 E8 error_code=execution_timeout。
	p := testPrincipal()
	connID := uuid.New()
	conns := &fakeConnectionStore{
		conns: []*metadata.Connection{{
			ID:            connID,
			WorkspaceID:   p.WorkspaceID,
			Engine:        metadata.EnginePostgreSQL,
			Environment:   metadata.EnvDevelopment,
			SecretRef:     uuid.New(),
			SecretVersion: 1,
		}},
	}
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	s := NewService(conns, &fakeMemberStore{role: metadata.RoleOwner}, audit, alarm,
		&fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}},
		fakeTesterFunc(func(ctx context.Context, _ adapter.ConnectConfig) error {
			return context.DeadlineExceeded
		}))

	err := s.Test(context.Background(), p, connID)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, StableErrorCode("execution_timeout")) {
		t.Fatalf("error = %v, want execution_timeout", err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1 (E8)", len(audit.events))
	}
	ev := audit.events[0]
	if ev.Action != metadata.ActionConnectionTest || ev.Outcome != metadata.OutcomeFailed {
		t.Fatalf("event = %s/%s, want connection.test/failed", ev.Action, ev.Outcome)
	}
	var md map[string]any
	if err := json.Unmarshal(ev.Metadata, &md); err != nil {
		t.Fatal(err)
	}
	if md["error_code"] != "execution_timeout" {
		t.Errorf("error_code = %v, want execution_timeout", md["error_code"])
	}
}

func TestConnection_TestCancelledContext(t *testing.T) {
	// 调用方 ctx 已取消 → ping 立即取消，返回 query_cancelled 稳定错误码，写 E8。
	p := testPrincipal()
	connID := uuid.New()
	conns := &fakeConnectionStore{
		conns: []*metadata.Connection{{
			ID:            connID,
			WorkspaceID:   p.WorkspaceID,
			Engine:        metadata.EnginePostgreSQL,
			Environment:   metadata.EnvDevelopment,
			SecretRef:     uuid.New(),
			SecretVersion: 1,
		}},
	}
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	s := NewService(conns, &fakeMemberStore{role: metadata.RoleOwner}, audit, alarm,
		&fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}},
		fakeTesterFunc(func(ctx context.Context, _ adapter.ConnectConfig) error {
			return ctx.Err()
		}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Test(ctx, p, connID)
	if err == nil {
		t.Fatal("expected cancelled error")
	}
	if !errors.Is(err, StableErrorCode("query_cancelled")) {
		t.Fatalf("error = %v, want query_cancelled", err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1 (E8)", len(audit.events))
	}
	ev := audit.events[0]
	var md map[string]any
	if err := json.Unmarshal(ev.Metadata, &md); err != nil {
		t.Fatal(err)
	}
	if md["error_code"] != "query_cancelled" {
		t.Errorf("error_code = %v, want query_cancelled", md["error_code"])
	}
}

// TestAdapterPingTester_GetErrorPropagates 覆盖 AdapterPingTester 的 Manager.Get
// 失败路径：错误正确传播、不 panic。连接归还由 AdapterPingTester.Ping 的
// defer handle.Release() 保证，完整归还语义由 adapter 包集成测试覆盖
// （adapter/manager_test.go 多处 defer h.Release()）。
func TestAdapterPingTester_GetErrorPropagates(t *testing.T) {
	mgr := adapter.NewAdapterManager(adapter.ManagerOptions{})
	defer mgr.Close(context.Background())

	tester := AdapterPingTester{Manager: mgr}
	cfg := adapter.ConnectConfig{
		ConnectionID: "ping-get-fail", ConfigRevision: 1,
		Engine: adapter.EnginePostgreSQL, Host: "localhost", Port: 5432,
		User: "u", Password: "p", Database: "d", TLS: adapter.TLSDisable,
	}
	// TLSDisable + AllowInsecureLocalDemo=false → createPool 拒绝，Get 返回错误。
	err := tester.Ping(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected Get failure to propagate")
	}
}

// ---- vpvC4：跨工作区成员拒绝回归测试 -----------------------------------------

func TestConnection_TestMemberWorkspaceMismatchRejected(t *testing.T) {
	// fakeMemberStore 按 wsID 过滤：调用方工作区与成员工作区不一致 → forbidden，
	// 且不写审计、不执行 ping（vpvC4）。
	p := testPrincipal()
	connID := uuid.New()
	otherWS := uuid.New()
	conns := &fakeConnectionStore{
		conns: []*metadata.Connection{{
			ID:            connID,
			WorkspaceID:   p.WorkspaceID,
			Engine:        metadata.EnginePostgreSQL,
			Environment:   metadata.EnvDevelopment,
			SecretRef:     uuid.New(),
			SecretVersion: 1,
		}},
	}
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	pinged := false
	s := NewService(conns, &fakeMemberStore{role: metadata.RoleOwner, wsID: otherWS}, audit, alarm,
		&fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}},
		fakeTesterFunc(func(context.Context, adapter.ConnectConfig) error {
			pinged = true
			return nil
		}))

	err := s.Test(context.Background(), p, connID)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("error = %v, want forbidden", err)
	}
	if pinged {
		t.Fatal("member workspace mismatch must not trigger ping")
	}
	if len(audit.events) != 0 {
		t.Fatalf("audit events = %d, want 0 (rejected before audit)", len(audit.events))
	}
}

// ---- vpvC5：真实 deadline 行为回归测试 ---------------------------------------

func TestConnection_TestTimeoutContextRealDeadline(t *testing.T) {
	// tester 阻塞在 pingCtx.Done()；connectionTestTimeout 注入为短超时，
	// 验证 ping 在服务端上限内被取消并映射 execution_timeout（vpvC5），
	// 而非仅验证错误映射。
	p := testPrincipal()
	connID := uuid.New()
	conns := &fakeConnectionStore{
		conns: []*metadata.Connection{{
			ID:            connID,
			WorkspaceID:   p.WorkspaceID,
			Engine:        metadata.EnginePostgreSQL,
			Environment:   metadata.EnvDevelopment,
			SecretRef:     uuid.New(),
			SecretVersion: 1,
		}},
	}
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	s := NewService(conns, &fakeMemberStore{role: metadata.RoleOwner}, audit, alarm,
		&fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}},
		fakeTesterFunc(func(ctx context.Context, _ adapter.ConnectConfig) error {
			<-ctx.Done()
			return ctx.Err()
		}))
	s.connectionTestTimeout = 50 * time.Millisecond

	start := time.Now()
	err := s.Test(context.Background(), p, connID)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, StableErrorCode("execution_timeout")) {
		t.Fatalf("error = %v, want execution_timeout", err)
	}
	if elapsed > 1*time.Second {
		t.Fatalf("ping not cancelled within server timeout: %v", elapsed)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1 (E8)", len(audit.events))
	}
	var md map[string]any
	if err := json.Unmarshal(audit.events[0].Metadata, &md); err != nil {
		t.Fatal(err)
	}
	if md["error_code"] != "execution_timeout" {
		t.Errorf("error_code = %v, want execution_timeout", md["error_code"])
	}
}

// ---- vpvC6：调用方取消后审计仍写入 ------------------------------------------

func TestConnection_TestCancelledContextStillWritesAudit(t *testing.T) {
	// 调用方 ctx 取消后，连接测试的审计事件仍必须追加（writeAudit 与取消解耦，vpvC6）。
	p := testPrincipal()
	connID := uuid.New()
	conns := &fakeConnectionStore{
		conns: []*metadata.Connection{{
			ID:            connID,
			WorkspaceID:   p.WorkspaceID,
			Engine:        metadata.EnginePostgreSQL,
			Environment:   metadata.EnvDevelopment,
			SecretRef:     uuid.New(),
			SecretVersion: 1,
		}},
	}
	audit := &fakeAuditSink{}
	alarm := &fakeAlarmSink{}
	s := NewService(conns, &fakeMemberStore{role: metadata.RoleOwner}, audit, alarm,
		&fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}},
		fakeTesterFunc(func(ctx context.Context, _ adapter.ConnectConfig) error {
			return ctx.Err()
		}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Test(ctx, p, connID)
	if !errors.Is(err, StableErrorCode("query_cancelled")) {
		t.Fatalf("error = %v, want query_cancelled", err)
	}

	// ping 因取消返回 Canceled → E8 query_cancelled；审计仍写入（使用非取消 context）。
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1 (audit must survive caller cancellation)", len(audit.events))
	}
	ev := audit.events[0]
	if ev.Action != metadata.ActionConnectionTest || ev.Outcome != metadata.OutcomeFailed {
		t.Fatalf("event = %s/%s, want connection.test/failed", ev.Action, ev.Outcome)
	}
	var md map[string]any
	if err := json.Unmarshal(ev.Metadata, &md); err != nil {
		t.Fatal(err)
	}
	if md["error_code"] != "query_cancelled" {
		t.Errorf("error_code = %v, want query_cancelled", md["error_code"])
	}
}

// ---- vtiLM：审计写入真实超时回归测试（PG 与 MySQL）--------------------------

func TestConnection_TestAuditWriteTimeoutRealDeadline(t *testing.T) {
	// auditWriteTimeout 注入为短超时，审计 store 阻塞直到 ctx 到期 →
	// Test 返回 audit_failed 且触发 $SECURITY_ALERT（vtiLM）。
	for _, engine := range []metadata.Engine{metadata.EnginePostgreSQL, metadata.EngineMySQL} {
		t.Run(string(engine), func(t *testing.T) {
			p := testPrincipal()
			connID := uuid.New()
			conns := &fakeConnectionStore{
				conns: []*metadata.Connection{{
					ID:            connID,
					WorkspaceID:   p.WorkspaceID,
					Engine:        engine,
					Environment:   metadata.EnvDevelopment,
					SecretRef:     uuid.New(),
					SecretVersion: 1,
				}},
			}
			audit := &blockingAuditSink{block: true}
			alarm := &fakeAlarmSink{}
			s := NewService(conns, &fakeMemberStore{role: metadata.RoleOwner}, audit, alarm,
				&fakeResolver{payload: credentials.CredentialPayload{User: "u", Password: "p"}},
				fakeTesterFunc(func(context.Context, adapter.ConnectConfig) error {
					return nil // ping 成功，进入审计写入
				}))
			s.auditWriteTimeout = 50 * time.Millisecond

			start := time.Now()
			err := s.Test(context.Background(), p, connID)
			elapsed := time.Since(start)
			if err == nil {
				t.Fatal("expected audit_failed")
			}
			if !errors.Is(err, ErrAuditFailed) {
				t.Fatalf("error = %v, want audit_failed", err)
			}
			if elapsed > 1*time.Second {
				t.Fatalf("audit write not cancelled within server timeout: %v", elapsed)
			}
			if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
				t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
			}
		})
	}
}

// TestConnectionLogStorageFailureRedactsSensitive 验证 connections.logStorageFailure
// 在写入结构化日志前统一脱敏（vti-OS）：注入含真实凭证内容的错误，断言敏感值不出现。
func TestConnectionLogStorageFailureRedactsSensitive(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	s := NewService(&fakeConnectionStore{}, &fakeMemberStore{}, &fakeAuditSink{}, &fakeAlarmSink{}, &fakeResolver{}, &fakeTester{})
	s.logger = logger

	sensitive := "password=classified123"
	s.logStorageFailure("test.op", uuid.New(), uuid.New(), errors.New("db error: "+sensitive))

	out := buf.String()
	if !strings.Contains(out, "db error") {
		t.Errorf("root cause context not logged: %q", out)
	}
	for _, leaked := range []string{sensitive, "classified123"} {
		if strings.Contains(out, leaked) {
			t.Errorf("log leaked sensitive value %q: %q", leaked, out)
		}
	}
	if !strings.Contains(out, "password=[redacted]") {
		t.Errorf("sensitive value not redacted: %q", out)
	}
}

// fakeTesterFunc 将函数适配为 ConnectionTester。
type fakeTesterFunc func(ctx context.Context, cfg adapter.ConnectConfig) error

func (f fakeTesterFunc) Ping(ctx context.Context, cfg adapter.ConnectConfig) error {
	return f(ctx, cfg)
}
