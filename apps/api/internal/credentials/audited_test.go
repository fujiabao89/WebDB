package credentials

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- mocks ----------------------------------------------------------------

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

type fakeAlarmRecorder struct {
	events []metadata.SecurityAlertEvent
}

func (f *fakeAlarmRecorder) Alarm(_ context.Context, e metadata.SecurityAlertEvent) {
	f.events = append(f.events, e)
}

// fakeCredentialStore 实现 CredentialTXStore + ConnectionTXStore。
type fakeCredentialStore struct {
	env    *metadata.CredentialEnvelope
	envErr error
	conns  []*metadata.Connection
}

func (s *fakeCredentialStore) CreateEnvelope(context.Context, *metadata.CredentialEnvelope) error {
	return nil
}

func (s *fakeCredentialStore) EnvelopeByRef(context.Context, uuid.UUID, uuid.UUID, int) (*metadata.CredentialEnvelope, error) {
	if s.envErr != nil {
		return nil, s.envErr
	}
	return s.env, nil
}

func (s *fakeCredentialStore) ListEnvelopes(context.Context, uuid.UUID) ([]metadata.CredentialEnvelope, error) {
	return nil, nil
}

func (s *fakeCredentialStore) LockEnvelopeForUpdate(context.Context, *sql.Tx, uuid.UUID, uuid.UUID) (*metadata.CredentialEnvelope, error) {
	return s.env, nil
}

func (s *fakeCredentialStore) LockEnvelopeVersion(context.Context, *sql.Tx, uuid.UUID, uuid.UUID, int) (*metadata.CredentialEnvelope, error) {
	return s.env, nil
}

func (s *fakeCredentialStore) InsertEnvelopeTx(context.Context, *sql.Tx, *metadata.CredentialEnvelope) error {
	return nil
}

func (s *fakeCredentialStore) ListEnvelopesByRef(context.Context, uuid.UUID, uuid.UUID) ([]metadata.CredentialEnvelope, error) {
	return []metadata.CredentialEnvelope{*s.env}, nil
}

func (s *fakeCredentialStore) UpdateRetiredAt(context.Context, *sql.Tx, uuid.UUID, uuid.UUID, int) error {
	return nil
}

func (s *fakeCredentialStore) CreateConnection(context.Context, *metadata.Connection) error {
	return nil
}

func (s *fakeCredentialStore) ConnectionByID(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
	if len(s.conns) > 0 {
		return s.conns[0], nil
	}
	return nil, sql.ErrNoRows
}

func (s *fakeCredentialStore) ListConnections(context.Context, uuid.UUID) ([]metadata.Connection, error) {
	return nil, nil
}

func (s *fakeCredentialStore) UpdateConnection(context.Context, uuid.UUID, *metadata.Connection) error {
	return nil
}

func (s *fakeCredentialStore) UpdateConnectionVersion(context.Context, *sql.Tx, uuid.UUID, uuid.UUID, int) error {
	return nil
}

func (s *fakeCredentialStore) CountConnectionsByVersion(context.Context, *sql.Tx, uuid.UUID, uuid.UUID, int) (int, error) {
	return 0, nil
}

// auditedManager 构建带审计的凭证生命周期管理器。
func auditedManager(store metadata.CredentialTXStore, conns metadata.ConnectionTXStore, kek KEKProvider, audit *fakeAuditStore, alarm *fakeAlarmRecorder) (*AuditedLifecycleManager, *fakeAuditStore, *fakeAlarmRecorder) {
	lm := NewLifecycleManager(nil, store, conns, kek)
	m := NewAuditedLifecycleManager(lm, audit, alarm)
	return m, audit, alarm
}

type simpleKEKProvider struct{}

func (*simpleKEKProvider) ActiveKEK() (int, []byte, error) {
	return 1, make([]byte, kekSize), nil
}

func (*simpleKEKProvider) GetKEK(int) ([]byte, error) {
	return make([]byte, kekSize), nil
}

func (*simpleKEKProvider) ReserveWrap(int) error { return nil }
func (*simpleKEKProvider) WrapCount(int) uint64  { return 0 }

func goodKEK() KEKProvider { return &simpleKEKProvider{} }

