package credentials

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- shared test helpers -----------------------------------------------------

// goodKEK 返回合成 256-bit 测试 KEK（禁止复制自文档的密钥字面量）。
func goodKEK() KEKProvider {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return &testKEK{key: key}
}

type testKEK struct {
	key []byte
}

func (k *testKEK) ActiveKEK() (int, []byte, error) { return 1, k.key, nil }
func (k *testKEK) GetKEK(int) ([]byte, error)      { return k.key, nil }
func (k *testKEK) ReserveWrap(int) error           { return nil }
func (k *testKEK) WrapCount(int) uint64            { return 0 }

// fakeAtomicTx 同时实现 metadata.CredentialAtomicTx 与 metadata.ConnectionAtomicTx。
type fakeAtomicTx struct {
	env                *metadata.CredentialEnvelope
	lockErr            error
	insertErr          error
	updateErr          error
	count              int
	countErr           error
	appendAuditErr     error
	events             []*metadata.AuditEvent
	appendCalls        int
	commitCalls        int
	rollbackCalls      int
	committed          bool
	insertCalls        int
	updateVersionCalls int
}

func (f *fakeAtomicTx) AppendAudit(ctx context.Context, op *metadata.OperationContext, e *metadata.AuditEvent) error {
	f.appendCalls++
	f.events = append(f.events, e)
	return f.appendAuditErr
}
func (f *fakeAtomicTx) LockEnvelopeForUpdate(context.Context, uuid.UUID, uuid.UUID) (*metadata.CredentialEnvelope, error) {
	return f.env, f.lockErr
}
func (f *fakeAtomicTx) LockEnvelopeVersion(context.Context, uuid.UUID, uuid.UUID, int) (*metadata.CredentialEnvelope, error) {
	return f.env, f.lockErr
}
func (f *fakeAtomicTx) InsertEnvelope(context.Context, *metadata.CredentialEnvelope) error {
	f.insertCalls++
	return f.insertErr
}
func (f *fakeAtomicTx) UpdateRetiredAt(context.Context, uuid.UUID, uuid.UUID, int) error {
	return f.updateErr
}
func (f *fakeAtomicTx) CountConnectionsByVersion(context.Context, uuid.UUID, uuid.UUID, int) (int, error) {
	return f.count, f.countErr
}
func (f *fakeAtomicTx) UpdateConnectionVersion(context.Context, uuid.UUID, uuid.UUID, int) error {
	f.updateVersionCalls++
	return f.updateErr
}
func (f *fakeAtomicTx) Commit() error { f.committed = true; f.commitCalls++; return nil }
func (f *fakeAtomicTx) Rollback() error {
	f.rollbackCalls++
	return nil
}
func (f *fakeAtomicTx) CreateConnection(context.Context, *metadata.Connection) error {
	return f.insertErr
}
func (f *fakeAtomicTx) UpdateConnection(context.Context, uuid.UUID, *metadata.Connection) error {
	return f.updateErr
}

type fakeAtomicTxStore struct {
	tx         *fakeAtomicTx
	beginErr   error
	beginCalls int
}

func (s *fakeAtomicTxStore) BeginCredential(ctx context.Context, op *metadata.OperationContext) (metadata.CredentialAtomicTx, error) {
	s.beginCalls++
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	return s.tx, nil
}

func (s *fakeAtomicTxStore) BeginConnection(ctx context.Context, op *metadata.OperationContext) (metadata.ConnectionAtomicTx, error) {
	s.beginCalls++
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	return s.tx, nil
}

type fakeReadStore struct {
	env    *metadata.CredentialEnvelope
	envErr error
}

func (s *fakeReadStore) EnvelopeByRef(context.Context, uuid.UUID, uuid.UUID, int) (*metadata.CredentialEnvelope, error) {
	return s.env, s.envErr
}

type fakeAuditSink struct {
	events []*metadata.AuditEvent
	fail   error
}

func (s *fakeAuditSink) AppendAudit(ctx context.Context, e *metadata.AuditEvent) error {
	if s.fail != nil {
		return s.fail
	}
	s.events = append(s.events, e)
	return nil
}
func (s *fakeAuditSink) QueryAudit(context.Context, metadata.AuditQuery) ([]metadata.AuditEvent, error) {
	return nil, nil
}

type fakeAlarmRecorder struct {
	events []metadata.SecurityAlertEvent
}

func (r *fakeAlarmRecorder) Alarm(ctx context.Context, e metadata.SecurityAlertEvent) {
	r.events = append(r.events, e)
}

