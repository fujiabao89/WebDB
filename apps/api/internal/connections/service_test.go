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

func (f *fakeConnectionStore) ConnectionByID(_ context.Context, _ uuid.UUID, id uuid.UUID) (*metadata.Connection, error) {
	for _, c := range f.conns {
		if c.ID == id {
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
	role metadata.MemberRole
}

func (f *fakeMemberStore) AddMember(context.Context, *metadata.WorkspaceMember) error { return nil }
func (f *fakeMemberStore) MemberByWorkspaceAndUser(_ context.Context, _, _ uuid.UUID) (*metadata.WorkspaceMember, error) {
	return &metadata.WorkspaceMember{Role: f.role}, nil
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
	s, _, _ := testService(conns, &fakeMemberStore{}, audit, alarm,
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
	s, _, _ := testService(conns, &fakeMemberStore{}, audit, alarm,
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
	s, _, _ := testService(conns, &fakeMemberStore{}, audit, alarm,
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
