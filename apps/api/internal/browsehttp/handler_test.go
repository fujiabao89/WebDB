package browsehttp

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/browse"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- 测试替身（实现 browse 导出接口） ----------------------------------------

type fMembers struct {
	fn func(ctx context.Context, wsID, userID uuid.UUID) (*metadata.WorkspaceMember, error)
}

func (m fMembers) MemberByWorkspaceAndUser(ctx context.Context, wsID, userID uuid.UUID) (*metadata.WorkspaceMember, error) {
	return m.fn(ctx, wsID, userID)
}

type fConns struct {
	byID func(ctx context.Context, wsID, id uuid.UUID) (*metadata.Connection, error)
	list func(ctx context.Context, wsID uuid.UUID) ([]metadata.Connection, error)
}

func (c fConns) ConnectionByID(ctx context.Context, wsID, id uuid.UUID) (*metadata.Connection, error) {
	return c.byID(ctx, wsID, id)
}

func (c fConns) ListConnections(ctx context.Context, wsID uuid.UUID) ([]metadata.Connection, error) {
	return c.list(ctx, wsID)
}

func (c fConns) ListConnectionsAllowed(ctx context.Context, wsID uuid.UUID, _ int) ([]metadata.Connection, error) {
	return c.list(ctx, wsID)
}

type fPolicies struct {
	byConn func(ctx context.Context, wsID, connID uuid.UUID) (*metadata.ConnectionPolicy, error)
}

func (p fPolicies) PolicyByConnection(ctx context.Context, wsID, connID uuid.UUID) (*metadata.ConnectionPolicy, error) {
	return p.byConn(ctx, wsID, connID)
}

type fResolver struct {
	fn func(ctx context.Context, wsID, secretRef uuid.UUID, version int) (credentials.CredentialPayload, error)
}

func (r fResolver) ResolveCredential(ctx context.Context, wsID, secretRef uuid.UUID, version int) (credentials.CredentialPayload, error) {
	return r.fn(ctx, wsID, secretRef, version)
}

type fBrowser struct {
	schemas func(ctx context.Context, cfg adapter.ConnectConfig, limit int) ([]adapter.Schema, error)
	tables  func(ctx context.Context, cfg adapter.ConnectConfig, schema string, limit int) ([]adapter.Table, error)
	columns func(ctx context.Context, cfg adapter.ConnectConfig, schema, table string, limit int) ([]adapter.Column, error)
}

func (b fBrowser) Schemas(ctx context.Context, cfg adapter.ConnectConfig, limit int) ([]adapter.Schema, error) {
	return b.schemas(ctx, cfg, limit)
}

func (b fBrowser) Tables(ctx context.Context, cfg adapter.ConnectConfig, schema string, limit int) ([]adapter.Table, error) {
	return b.tables(ctx, cfg, schema, limit)
}

func (b fBrowser) Columns(ctx context.Context, cfg adapter.ConnectConfig, schema, table string, limit int) ([]adapter.Column, error) {
	return b.columns(ctx, cfg, schema, table, limit)
}

func wsID() uuid.UUID { return uuid.MustParse("11111111-1111-1111-1111-111111111111") }
func uid() uuid.UUID  { return uuid.MustParse("22222222-2222-2222-2222-222222222222") }
func cid() uuid.UUID  { return uuid.MustParse("33333333-3333-3333-3333-333333333333") }

func okMember() *metadata.WorkspaceMember {
	return &metadata.WorkspaceMember{WorkspaceID: wsID(), UserID: uid(), Role: metadata.RoleViewer}
}

func okPolicy() *metadata.ConnectionPolicy {
	b := true
	return &metadata.ConnectionPolicy{WorkspaceID: wsID(), ConnectionID: cid(), AllowRead: &b}
}

