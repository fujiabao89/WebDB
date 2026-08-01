package execution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

type fakeConnectionReader struct {
	connections []*metadata.Connection
	err         error
	calls       int
}

func (f *fakeConnectionReader) ConnectionByID(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if len(f.connections) == 0 {
		return nil, sql.ErrNoRows
	}
	conn := f.connections[0]
	if len(f.connections) > 1 {
		f.connections = f.connections[1:]
	}
	return conn, nil
}

type fakePolicyReader struct {
	policy *metadata.ConnectionPolicy
	err    error
	calls  int
}

func (f *fakePolicyReader) PolicyByConnection(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
	f.calls++
	return f.policy, f.err
}

type fakeResolver struct {
	payload credentials.CredentialPayload
	err     error
	calls   int
}

func (f *fakeResolver) ResolveCredential(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
	f.calls++
	return f.payload, f.err
}

type fakeMemberReader struct {
	member *metadata.WorkspaceMember
	err    error
	calls  int
}

func (f *fakeMemberReader) MemberByWorkspaceAndUser(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
	f.calls++
	return f.member, f.err
}

type fakeAdapterClient struct {
	handle  *fakeAdapterHandle
	err     error
	calls   int
	configs []adapter.ConnectConfig
}

func (f *fakeAdapterClient) Get(_ context.Context, cfg adapter.ConnectConfig) (AdapterHandle, error) {
	f.calls++
	f.configs = append(f.configs, cfg)
	if f.err != nil {
		return nil, f.err
	}
	return f.handle, nil
}

type fakeAdapterHandle struct {
	result   *adapter.QueryResult
	err      error
	calls    int
	releases int
	requests []adapter.FirstPageRequest
}

func (f *fakeAdapterHandle) Query(_ context.Context, req adapter.FirstPageRequest) (*adapter.QueryResult, error) {
	f.calls++
	f.requests = append(f.requests, req)
	return f.result, f.err
}

func (f *fakeAdapterHandle) Release() {
	f.releases++
}

func boolPtr(v bool) *bool { return &v }

func testPipeline(
	connReader ConnectionReader,
	policyReader ConnectionPolicyReader,
	members WorkspaceMemberReader,
	resolver credentials.CredentialResolver,
	adapterClient AdapterClient,
) *Pipeline {
	return NewPipeline(PipelineConfig{
		Store:       connReader,
		PolicyStore: policyReader,
		Members:     members,
		Resolver:    resolver,
		Adapter:     adapterClient,
	})
}

func validPipelineInputs() (
	AuthenticatedPrincipal,
	*metadata.Connection,
	*metadata.ConnectionPolicy,
	*fakeResolver,
	*fakeAdapterClient,
) {
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
		MaxRows:            250,
	}
	resolver := &fakeResolver{
		payload: credentials.CredentialPayload{User: "synthetic_user", Password: "synthetic_password"},
	}
	handle := &fakeAdapterHandle{result: &adapter.QueryResult{}}
	client := &fakeAdapterClient{handle: handle}
	return principal, conn, policy, resolver, client
}

func TestPipelineFailClosedStageBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		sql               string
		connErr           error
		policy            *metadata.ConnectionPolicy
		policySet         bool // true: 使用 tt.policy（含 nil）；false: 使用 defaultPolicy
		policyErr         error
		memberErr         error
		resolverErr       error
		adapterErr        error
		wantCode          StableErrorCode
		wantResolverCalls int
		wantAdapterCalls  int
	}{
		{
			name:              "non workspace member",
			sql:               "SELECT 1",
			memberErr:         sql.ErrNoRows,
			wantCode:          ErrForbidden,
			wantResolverCalls: 0,
			wantAdapterCalls:  0,
		},
		{
			name:              "cross workspace connection",
			sql:               "SELECT 1",
			connErr:           sql.ErrNoRows,
			wantCode:          ErrConnectionNotFound,
			wantResolverCalls: 0,
			wantAdapterCalls:  0,
		},
		{
			name:              "SQL policy denial",
			sql:               "DELETE FROM users",
			wantCode:          StableErrorCode("statement_not_allowed"),
			wantResolverCalls: 0,
			wantAdapterCalls:  0,
		},
		{
			name:              "missing connection policy",
			sql:               "SELECT 1",
			policy:            nil,
			policySet:         true,
			wantCode:          ErrPolicyNotConfigured,
			wantResolverCalls: 0,
			wantAdapterCalls:  0,
		},
		{
			name:              "connection policy denies reads",
			sql:               "SELECT 1",
			policy:            &metadata.ConnectionPolicy{AllowRead: boolPtr(false), MaxRows: 100, StatementTimeoutMs: 1_000},
			policySet:         true,
			wantCode:          ErrReadNotAllowed,
			wantResolverCalls: 0,
			wantAdapterCalls:  0,
		},
		{
			name:              "credential retired",
			sql:               "SELECT 1",
			resolverErr:       credentials.ErrCredentialRetired,
			wantCode:          StableErrorCode(credentials.ErrCredentialRetired),
			wantResolverCalls: 1,
			wantAdapterCalls:  0,
		},
		{
			name:              "credential decrypt fails",
			sql:               "SELECT 1",
			resolverErr:       credentials.ErrDecryptionFailed,
			wantCode:          StableErrorCode(credentials.ErrDecryptionFailed),
			wantResolverCalls: 1,
			wantAdapterCalls:  0,
		},
		{
			name:              "adapter admission rejects",
			sql:               "SELECT 1",
			adapterErr:        &adapter.AdapterError{Code: adapter.ErrRateLimited},
			wantCode:          ErrRateLimited,
			wantResolverCalls: 1,
			wantAdapterCalls:  1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			principal, conn, defaultPolicy, resolver, client := validPipelineInputs()
			if tt.resolverErr != nil {
				resolver.err = tt.resolverErr
			}
			if tt.adapterErr != nil {
				client.err = tt.adapterErr
			}
			policy := defaultPolicy
			if tt.policySet {
				policy = tt.policy
			}
			store := &fakeConnectionReader{connections: []*metadata.Connection{conn}, err: tt.connErr}
			policies := &fakePolicyReader{policy: policy, err: tt.policyErr}
			members := &fakeMemberReader{
				member: &metadata.WorkspaceMember{WorkspaceID: principal.WorkspaceID, UserID: principal.UserID},
				err:    tt.memberErr,
			}
			pipeline := testPipeline(store, policies, members, resolver, client)

			result, err := pipeline.Execute(context.Background(), ExecuteRequest{
				Principal:    principal,
				ConnectionID: conn.ID,
				SQL:          tt.sql,
				Engine:       EnginePostgreSQL,
			})
			if err == nil {
				t.Fatal("Execute() error = nil")
			}
			if result.ErrorCode != tt.wantCode {
				t.Fatalf("Execute() code = %q, want %q (err=%v)", result.ErrorCode, tt.wantCode, err)
			}
			if resolver.calls != tt.wantResolverCalls {
				t.Fatalf("resolver calls = %d, want %d", resolver.calls, tt.wantResolverCalls)
			}
			if client.calls != tt.wantAdapterCalls {
				t.Fatalf("adapter Get calls = %d, want %d", client.calls, tt.wantAdapterCalls)
			}
		})
	}
}