// newTestManager 构建带 fake 依赖的 LifecycleManager。
func newTestManager(tx *fakeAtomicTx, read *fakeReadStore, audit *fakeAuditSink, alarm *fakeAlarmRecorder, logger ...*slog.Logger) (*LifecycleManager, *fakeAtomicTxStore) {
	txstore := &fakeAtomicTxStore{tx: tx}
	m := NewLifecycleManager(read, txstore, audit, goodKEK(), alarm, logger...)
	return m, txstore
}

func testPayload() CredentialPayload {
	return CredentialPayload{User: "synthetic_user", Password: "synthetic_password"}
}

// ---- Create 原子性 -----------------------------------------------------------

func TestCreateAppendsE3AndCommits(t *testing.T) {
	tx := &fakeAtomicTx{}
	alarm := &fakeAlarmRecorder{}
	m, _ := newTestManager(tx, &fakeReadStore{}, &fakeAuditSink{}, alarm)

	env, err := m.Create(context.Background(), uuid.New(), uuid.New(), testPayload())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if env == nil {
		t.Fatal("Create() envelope = nil")
	}
	if tx.appendCalls != 1 {
		t.Fatalf("AppendAudit calls = %d, want 1", tx.appendCalls)
	}
	if len(tx.events) != 1 || tx.events[0].Action != metadata.ActionCredentialCreate {
		t.Fatalf("expected E3 event, got %d events", len(tx.events))
	}
	if !tx.committed {
		t.Fatal("expected commit after successful create")
	}
}

func TestCreateAuditFailureRollsBack(t *testing.T) {
	tx := &fakeAtomicTx{appendAuditErr: errors.New("injected audit failure")}
	alarm := &fakeAlarmRecorder{}
	m, _ := newTestManager(tx, &fakeReadStore{}, &fakeAuditSink{}, alarm)

	_, err := m.Create(context.Background(), uuid.New(), uuid.New(), testPayload())
	if !IsErrorCode(err, ErrAuditFailed) {
		t.Fatalf("Create() error = %v, want audit_failed", err)
	}
	if tx.committed {
		t.Fatal("must NOT commit when audit append fails")
	}
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %d alarms", len(alarm.events))
	}
}

func TestCreateInsertFailureNoCommit(t *testing.T) {
	tx := &fakeAtomicTx{insertErr: errors.New("injected insert failure")}
	m, _ := newTestManager(tx, &fakeReadStore{}, &fakeAuditSink{}, &fakeAlarmRecorder{})

	_, err := m.Create(context.Background(), uuid.New(), uuid.New(), testPayload())
	if !IsErrorCode(err, ErrInternalError) {
		t.Fatalf("Create() error = %v, want internal_error", err)
	}
	if tx.committed {
		t.Fatal("must NOT commit when insert fails")
	}
	if tx.appendCalls != 0 {
		t.Fatalf("AppendAudit calls = %d, want 0 (no audit after mutation failure)", tx.appendCalls)
	}
}

// ---- Rotate 原子性 -----------------------------------------------------------

func TestRotateAppendsE4AndCommits(t *testing.T) {
	ref := uuid.New()
	tx := &fakeAtomicTx{env: &metadata.CredentialEnvelope{SecretRef: ref, Version: 1}}
	m, _ := newTestManager(tx, &fakeReadStore{}, &fakeAuditSink{}, &fakeAlarmRecorder{})

	env, err := m.Rotate(context.Background(), uuid.New(), uuid.New(), ref, 1, testPayload())
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if env == nil {
		t.Fatal("Rotate() envelope = nil")
	}
	if tx.updateVersionCalls != 1 {
		t.Fatalf("UpdateConnectionVersion calls = %d, want 1", tx.updateVersionCalls)
	}
	if len(tx.events) != 1 || tx.events[0].Action != metadata.ActionCredentialRotate || tx.events[0].Outcome != metadata.OutcomeSucceeded {
		t.Fatalf("expected E4 event, got %d events", len(tx.events))
	}
	if !tx.committed {
		t.Fatal("expected commit after successful rotate")
	}
}

