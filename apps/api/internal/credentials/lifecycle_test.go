package credentials

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
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

// TestLifecycleLogStorageFailureRedactsSensitive 验证 logger 可注入（vtiLS）：
// 脱敏返回前用注入 logger 记录底层根因，且日志不含密码/KEK/明文。
func TestLifecycleLogStorageFailureRedactsSensitive(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	store := &fakeCredentialStore{envErr: errors.New("synthetic db error")}
	manager := NewLifecycleManager(nil, store, nil, goodKEK(), logger)

	_, err := manager.Resolve(context.Background(), uuid.New(), uuid.New(), 1)
	if !IsErrorCode(err, ErrInternalError) {
		t.Fatalf("error = %v, want internal_error", err)
	}
	out := buf.String()
	if !strings.Contains(out, "synthetic db error") {
		t.Errorf("root cause not logged: %q", out)
	}
	for _, sensitive := range []string{"password", "kek=", "BEGIN"} {
		if strings.Contains(out, sensitive) {
			t.Errorf("log leaked sensitive data %q: %q", sensitive, out)
		}
	}
}

func TestLifecycleCreateDoesNotReturnReservationAfterPersistenceFailure(t *testing.T) {
	t.Parallel()

	storeErr := errors.New("synthetic persistence failure")
	store := &createOnlyCredentialStore{createErr: storeErr}
	kek := &reservationKEKProvider{}
	manager := NewLifecycleManager(nil, store, nil, kek)

	_, err := manager.Create(
		context.Background(),
		uuid.New(),
		CredentialPayload{User: "synthetic_user", Password: "synthetic_password"},
	)
	if !errors.Is(err, storeErr) {
		t.Fatalf("Create() error = %v, want persistence error", err)
	}
	if kek.reserveCalls != 1 {
		t.Fatalf("ReserveWrap calls = %d, want 1", kek.reserveCalls)
	}
}