func TestPipelineUsesPolicyBoundSinglePageAndPersistentConfigRevision(t *testing.T) {
	t.Parallel()

	principal, connV1, policy, resolver, client := validPipelineInputs()
	connV2 := *connV1
	connV2.Host = "db2.example.invalid"
	connV2.UpdatedAt = connV1.UpdatedAt.Add(time.Microsecond)
	store := &fakeConnectionReader{connections: []*metadata.Connection{connV1, &connV2}}
	pipeline := testPipeline(store, &fakePolicyReader{policy: policy}, &fakeMemberReader{member: &metadata.WorkspaceMember{WorkspaceID: principal.WorkspaceID, UserID: principal.UserID}}, resolver, client)
	req := ExecuteRequest{
		Principal:    principal,
		ConnectionID: connV1.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	}

	for i := 0; i < 2; i++ {
		result, err := pipeline.Execute(context.Background(), req)
		if err != nil {
			t.Fatalf("Execute() #%d error = %v, code=%q", i+1, err, result.ErrorCode)
		}
	}

	if len(client.configs) != 2 {
		t.Fatalf("adapter configs = %d, want 2", len(client.configs))
	}
	if client.configs[0].ConfigRevision <= 0 {
		t.Fatalf("first config revision = %d, want positive persisted revision", client.configs[0].ConfigRevision)
	}
	if client.configs[1].ConfigRevision <= client.configs[0].ConfigRevision {
		t.Fatalf(
			"config revisions = %d then %d, want strictly increasing",
			client.configs[0].ConfigRevision,
			client.configs[1].ConfigRevision,
		)
	}
	if len(client.handle.requests) != 2 {
		t.Fatalf("query requests = %d, want 2", len(client.handle.requests))
	}
	for i, queryReq := range client.handle.requests {
		if len(queryReq.SortKeys) != 0 {
			t.Fatalf("request #%d sort keys = %+v, want none without a verified unique key", i+1, queryReq.SortKeys)
		}
		if queryReq.MaxRows != policy.MaxRows || queryReq.PageSize != policy.MaxRows {
			t.Fatalf(
				"request #%d bounds = pageSize %d maxRows %d, want %d/%d",
				i+1,
				queryReq.PageSize,
				queryReq.MaxRows,
				policy.MaxRows,
				policy.MaxRows,
			)
		}
	}
}

func TestMapAdapterErrorPreservesStableAdapterClassifications(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want StableErrorCode
	}{
		{
			name: "rate limited",
			err:  &adapter.AdapterError{Code: adapter.ErrRateLimited},
			want: ErrRateLimited,
		},
		{
			name: "pool busy",
			err:  &adapter.AdapterError{Code: adapter.ErrConnPoolExhausted},
			want: ErrConnectionBusy,
		},
		{
			name: "query timeout",
			err:  &adapter.AdapterError{Code: adapter.ErrQueryTimeout},
			want: ErrExecutionTimeout,
		},
		{
			name: "query cancelled",
			err:  &adapter.AdapterError{Code: adapter.ErrQueryCanceled},
			want: ErrExecutionCancelled,
		},
		{
			name: "config conflict",
			err:  &adapter.AdapterError{Code: adapter.ErrConfigConflict},
			want: ErrConnectionConfigConflict,
		},
		{
			name: "wrapped deadline",
			err:  errors.Join(errors.New("outer"), context.DeadlineExceeded),
			want: ErrExecutionTimeout,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := mapAdapterError(tt.err); got != tt.want {
				t.Fatalf("mapAdapterError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMapCredentialErrorPreservesStableClassifications(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want StableErrorCode
	}{
		{
			name: "not found",
			err:  credentials.ErrCredentialNotFound,
			want: StableErrorCode(credentials.ErrCredentialNotFound),
		},
		{
			name: "retired",
			err:  credentials.ErrCredentialRetired,
			want: StableErrorCode(credentials.ErrCredentialRetired),
		},
		{
			name: "decryption failed",
			err:  credentials.ErrDecryptionFailed,
			want: StableErrorCode(credentials.ErrDecryptionFailed),
		},
		{
			name: "unknown KEK version",
			err:  credentials.ErrUnknownKEKVersion,
			want: StableErrorCode(credentials.ErrUnknownKEKVersion),
		},
		{
			name: "wrap quota exhausted",
			err:  credentials.ErrWrapQuotaExhausted,
			want: StableErrorCode(credentials.ErrWrapQuotaExhausted),
		},
		{
			name: "wrapped wrap quota exhausted",
			err:  fmt.Errorf("rotate: %w", credentials.ErrWrapQuotaExhausted),
			want: StableErrorCode(credentials.ErrWrapQuotaExhausted),
		},
		{
			name: "unknown error",
			err:  errors.New("some random error"),
			want: ErrInternalError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := mapCredentialError(tt.err); got != tt.want {
				t.Fatalf("mapCredentialError() = %q, want %q", got, tt.want)
			}
		})
	}
}
