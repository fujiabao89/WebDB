package credentials

import (
	"bytes"
	"context"
	"errors"
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
	// 复刻真实审计闸门契约（CodeRabbit-0）：op 与事件字段完全匹配才允许追加。
	if err := matchFakeOpEvent(op, e); err != nil {
		return err
	}
	f.appendCalls++
	f.events = append(f.events, e)
	return f.appendAuditErr
}

// matchFakeOpEvent 复刻 metadata.matchAuditEventOp 的字段一致性校验，防止 fake
// 跳过审计闸门导致单元测试层对 OperationContext 与 AuditEvent 的一致性无保护。
func matchFakeOpEvent(op *metadata.OperationContext, e *metadata.AuditEvent) error {
	if op == nil {
		return errors.New("audit gate: nil operation context")
	}
	if e == nil {
		return errors.New("audit gate: nil audit event")
	}
	if op.WorkspaceID() != e.WorkspaceID {
		return errors.New("audit gate: workspace mismatch")
	}
	if op.Action() != e.Action {
		return errors.New("audit gate: action mismatch")
	}
	if op.Resource() != e.ResourceType {
		return errors.New("audit gate: resource mismatch")
	}
	if op.ResourceID() != e.ResourceID {
		return errors.New("audit gate: resource id mismatch")
	}
	if op.Outcome() != string(e.Outcome) {
		return errors.New("audit gate: outcome mismatch")
	}
	if op.TraceID() != e.TraceID {
		return errors.New("audit gate: trace mismatch")
	}
	if e.ActorID == nil || *e.ActorID != op.ActorID() {
		return errors.New("audit gate: actor mismatch")
	}
	return nil
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
	lastOp     *metadata.OperationContext // 记录最近一次 Begin* 的 op（用于 trace 关联断言）
}

