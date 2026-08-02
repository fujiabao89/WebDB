package credentials

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

type createOnlyCredentialStore struct {
	createErr   error
	createCalls int
	events      *[]string
}

func (s *createOnlyCredentialStore) CreateEnvelope(context.Context, *metadata.CredentialEnvelope) error {
	s.createCalls++
	if s.events != nil {
		*s.events = append(*s.events, "create")
	}
	return s.createErr
}

func (*createOnlyCredentialStore) EnvelopeByRef(context.Context, uuid.UUID, uuid.UUID, int) (*metadata.CredentialEnvelope, error) {
	panic("unexpected EnvelopeByRef call")
}

func (*createOnlyCredentialStore) ListEnvelopes(context.Context, uuid.UUID) ([]metadata.CredentialEnvelope, error) {
	panic("unexpected ListEnvelopes call")
}

func (*createOnlyCredentialStore) LockEnvelopeForUpdate(context.Context, *sql.Tx, uuid.UUID, uuid.UUID) (*metadata.CredentialEnvelope, error) {
	panic("unexpected LockEnvelopeForUpdate call")
}

func (*createOnlyCredentialStore) LockEnvelopeVersion(context.Context, *sql.Tx, uuid.UUID, uuid.UUID, int) (*metadata.CredentialEnvelope, error) {
	panic("unexpected LockEnvelopeVersion call")
}

func (*createOnlyCredentialStore) InsertEnvelopeTx(context.Context, *sql.Tx, *metadata.CredentialEnvelope) error {
	panic("unexpected InsertEnvelopeTx call")
}

func (*createOnlyCredentialStore) ListEnvelopesByRef(context.Context, uuid.UUID, uuid.UUID) ([]metadata.CredentialEnvelope, error) {
	panic("unexpected ListEnvelopesByRef call")
}

func (*createOnlyCredentialStore) UpdateRetiredAt(context.Context, *sql.Tx, uuid.UUID, uuid.UUID, int) error {
	panic("unexpected UpdateRetiredAt call")
}

type reservationKEKProvider struct {
	reserveErr   error
	reserveCalls int
	events       *[]string
}

func (*reservationKEKProvider) ActiveKEK() (int, []byte, error) {
	return 1, make([]byte, kekSize), nil
}

func (*reservationKEKProvider) GetKEK(int) ([]byte, error) {
	return make([]byte, kekSize), nil
}

func (p *reservationKEKProvider) ReserveWrap(int) error {
	p.reserveCalls++
	if p.events != nil {
		*p.events = append(*p.events, "reserve")
	}
	return p.reserveErr
}

func (p *reservationKEKProvider) WrapCount(int) uint64 {
	return uint64(p.reserveCalls)
}

func TestLifecycleCreateReservesWrapBeforePersistence(t *testing.T) {
	t.Parallel()

	var events []string
	store := &createOnlyCredentialStore{events: &events}
	kek := &reservationKEKProvider{events: &events}
	manager := NewLifecycleManager(nil, store, nil, kek)

	env, err := manager.Create(
		context.Background(),
		uuid.New(),
		CredentialPayload{User: "synthetic_user", Password: "synthetic_password"},
	)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if env == nil {
		t.Fatal("Create() envelope = nil")
	}
	if got, want := events, []string{"reserve", "create"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestLifecycleCreateRejectsBeforeSealAndPersistenceWhenWrapLimitReached(t *testing.T) {
	t.Parallel()

	quotaErr := errors.New("wrap quota exhausted")
	store := &createOnlyCredentialStore{}
	kek := &reservationKEKProvider{reserveErr: quotaErr}
	manager := NewLifecycleManager(nil, store, nil, kek)

	env, err := manager.Create(
		context.Background(),
		uuid.New(),
		CredentialPayload{User: "synthetic_user", Password: "synthetic_password"},
	)
	if !errors.Is(err, quotaErr) {
		t.Fatalf("Create() error = %v, want reservation error", err)
	}
	if env != nil {
		t.Fatalf("Create() envelope = %+v, want nil", env)
	}
	if store.createCalls != 0 {
		t.Fatalf("CreateEnvelope calls = %d, want 0", store.createCalls)
	}
}

// TestLifecycleLogStorageFailureRedactsSensitive 验证 logger 可注入（vtiLS）且
// logStorageFailure 在写入日志前统一脱敏（vti-OZ）：注入含真实凭证内容的底层错误，
// 断言敏感值与原始片段不出现在日志、脱敏占位符生效。
func TestLifecycleLogStorageFailureRedactsSensitive(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	sensitive := "password=classified123"
	store := &fakeCredentialStore{envErr: errors.New("db error: " + sensitive)}
	manager := NewLifecycleManager(nil, store, nil, goodKEK(), logger)

	_, err := manager.Resolve(context.Background(), uuid.New(), uuid.New(), 1)
	if !IsErrorCode(err, ErrInternalError) {
		t.Fatalf("error = %v, want internal_error", err)
	}
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

func TestLifecycleCreateDoesNotReturnReservationAfterPersistenceFailure(t *testing.T) {
	t.Parallel()

	storeErr := errors.New("synthetic persistence failure")
	store := &createOnlyCredentialStore{createErr: storeErr}
	kek := &reservationKEKProvider{}
	manager := NewLifecycleManager(nil, store, nil, kek,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := manager.Create(
		context.Background(),
		uuid.New(),
		CredentialPayload{User: "synthetic_user", Password: "synthetic_password"},
	)
	// 存储故障返回脱敏 internal_error（outside finding 18），不再把底层错误文本透出。
	if !IsErrorCode(err, ErrInternalError) {
		t.Fatalf("Create() error = %v, want internal_error (sanitized)", err)
	}
	if kek.reserveCalls != 1 {
		t.Fatalf("ReserveWrap calls = %d, want 1", kek.reserveCalls)
	}
}
