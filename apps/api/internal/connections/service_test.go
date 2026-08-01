package connections

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
	return nil, errors.New("connection not found")
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

// fakeTesterFunc 将函数适配为 ConnectionTester。
type fakeTesterFunc func(ctx context.Context, cfg adapter.ConnectConfig) error

func (f fakeTesterFunc) Ping(ctx context.Context, cfg adapter.ConnectConfig) error {
	return f(ctx, cfg)
}