func conn() *metadata.Connection {
	return &metadata.Connection{
		ID: cid(), WorkspaceID: wsID(), Name: "prod-pg",
		Engine: metadata.EnginePostgreSQL, Host: "db.internal", Port: 5432,
		Database: "app", Environment: metadata.EnvProduction,
		SecretRef: uuid.MustParse("44444444-4444-4444-4444-444444444444"), SecretVersion: 1,
		CreatedBy: uid(), UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// newTestServer 构造真实 browse.Service + browsehttp.Server，注入测试替身与固定 Principal。
func newTestServer(t *testing.T, deps browseDeps) *Server {
	t.Helper()
	svc := browse.NewService(deps.members, deps.conns, deps.policies, deps.resolver, deps.browser, browse.DefaultLimits())
	return NewServer(svc, func(r *http.Request) (browse.Principal, bool) {
		return browse.Principal{UserID: uid(), WorkspaceID: wsID()}, true
	})
}

type browseDeps struct {
	members  browse.MemberReader
	conns    browse.ConnectionReader
	policies browse.PolicyReader
	resolver credentials.CredentialResolver
	browser  browse.MetadataBrowser
}

func doReq(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeErr(t *testing.T, rr *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var eb errorBody
	if err := json.Unmarshal(rr.Body.Bytes(), &eb); err != nil {
		t.Fatalf("decode error body %q: %v", rr.Body.String(), err)
	}
	return eb
}

// ---- 连接列表 ---------------------------------------------------------------

func TestListConnections_Unauthorized(t *testing.T) {
	svc := browse.NewService(nil, nil, nil, nil, nil, browse.DefaultLimits())
	s := NewServer(svc, func(r *http.Request) (browse.Principal, bool) {
		return browse.Principal{}, false
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrUnauthorized) {
		t.Fatalf("code=%q want unauthorized", eb.Error.Code)
	}
}

func TestListConnections_WorkspaceMismatch(t *testing.T) {
	svc := browse.NewService(nil, nil, nil, nil, nil, browse.DefaultLimits())
	s := NewServer(svc, func(r *http.Request) (browse.Principal, bool) {
		return browse.Principal{UserID: uid(), WorkspaceID: uuid.MustParse("99999999-9999-9999-9999-999999999999")}, true
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrForbidden) {
		t.Fatalf("code=%q want forbidden", eb.Error.Code)
	}
}

func TestListConnections_InvalidWorkspaceID(t *testing.T) {
	s := newTestServer(t, browseDeps{})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/not-a-uuid/connections")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrInvalidScope) {
		t.Fatalf("code=%q want invalid_scope", eb.Error.Code)
	}
}

func TestListConnections_Success(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{list: func(context.Context, uuid.UUID) ([]metadata.Connection, error) {
			return []metadata.Connection{*conn()}, nil
		}},
		policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return okPolicy(), nil
		}},
		resolver: fResolver{},
		browser:  fBrowser{},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200: body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, forbidden := range []string{"host", "port", "secret_ref", "secret_version", "created_by", "workspace_id", "password", "db.internal"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaks %q: %s", forbidden, body)
		}
	}
	for _, allowed := range []string{`"name":"prod-pg"`, `"engine":"postgresql"`, `"database":"app"`} {
		if !strings.Contains(body, allowed) {
			t.Fatalf("response missing %q: %s", allowed, body)
		}
	}
}

func TestListConnections_Forbidden(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return nil, sql.ErrNoRows
		}},
		conns: fConns{list: func(context.Context, uuid.UUID) ([]metadata.Connection, error) {
			t.Fatal("must not list for non-member")
			return nil, nil
		}},
		policies: fPolicies{},
		resolver: fResolver{},
		browser:  fBrowser{},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rr.Code)
	}
}

func TestListConnections_MethodNotAllowed(t *testing.T) {
	s := newTestServer(t, browseDeps{})
	rr := doReq(t, s.Handler(), http.MethodPost, "/api/v1/workspaces/"+wsID().String()+"/connections")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rr.Code)
	}
}

// ---- Schema 浏览 ------------------------------------------------------------

func TestListSchemas_InvalidConnectionID(t *testing.T) {
	s := newTestServer(t, browseDeps{})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/not-a-uuid/schemas")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrInvalidScope) {
		t.Fatalf("code=%q want invalid_scope", eb.Error.Code)
	}
}

func TestListSchemas_ConnectionNotFound(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return nil, sql.ErrNoRows
		}},
		policies: fPolicies{},
		resolver: fResolver{},
		browser:  fBrowser{},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/schemas")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrConnectionNotFound) {
		t.Fatalf("code=%q want connection_not_found", eb.Error.Code)
	}
}