// ---- E3 create ------------------------------------------------------------

func TestAuditedCredential_CreateWritesE3(t *testing.T) {
	store := &createOnlyCredentialStore{}
	audit := &fakeAuditStore{}
	alarm := &fakeAlarmRecorder{}
	m, _, _ := auditedManager(store, nil, goodKEK(), audit, alarm)

	wsID := uuid.New()
	actorID := uuid.New()
	env, err := m.Create(context.Background(), wsID, actorID, CredentialPayload{User: "u", Password: "p"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(audit.events))
	}
	ev := audit.events[0]
	if ev.Action != metadata.ActionCredentialCreate {
		t.Fatalf("action = %q, want credential.create", ev.Action)
	}
	if ev.Outcome != metadata.OutcomeSucceeded {
		t.Fatalf("outcome = %q, want succeeded", ev.Outcome)
	}
	if ev.ActorType != metadata.ActorTypeUser || ev.ActorID == nil || *ev.ActorID != actorID {
		t.Fatalf("actor = %v/%v, want user/%v", ev.ActorType, ev.ActorID, actorID)
	}
	if ev.WorkspaceID != wsID {
		t.Fatalf("workspace = %v, want %v", ev.WorkspaceID, wsID)
	}
	if ev.ResourceID != env.SecretRef.String() {
		t.Fatalf("resource_id = %s, want %s", ev.ResourceID, env.SecretRef)
	}
}

func TestAuditedCredential_CreateAuditFailureReturnsAuditFailed(t *testing.T) {
	store := &createOnlyCredentialStore{}
	audit := &fakeAuditStore{fail: errors.New("injected audit failure")}
	alarm := &fakeAlarmRecorder{}
	m, _, _ := auditedManager(store, nil, goodKEK(), audit, alarm)

	_, err := m.Create(context.Background(), uuid.New(), uuid.New(), CredentialPayload{User: "u", Password: "p"})
	if !IsErrorCode(err, ErrAuditFailed) {
		t.Fatalf("error = %v, want audit_failed", err)
	}
}

// ---- E14/E15 Resolve ------------------------------------------------------

func TestAuditedCredential_ResolveNotFoundWritesE14(t *testing.T) {
	store := &fakeCredentialStore{envErr: metadata.ErrEnvelopeNotFound}
	audit := &fakeAuditStore{}
	alarm := &fakeAlarmRecorder{}
	m, _, _ := auditedManager(store, nil, goodKEK(), audit, alarm)

	_, err := m.Resolve(context.Background(), uuid.New(), uuid.New(), 1)
	if err == nil {
		t.Fatal("expected resolve error")
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(audit.events))
	}

	ev := audit.events[0]
	if ev.Action != metadata.ActionCredentialLookup {
		t.Fatalf("action = %q, want credential.lookup", ev.Action)
	}
	if ev.Outcome != metadata.OutcomeFailed {
		t.Fatalf("outcome = %q, want failed", ev.Outcome)
	}
	if ev.ActorType != metadata.ActorTypeSystem || ev.ActorID != nil {
		t.Fatalf("actor = %v/%v, want system/nil", ev.ActorType, ev.ActorID)
	}
	if len(alarm.events) != 0 {
		t.Fatalf("alarm events = %d, want 0 (lookup failure)", len(alarm.events))
	}
}

func TestAuditedCredential_ResolveDecryptFailureWritesE15AndAlarm(t *testing.T) {
	// 伪造 ciphertext 使 GCM 认证失败 → decryption_failed。
	store := &fakeCredentialStore{
		env: &metadata.CredentialEnvelope{
			WorkspaceID:   uuid.New(),
			SecretRef:     uuid.New(),
			Version:       1,
			Ciphertext:    []byte{0x01},
			DataNonce:     make([]byte, nonceSize),
			WrappedDEK:    []byte{0x02},
			WrapNonce:     make([]byte, nonceSize),
			EnvelopeSuite: SuiteAES256GCMv1,
			KEKVersion:    1,
		},
	}
	audit := &fakeAuditStore{}
	alarm := &fakeAlarmRecorder{}
	m, _, _ := auditedManager(store, nil, goodKEK(), audit, alarm)

	_, err := m.Resolve(context.Background(), uuid.New(), uuid.New(), 1)
	if err == nil {
		t.Fatal("expected resolve error")
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(audit.events))
	}

	ev := audit.events[0]
	if ev.Action != metadata.ActionCredentialDecrypt {
		t.Fatalf("action = %q, want credential.decrypt", ev.Action)
	}
	if len(alarm.events) != 1 {
		t.Fatalf("alarm events = %d, want 1 (decrypt failure)", len(alarm.events))
	}
	if alarm.events[0].Code != string(ErrDecryptionFailed) {
		t.Fatalf("alarm code = %q, want decryption_failed", alarm.events[0].Code)
	}
}