func TestRotateVersionConflictWritesE5(t *testing.T) {
	ref := uuid.New()
	tx := &fakeAtomicTx{env: &metadata.CredentialEnvelope{SecretRef: ref, Version: 2}}
	audit := &fakeAuditSink{}
	m, _ := newTestManager(tx, &fakeReadStore{}, audit, &fakeAlarmRecorder{})

	_, err := m.Rotate(context.Background(), uuid.New(), uuid.New(), ref, 1, testPayload())
	if !IsErrorCode(err, ErrVersionConflict) {
		t.Fatalf("Rotate() error = %v, want version_conflict", err)
	}
	if tx.committed {
		t.Fatal("must NOT commit on version conflict")
	}
	if len(audit.events) != 1 || audit.events[0].Action != metadata.ActionCredentialRotate || audit.events[0].Outcome != metadata.OutcomeFailed {
		t.Fatalf("expected E5 failure audit, got %d events", len(audit.events))
	}
}

// ---- Retire 原子性 -----------------------------------------------------------

func TestRetireAppendsE6AndCommits(t *testing.T) {
	ref := uuid.New()
	tx := &fakeAtomicTx{env: &metadata.CredentialEnvelope{SecretRef: ref, Version: 1}}
	m, _ := newTestManager(tx, &fakeReadStore{}, &fakeAuditSink{}, &fakeAlarmRecorder{})

	if err := m.Retire(context.Background(), uuid.New(), uuid.New(), ref, 1); err != nil {
		t.Fatalf("Retire() error = %v", err)
	}
	if len(tx.events) != 1 || tx.events[0].Action != metadata.ActionCredentialRetire {
		t.Fatalf("expected E6 event, got %d events", len(tx.events))
	}
	if !tx.committed {
		t.Fatal("expected commit after successful retire")
	}
}

func TestRetireInUseNoCommit(t *testing.T) {
	ref := uuid.New()
	tx := &fakeAtomicTx{env: &metadata.CredentialEnvelope{SecretRef: ref, Version: 1}, count: 1}
	m, _ := newTestManager(tx, &fakeReadStore{}, &fakeAuditSink{}, &fakeAlarmRecorder{})

	err := m.Retire(context.Background(), uuid.New(), uuid.New(), ref, 1)
	if !IsErrorCode(err, ErrCredentialInUse) {
		t.Fatalf("Retire() error = %v, want credential_in_use", err)
	}
	if tx.committed {
		t.Fatal("must NOT commit when references exist")
	}
}

// ---- Resolve 失败审计（E14-E16） ---------------------------------------------

func TestResolveNotFoundWritesE14(t *testing.T) {
	ref := uuid.New()
	read := &fakeReadStore{envErr: metadata.ErrEnvelopeNotFound}
	audit := &fakeAuditSink{}
	m, _ := newTestManager(nil, read, audit, &fakeAlarmRecorder{})

	_, err := m.Resolve(context.Background(), uuid.New(), ref, 1)
	if !IsErrorCode(err, ErrCredentialNotFound) {
		t.Fatalf("Resolve() error = %v, want credential_not_found", err)
	}
	if len(audit.events) != 1 || audit.events[0].Action != metadata.ActionCredentialLookup {
		t.Fatalf("expected E14 lookup event, got %d events", len(audit.events))
	}
}

// ---- 顺序与脱敏 --------------------------------------------------------------

func TestCreatePerformsInsertAndCommit(t *testing.T) {
	tx := &fakeAtomicTx{}
	m, _ := newTestManager(tx, &fakeReadStore{}, &fakeAuditSink{}, &fakeAlarmRecorder{})

	if _, err := m.Create(context.Background(), uuid.New(), uuid.New(), testPayload()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if tx.insertCalls != 1 {
		t.Fatalf("InsertEnvelope calls = %d, want 1", tx.insertCalls)
	}
}

func TestLogStorageFailureRedactsSensitive(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	read := &fakeReadStore{envErr: errors.New("db error: password=classified123")}
	alarm := &fakeAlarmRecorder{}
	m, _ := newTestManager(nil, read, &fakeAuditSink{}, alarm, logger)

	_, err := m.Resolve(context.Background(), uuid.New(), uuid.New(), 1)
	if !IsErrorCode(err, ErrInternalError) {
		t.Fatalf("error = %v, want internal_error", err)
	}
	out := buf.String()
	if !strings.Contains(out, "db error") {
		t.Errorf("root cause context not logged: %q", out)
	}
	for _, leaked := range []string{"classified123", "password=classified"} {
		if strings.Contains(out, leaked) {
			t.Errorf("log leaked sensitive value %q: %q", leaked, out)
		}
	}
	if !strings.Contains(out, "password=[redacted]") {
		t.Errorf("sensitive value not redacted: %q", out)
	}
}

// 引用 io，避免未使用导入（logger 需要 io.Discard）。
var _ = io.Discard