func TestListSchemas_PolicyMissing(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return conn(), nil
		}},
		policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return nil, nil // 缺失策略（生产仓储返回 nil,nil）
		}},
		resolver: fResolver{},
		browser:  fBrowser{},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/schemas")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrPolicyNotConfigured) {
		t.Fatalf("code=%q want policy_not_configured", eb.Error.Code)
	}
}

func TestListSchemas_ReadNotAllowed(t *testing.T) {
	f := false
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return conn(), nil
		}},
		policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return &metadata.ConnectionPolicy{WorkspaceID: wsID(), ConnectionID: cid(), AllowRead: &f}, nil
		}},
		resolver: fResolver{},
		browser:  fBrowser{},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/schemas")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrReadNotAllowed) {
		t.Fatalf("code=%q want read_not_allowed", eb.Error.Code)
	}
}

func TestListSchemas_Success(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return conn(), nil
		}},
		policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return okPolicy(), nil
		}},
		resolver: fResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "demo_reader", Password: "secret"}, nil
		}},
		browser: fBrowser{
			schemas: func(context.Context, adapter.ConnectConfig, int) ([]adapter.Schema, error) {
				return []adapter.Schema{{Name: "public"}}, nil
			},
		},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/schemas")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200: body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"name":"public"`) || !strings.Contains(body, `"catalog":"app"`) {
		t.Fatalf("schemas body mismatch: %s", body)
	}
	// canary：响应不得出现凭证、host、secret 引用。
	for _, forbidden := range []string{"secret", "db.internal", "password", "secret_ref"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaks %q: %s", forbidden, body)
		}
	}
}

func TestListSchemas_ConnectionUnavailable(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return conn(), nil
		}},
		policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return okPolicy(), nil
		}},
		resolver: fResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{}, credentials.ErrDecryptionFailed
		}},
		browser: fBrowser{},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/schemas")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rr.Code)
	}
	body := rr.Body.String()
	// 原始凭证错误不进入响应。
	if strings.Contains(body, "decryption") || strings.Contains(body, "kek") {
		t.Fatalf("response leaks credential detail: %s", body)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrConnectionUnavailable) {
		t.Fatalf("code=%q want connection_unavailable", eb.Error.Code)
	}
}

func TestListSchemas_DatabaseErrorRedacted(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return conn(), nil
		}},
		policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return okPolicy(), nil
		}},
		resolver: fResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: fBrowser{
			schemas: func(context.Context, adapter.ConnectConfig, int) ([]adapter.Schema, error) {
				return nil, &adapter.AdapterError{Code: adapter.ErrDatabaseError, Message: "pq: permission denied for relation secrets"}
			},
		},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/schemas")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "pq:") || strings.Contains(body, "permission denied") {
		t.Fatalf("raw database error leaked: %s", body)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrDatabaseError) {
		t.Fatalf("code=%q want database_error", eb.Error.Code)
	}
}

func TestListSchemas_TimeoutMapsTo504(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return conn(), nil
		}},
		policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return okPolicy(), nil
		}},
		resolver: fResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: fBrowser{
			schemas: func(ctx context.Context, _ adapter.ConnectConfig, _ int) ([]adapter.Schema, error) {
				return nil, context.DeadlineExceeded
			},
		},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/schemas")
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d want 504", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrQueryTimeout) {
		t.Fatalf("code=%q want query_timeout", eb.Error.Code)
	}
}

func TestListSchemas_CancelledMapsTo499(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return conn(), nil
		}},
		policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return okPolicy(), nil
		}},
		resolver: fResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: fBrowser{
			schemas: func(ctx context.Context, _ adapter.ConnectConfig, _ int) ([]adapter.Schema, error) {
				return nil, context.Canceled
			},
		},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/schemas")
	if rr.Code != 499 {
		t.Fatalf("status=%d want 499", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrQueryCancelled) {
		t.Fatalf("code=%q want query_cancelled", eb.Error.Code)
	}
}