// ---- E4/E5 Rotate（事件构造语义；事务端到端由集成测试覆盖）-------------------

func TestAuditedCredential_RotateSucceededEvent(t *testing.T) {
	wsID := uuid.New()
	actorID := uuid.New()
	secretRef := uuid.New()
	env := &metadata.CredentialEnvelope{
		WorkspaceID:   wsID,
		SecretRef:     secretRef,
		Version:       2,
		EnvelopeSuite: SuiteAES256GCMv1,
		KEKVersion:    1,
	}

	ev, err := newRotateSucceededEvent(wsID, actorID, secretRef, 1, env, "trace-1", timeNow())
	if err != nil {
		t.Fatalf("newRotateSucceededEvent error = %v", err)
	}
	if ev.Action != metadata.ActionCredentialRotate || ev.Outcome != metadata.OutcomeSucceeded {
		t.Fatalf("event = %s/%s, want credential.rotate/succeeded", ev.Action, ev.Outcome)
	}
	var md map[string]any
	if err := json.Unmarshal(ev.Metadata, &md); err != nil {
		t.Fatal(err)
	}
	if md["old_version"] != float64(1) || md["new_version"] != float64(2) {
		t.Errorf("versions = %v/%v, want 1/2", md["old_version"], md["new_version"])
	}
	if md["envelope_suite"] != SuiteAES256GCMv1 {
		t.Errorf("envelope_suite = %v", md["envelope_suite"])
	}
}

func TestAuditedCredential_RotateFailedEvent(t *testing.T) {
	wsID := uuid.New()
	actorID := uuid.New()
	secretRef := uuid.New()

	conflictErr := fmt.Errorf("%w: expected 1, actual 3", ErrVersionConflict)
	ev, err := newRotateFailedEvent(wsID, actorID, secretRef, 1, conflictErr, "trace-1", timeNow())
	if err != nil {
		t.Fatalf("newRotateFailedEvent error = %v", err)
	}
	if ev.Action != metadata.ActionCredentialRotate || ev.Outcome != metadata.OutcomeFailed {
		t.Fatalf("event = %s/%s, want credential.rotate/failed", ev.Action, ev.Outcome)
	}
	var md map[string]any
	if err := json.Unmarshal(ev.Metadata, &md); err != nil {
		t.Fatal(err)
	}
	if md["error_code"] != string(ErrVersionConflict) {
		t.Errorf("error_code = %v, want version_conflict", md["error_code"])
	}
	if md["expected_version"] != float64(1) {
		t.Errorf("expected_version = %v, want 1", md["expected_version"])
	}
}

// ---- E6 Retire（事件构造语义）---------------------------------------------

func TestAuditedCredential_RetireSucceededEvent(t *testing.T) {
	wsID := uuid.New()
	actorID := uuid.New()
	secretRef := uuid.New()

	ev, err := newRetireSucceededEvent(wsID, actorID, secretRef, 1, "trace-1", timeNow())
	if err != nil {
		t.Fatalf("newRetireSucceededEvent error = %v", err)
	}
	if ev.Action != metadata.ActionCredentialRetire || ev.Outcome != metadata.OutcomeSucceeded {
		t.Fatalf("event = %s/%s, want credential.retire/succeeded", ev.Action, ev.Outcome)
	}
	var md map[string]any
	if err := json.Unmarshal(ev.Metadata, &md); err != nil {
		t.Fatal(err)
	}
	if md["secret_version"] != float64(1) {
		t.Errorf("secret_version = %v, want 1", md["secret_version"])
	}
}

func timeNow() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