func (s *fakeAtomicTxStore) BeginCredential(ctx context.Context, op *metadata.OperationContext) (metadata.CredentialAtomicTx, error) {
	s.beginCalls++
	s.lastOp = op
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

// TestRotateE5TraceReusesRequestTrace 验证 E5 失败审计复用 Rotate 请求的 traceID
// （LATEST-CR-01）：同一次 Rotate 请求不得产生第二个 trace，排障可按 trace 关联。
func TestRotateE5TraceReusesRequestTrace(t *testing.T) {
	ref := uuid.New()
	tx := &fakeAtomicTx{env: &metadata.CredentialEnvelope{SecretRef: ref, Version: 2}}
	audit := &fakeAuditSink{}
	m, txstore := newTestManager(tx, &fakeReadStore{}, audit, &fakeAlarmRecorder{})

	_, err := m.Rotate(context.Background(), uuid.New(), uuid.New(), ref, 1, testPayload())
	if !IsErrorCode(err, ErrVersionConflict) {
		t.Fatalf("Rotate() error = %v, want version_conflict", err)
	}
	if txstore.lastOp == nil {
		t.Fatal("BeginCredential not called with op")
	}
	if len(audit.events) != 1 {
		t.Fatalf("expected E5 failure audit, got %d events", len(audit.events))
	}
	if audit.events[0].TraceID != txstore.lastOp.TraceID() {
		t.Fatalf("E5 trace %q != request trace %q (must reuse Rotate traceID)", audit.events[0].TraceID, txstore.lastOp.TraceID())
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

func TestResolveFailureDoesNotWriteE14(t *testing.T) {
	// QODO-13：Resolve 本身不写 E14-E16（execution.Pipeline.recordCredentialFailure 为
	// 权威写入者，携带 connection_id 符合 E14-E16 事件矩阵）。此处断言 Resolve 不产生
	// 审计，防止与 pipeline 重复审计行。
	ref := uuid.New()
	read := &fakeReadStore{envErr: metadata.ErrEnvelopeNotFound}
	audit := &fakeAuditSink{}
	m, _ := newTestManager(nil, read, audit, &fakeAlarmRecorder{})

	_, err := m.Resolve(context.Background(), uuid.New(), ref, 1)
	if !IsErrorCode(err, ErrCredentialNotFound) {
		t.Fatalf("Resolve() error = %v, want credential_not_found", err)
	}
	if len(audit.events) != 0 {
		t.Fatalf("Resolve must not write E14-E16 (pipeline is authoritative): got %d events", len(audit.events))
	}
}

func TestResolveDecryptFailureDoesNotWriteE15(t *testing.T) {
	// QODO-13：decrypt 类失败同样由 pipeline 权威写入，Resolve 不写 E15/E16。
	ref := uuid.New()
	read := &fakeReadStore{env: &metadata.CredentialEnvelope{
		WorkspaceID:   uuid.New(),
		SecretRef:     ref,
		Version:       1,
		EnvelopeSuite: "AES256GCM-v1",
		KEKVersion:    1,
		Ciphertext:    []byte("garbage-cipher"),
		DataNonce:     []byte("data-nonce-12b"),
		WrappedDEK:    []byte("garbage-dek"),
		WrapNonce:     []byte("wrap-nonce-12b"),
	}}
	audit := &fakeAuditSink{}
	m, _ := newTestManager(nil, read, audit, &fakeAlarmRecorder{})

	if _, err := m.Resolve(context.Background(), uuid.New(), ref, 1); err == nil {
		t.Fatal("expected decrypt failure")
	}
	if len(audit.events) != 0 {
		t.Fatalf("Resolve must not write E15/E16 (pipeline is authoritative): got %d events", len(audit.events))
	}
}

// TestRotateE5WriteFailureFailsClosed 验证 E5 失败审计写失败时 fail-closed 返回
// audit_failed（QODO-14；恢复 WEB-23 AuditedLifecycleManager 语义）。
func TestRotateE5WriteFailureFailsClosed(t *testing.T) {
	ref := uuid.New()
	tx := &fakeAtomicTx{env: &metadata.CredentialEnvelope{SecretRef: ref, Version: 2}}
	audit := &fakeAuditSink{fail: errors.New("injected audit failure")}
	alarm := &fakeAlarmRecorder{}
	m, _ := newTestManager(tx, &fakeReadStore{}, audit, alarm)

	_, err := m.Rotate(context.Background(), uuid.New(), uuid.New(), ref, 1, testPayload())
	if !IsErrorCode(err, ErrAuditFailed) {
		t.Fatalf("Rotate() error = %v, want audit_failed (fail-closed on E5 write failure)", err)
	}
	if tx.committed {
		t.Fatal("must NOT commit when rotate fails")
	}
	if len(alarm.events) == 0 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
}

// TestRotateAuditFailureRollsBack 覆盖 Rotate 成功路径 AppendAudit 失败 → 回滚
// （CR-10：Rotate 审计失败回归）。
func TestRotateAuditFailureRollsBack(t *testing.T) {
	ref := uuid.New()
	tx := &fakeAtomicTx{
		env:            &metadata.CredentialEnvelope{SecretRef: ref, Version: 1},
		appendAuditErr: errors.New("injected audit failure"),
	}
	alarm := &fakeAlarmRecorder{}
	m, _ := newTestManager(tx, &fakeReadStore{}, &fakeAuditSink{}, alarm)

	_, err := m.Rotate(context.Background(), uuid.New(), uuid.New(), ref, 1, testPayload())
	if !IsErrorCode(err, ErrAuditFailed) {
		t.Fatalf("Rotate() error = %v, want audit_failed", err)
	}
	if tx.committed {
		t.Fatal("must NOT commit when audit append fails")
	}
	if tx.rollbackCalls == 0 {
		t.Fatal("must roll back when audit append fails")
	}
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
}

// TestRetireAuditFailureRollsBack 覆盖 Retire AppendAudit 失败 → 回滚
// （CR-10：Retire 审计失败回归）。
func TestRetireAuditFailureRollsBack(t *testing.T) {
	ref := uuid.New()
	tx := &fakeAtomicTx{
		env:            &metadata.CredentialEnvelope{SecretRef: ref, Version: 1},
		appendAuditErr: errors.New("injected audit failure"),
	}
	alarm := &fakeAlarmRecorder{}
	m, _ := newTestManager(tx, &fakeReadStore{}, &fakeAuditSink{}, alarm)

	err := m.Retire(context.Background(), uuid.New(), uuid.New(), ref, 1)
	if !IsErrorCode(err, ErrAuditFailed) {
		t.Fatalf("Retire() error = %v, want audit_failed", err)
	}
	if tx.committed {
		t.Fatal("must NOT commit when audit append fails")
	}
	if tx.rollbackCalls == 0 {
		t.Fatal("must roll back when audit append fails")
	}
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
}

// TestAuditEventBuildFailedLogsRedactedRootCause 验证 auditEventBuildFailed 记录脱敏根因
// 日志（CR-4），且日志不含敏感值。
func TestAuditEventBuildFailedLogsRedactedRootCause(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	alarm := &fakeAlarmRecorder{}
	m, _ := newTestManager(&fakeAtomicTx{}, &fakeReadStore{}, &fakeAuditSink{}, alarm, logger)

	err := m.auditEventBuildFailed(context.Background(), uuid.New(), errors.New("build error: password=classified123"))
	if !IsErrorCode(err, ErrAuditFailed) {
		t.Fatalf("auditEventBuildFailed() error = %v, want audit_failed", err)
	}
	if len(alarm.events) != 1 || alarm.events[0].Code != string(ErrAuditFailed) {
		t.Fatalf("expected audit_failed alarm, got %+v", alarm.events)
	}
	out := buf.String()
	if !strings.Contains(out, "build error") {
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