// TestListSchemas_DatabaseErrorCancelMapsTo499 验证流式读取中数据库错误包装的取消
// cause 映射为 499/query_cancelled，而非 500/database_error（WEB-36 P1）。
func TestListSchemas_DatabaseErrorCancelMapsTo499(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return conn(), nil
		}},
		policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return okPolicy(), nil
		}},
		resolver: fResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: fBrowser{
			schemas: func(context.Context, adapter.ConnectConfig, int) ([]adapter.Schema, error) {
				return nil, adapter.WrapDatabaseError(context.Canceled)
			},
		},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/schemas")
	if rr.Code != 499 {
		t.Fatalf("status=%d want 499", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrQueryCancelled) {
		t.Fatalf("code=%q want query_cancelled", eb.Error.Code)
	}
}

// TestListSchemas_DatabaseErrorTimeoutMapsTo504 验证数据库错误包装的超时 cause
// 映射为 504/query_timeout，而非 500/database_error。
func TestListSchemas_DatabaseErrorTimeoutMapsTo504(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return conn(), nil
		}},
		policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return okPolicy(), nil
		}},
		resolver: fResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: fBrowser{
			schemas: func(context.Context, adapter.ConnectConfig, int) ([]adapter.Schema, error) {
				return nil, adapter.WrapDatabaseError(context.DeadlineExceeded)
			},
		},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/schemas")
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d want 504", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrQueryTimeout) {
		t.Fatalf("code=%q want query_timeout", eb.Error.Code)
	}
}

// TestListSchemas_429RetryAfter 验证 429 + Retry-After 头（F6，P0-06A §12 D15d）。
func TestListSchemas_429RetryAfter(t *testing.T) {
	cases := []struct {
		name string
		code adapter.ErrorCode
		want string
	}{
		{"rate_limited", adapter.ErrRateLimited, string(browse.ErrRateLimited)},
		{"connection_busy", adapter.ErrConnPoolExhausted, string(browse.ErrConnectionBusy)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, browseDeps{
				members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
					return okMember(), nil
				}},
				conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
					return conn(), nil
				}},
				policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
					return okPolicy(), nil
				}},
				resolver: fResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
					return credentials.CredentialPayload{User: "u", Password: "p"}, nil
				}},
				browser: fBrowser{
					schemas: func(context.Context, adapter.ConnectConfig, int) ([]adapter.Schema, error) {
						return nil, &adapter.AdapterError{Code: tc.code}
					},
				},
			})
			rr := doReq(t, s.Handler(), http.MethodGet,
				"/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/schemas")
			if rr.Code != http.StatusTooManyRequests {
				t.Fatalf("status=%d want 429", rr.Code)
			}
			if got := rr.Header().Get("Retry-After"); got == "" {
				t.Fatal("missing Retry-After header")
			}
			if eb := decodeErr(t, rr); eb.Error.Code != tc.want {
				t.Fatalf("code=%q want %q", eb.Error.Code, tc.want)
			}
		})
	}
}

// ---- 标识符校验（CT-16） -----------------------------------------------------

func TestListTables_MissingSchema(t *testing.T) {
	s := newTestServer(t, browseDeps{})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/tables")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrInvalidScope) {
		t.Fatalf("code=%q want invalid_scope", eb.Error.Code)
	}
}

func TestListTables_InvalidSchemaInjection(t *testing.T) {
	s := newTestServer(t, browseDeps{})
	for _, bad := range []string{"public;DROP TABLE users;--", `public"`, "p'ublic", "public--", "a b"} {
		rr := doReq(t, s.Handler(), http.MethodGet,
			"/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/tables?schema="+urlQueryEscape(bad))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("schema=%q status=%d want 400", bad, rr.Code)
		}
		if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrInvalidScope) {
			t.Fatalf("schema=%q code=%q want invalid_scope", bad, eb.Error.Code)
		}
	}
}

func TestListTables_LongSchemaRejected(t *testing.T) {
	s := newTestServer(t, browseDeps{})
	long := strings.Repeat("a", 64)
	rr := doReq(t, s.Handler(), http.MethodGet,
		"/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/tables?schema="+long)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestListColumns_MissingTable(t *testing.T) {
	s := newTestServer(t, browseDeps{})
	rr := doReq(t, s.Handler(), http.MethodGet,
		"/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/columns?schema=public")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestListColumns_Success(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return conn(), nil
		}},
		policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return okPolicy(), nil
		}},
		resolver: fResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: fBrowser{
			columns: func(context.Context, adapter.ConnectConfig, string, string, int) ([]adapter.Column, error) {
				return []adapter.Column{{Name: "id", Ordinal: 1, NativeType: "int4", Nullable: false, HasDefault: true}}, nil
			},
		},
	})
	rr := doReq(t, s.Handler(), http.MethodGet,
		"/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/columns?schema=public&table=users")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200: body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"name":"id"`) || !strings.Contains(body, `"native_type":"int4"`) {
		t.Fatalf("columns body mismatch: %s", body)
	}
}

func urlQueryEscape(s string) string {
	return url.QueryEscape(s)
}

// TestNewServer_NilServicePanics 验证装配错误 fail-fast（F3）：nil browse.Service 必须 panic。
func TestNewServer_NilServicePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil browse.Service")
		}
	}()
	NewServer(nil, func(r *http.Request) (browse.Principal, bool) {
		return browse.Principal{}, true
	})
}

// TestNewServer_NilPrincipalProviderPanics 验证缺失可信 Principal 配置时构造期拒绝
// （D01b/CT-18：启动失败而非每请求 401）。
func TestNewServer_NilPrincipalProviderPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil PrincipalProvider")
		}
	}()
	svc := browse.NewService(nil, nil, nil, nil, nil, browse.DefaultLimits())
	NewServer(svc, nil)
}

// TestBoundListCtx_DefaultFiveSeconds 验证连接列表元数据库查询默认超时为 5s
// （P0-06A §6：默认 5s、上限 10s），而非把 10s 上限当作默认。
func TestBoundListCtx_DefaultFiveSeconds(t *testing.T) {
	s := &Server{listTimeout: 0} // 未配置 → 兜底默认 5s
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, cancel := s.boundListCtx(req)
	defer cancel()
	d, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline on list ctx")
	}
	remaining := time.Until(d)
	if remaining <= 4*time.Second || remaining > defaultListTimeout {
		t.Fatalf("default list deadline want ≈%v, got %v", defaultListTimeout, remaining)
	}
}

// TestBoundListCtx_CappedAtTenSeconds 验证配置超上限时被钳制到 10s（契约上限，
// 上限与默认分离）。
func TestBoundListCtx_CappedAtTenSeconds(t *testing.T) {
	s := &Server{listTimeout: 30 * time.Second} // 配置漂移超上限 → 钳到 10s
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, cancel := s.boundListCtx(req)
	defer cancel()
	d, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline on list ctx")
	}
	remaining := time.Until(d)
	if remaining <= 9*time.Second || remaining > maxListTimeout {
		t.Fatalf("capped list deadline want ≈%v, got %v", maxListTimeout, remaining)
	}
}

// TestWriteData_TooLarge 验证 browsehttp 成功 envelope 的字节预算（F2 方案 A）：
// 序列化后超过 8 MiB 返回 422 result_too_large，而非写出 200。
func TestWriteData_TooLarge(t *testing.T) {
	w := httptest.NewRecorder()
	writeData(w, strings.Repeat("a", browse.MaxResponseBytes+1))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, string(browse.ErrResultTooLarge)) {
		t.Fatalf("body missing result_too_large: %s", body)
	}
}

// TestListSchemas_ResponseByteTooLarge 验证 service 层字节预算穿透到 handler（422）。
func TestListSchemas_ResponseByteTooLarge(t *testing.T) {
	s := newTestServer(t, browseDeps{
		members: fMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return okMember(), nil
		}},
		conns: fConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return conn(), nil
		}},
		policies: fPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return okPolicy(), nil
		}},
		resolver: fResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: fBrowser{
			schemas: func(context.Context, adapter.ConnectConfig, int) ([]adapter.Schema, error) {
				return []adapter.Schema{{Name: strings.Repeat("a", browse.MaxResponseBytes)}}, nil
			},
		},
	})
	rr := doReq(t, s.Handler(), http.MethodGet, "/api/v1/workspaces/"+wsID().String()+"/connections/"+cid().String()+"/schemas")
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422", rr.Code)
	}
	if eb := decodeErr(t, rr); eb.Error.Code != string(browse.ErrResultTooLarge) {
		t.Fatalf("code=%q want result_too_large", eb.Error.Code)
	}
}
