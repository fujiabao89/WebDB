package browse

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- 测试替身 ---------------------------------------------------------------

type stubMembers struct {
	fn func(ctx context.Context, wsID, userID uuid.UUID) (*metadata.WorkspaceMember, error)
}

func (m stubMembers) MemberByWorkspaceAndUser(ctx context.Context, wsID, userID uuid.UUID) (*metadata.WorkspaceMember, error) {
	return m.fn(ctx, wsID, userID)
}

type stubConns struct {
	byID        func(ctx context.Context, wsID, id uuid.UUID) (*metadata.Connection, error)
	list        func(ctx context.Context, wsID uuid.UUID) ([]metadata.Connection, error)
	listAllowed func(ctx context.Context, wsID uuid.UUID, limit int) ([]metadata.Connection, error)
}

func (c stubConns) ConnectionByID(ctx context.Context, wsID, id uuid.UUID) (*metadata.Connection, error) {
	return c.byID(ctx, wsID, id)
}

// ListConnectionsAllowed 默认委托 list（过滤已在仓储层，见 P1-2）；测试可用 listAllowed 覆盖 limit。
func (c stubConns) ListConnectionsAllowed(ctx context.Context, wsID uuid.UUID, limit int) ([]metadata.Connection, error) {
	if c.listAllowed != nil {
		return c.listAllowed(ctx, wsID, limit)
	}
	return c.list(ctx, wsID)
}

type stubPolicies struct {
	byConn func(ctx context.Context, wsID, connID uuid.UUID) (*metadata.ConnectionPolicy, error)
}

func (p stubPolicies) PolicyByConnection(ctx context.Context, wsID, connID uuid.UUID) (*metadata.ConnectionPolicy, error) {
	return p.byConn(ctx, wsID, connID)
}

type countingResolver struct {
	calls int
	fn    func(ctx context.Context, wsID, secretRef uuid.UUID, version int) (credentials.CredentialPayload, error)
}

func (r *countingResolver) ResolveCredential(ctx context.Context, wsID, secretRef uuid.UUID, version int) (credentials.CredentialPayload, error) {
	r.calls++
	return r.fn(ctx, wsID, secretRef, version)
}

type countingBrowser struct {
	calls   int
	schemas func(ctx context.Context, cfg adapter.ConnectConfig, limit int) ([]adapter.Schema, error)
	tables  func(ctx context.Context, cfg adapter.ConnectConfig, schema string, limit int) ([]adapter.Table, error)
	columns func(ctx context.Context, cfg adapter.ConnectConfig, schema, table string, limit int) ([]adapter.Column, error)
}

func (b *countingBrowser) Schemas(ctx context.Context, cfg adapter.ConnectConfig, limit int) ([]adapter.Schema, error) {
	b.calls++
	return b.schemas(ctx, cfg, limit)
}

func (b *countingBrowser) Tables(ctx context.Context, cfg adapter.ConnectConfig, schema string, limit int) ([]adapter.Table, error) {
	b.calls++
	return b.tables(ctx, cfg, schema, limit)
}

func (b *countingBrowser) Columns(ctx context.Context, cfg adapter.ConnectConfig, schema, table string, limit int) ([]adapter.Column, error) {
	b.calls++
	return b.columns(ctx, cfg, schema, table, limit)
}

type browseDeps struct {
	members  MemberReader
	conns    ConnectionReader
	policies PolicyReader
	resolver credentials.CredentialResolver
	browser  MetadataBrowser
}

func newService(t *testing.T, d browseDeps) *Service {
	t.Helper()
	return NewService(d.members, d.conns, d.policies, d.resolver, d.browser, DefaultLimits())
}

func boolPtr(b bool) *bool { return &b }

func wsID() uuid.UUID { return uuid.MustParse("11111111-1111-1111-1111-111111111111") }
func uid() uuid.UUID  { return uuid.MustParse("22222222-2222-2222-2222-222222222222") }
func connID() uuid.UUID {
	return uuid.MustParse("33333333-3333-3333-3333-333333333333")
}
func secretRef() uuid.UUID {
	return uuid.MustParse("44444444-4444-4444-4444-444444444444")
}

func readRole() metadata.WorkspaceMember {
	return metadata.WorkspaceMember{WorkspaceID: wsID(), UserID: uid(), Role: metadata.RoleViewer}
}

func deniedRole() metadata.WorkspaceMember {
	return metadata.WorkspaceMember{WorkspaceID: wsID(), UserID: uid(), Role: "superuser"}
}

func testConn() *metadata.Connection {
	return &metadata.Connection{
		ID:            connID(),
		WorkspaceID:   wsID(),
		Name:          "prod-pg",
		Engine:        metadata.EnginePostgreSQL,
		Host:          "db.internal",
		Port:          5432,
		Database:      "app",
		Environment:   metadata.EnvProduction,
		SecretRef:     secretRef(),
		SecretVersion: 1,
		CreatedBy:     uid(),
		UpdatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func readPolicy(allow bool) *metadata.ConnectionPolicy {
	return &metadata.ConnectionPolicy{WorkspaceID: wsID(), ConnectionID: connID(), AllowRead: boolPtr(allow)}
}

func principal() Principal { return Principal{UserID: uid(), WorkspaceID: wsID()} }

// allowedDeps 构造授权全部通过的依赖（member=viewer、连接存在、policy allow_read=true、
// 凭证可解析），使测试聚焦于浏览行为而非授权矩阵。
func allowedDeps(browser MetadataBrowser) browseDeps {
	return browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: browser,
	}
}

// ---- 授权矩阵：schema 浏览 ---------------------------------------------------

func TestAuthorize_NoPrincipal(t *testing.T) {
	resolver := &countingResolver{}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: resolver,
		browser:  browser,
	})
	_, err := s.ListSchemas(context.Background(), Principal{}, connID())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected unauthorized, got %v", err)
	}
	if resolver.calls != 0 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 0/0", resolver.calls, browser.calls)
	}
}

func TestAuthorize_InactiveUser(t *testing.T) {
	resolver := &countingResolver{}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return nil, sql.ErrNoRows
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: resolver,
		browser:  browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden, got %v", err)
	}
	if resolver.calls != 0 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 0/0", resolver.calls, browser.calls)
	}
}

func TestAuthorize_NonMember(t *testing.T) {
	resolver := &countingResolver{}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return nil, sql.ErrNoRows
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: resolver,
		browser:  browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden, got %v", err)
	}
	if resolver.calls != 0 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 0/0", resolver.calls, browser.calls)
	}
}

func TestAuthorize_UnknownRole(t *testing.T) {
	resolver := &countingResolver{}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := deniedRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: resolver,
		browser:  browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden, got %v", err)
	}
	if resolver.calls != 0 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 0/0", resolver.calls, browser.calls)
	}
}

func TestAuthorize_MemberLookupError(t *testing.T) {
	resolver := &countingResolver{}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return nil, errors.New("member store down")
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: resolver,
		browser:  browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrInternalError) {
		t.Fatalf("expected internal_error, got %v", err)
	}
	if resolver.calls != 0 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 0/0", resolver.calls, browser.calls)
	}
}

func TestAuthorize_CrossWorkspaceConnection(t *testing.T) {
	resolver := &countingResolver{}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return nil, sql.ErrNoRows // 跨工作区连接在 wsID 作用域下查不到
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: resolver,
		browser:  browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("expected connection_not_found, got %v", err)
	}
	if resolver.calls != 0 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 0/0", resolver.calls, browser.calls)
	}
}

func TestAuthorize_ConnectionStoreError(t *testing.T) {
	resolver := &countingResolver{}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return nil, errors.New("conn store down")
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: resolver,
		browser:  browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrInternalError) {
		t.Fatalf("expected internal_error, got %v", err)
	}
	if resolver.calls != 0 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 0/0", resolver.calls, browser.calls)
	}
}

func TestAuthorize_PolicyMissing(t *testing.T) {
	resolver := &countingResolver{}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return nil, nil // 缺失策略（生产仓储返回 nil,nil）
		}},
		resolver: resolver,
		browser:  browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrPolicyNotConfigured) {
		t.Fatalf("expected policy_not_configured, got %v", err)
	}
	if resolver.calls != 0 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 0/0", resolver.calls, browser.calls)
	}
}

func TestAuthorize_AllowReadNil(t *testing.T) {
	resolver := &countingResolver{}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return &metadata.ConnectionPolicy{WorkspaceID: wsID(), ConnectionID: connID()}, nil
		}},
		resolver: resolver,
		browser:  browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrReadNotAllowed) {
		t.Fatalf("expected read_not_allowed, got %v", err)
	}
	if resolver.calls != 0 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 0/0", resolver.calls, browser.calls)
	}
}

func TestAuthorize_AllowReadFalse(t *testing.T) {
	resolver := &countingResolver{}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(false), nil
		}},
		resolver: resolver,
		browser:  browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrReadNotAllowed) {
		t.Fatalf("expected read_not_allowed, got %v", err)
	}
	if resolver.calls != 0 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 0/0", resolver.calls, browser.calls)
	}
}

func TestAuthorize_PolicyStoreError(t *testing.T) {
	resolver := &countingResolver{}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return nil, errors.New("policy store down")
		}},
		resolver: resolver,
		browser:  browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrInternalError) {
		t.Fatalf("expected internal_error, got %v", err)
	}
	if resolver.calls != 0 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 0/0", resolver.calls, browser.calls)
	}
}

func TestAuthorize_CredentialFailure(t *testing.T) {
	resolver := &countingResolver{
		fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{}, credentials.ErrDecryptionFailed
		},
	}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: resolver,
		browser:  browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrConnectionUnavailable) {
		t.Fatalf("expected connection_unavailable, got %v", err)
	}
	if resolver.calls != 1 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 1/0", resolver.calls, browser.calls)
	}
}

// ---- schema 浏览成功与错误路径 ----------------------------------------------

func TestListSchemas_Success(t *testing.T) {
	resolver := &countingResolver{
		fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "demo_reader", Password: "secret"}, nil
		},
	}
	browser := &countingBrowser{
		schemas: func(context.Context, adapter.ConnectConfig, int) ([]adapter.Schema, error) {
			return []adapter.Schema{{Name: "public"}, {Name: "app"}}, nil
		},
	}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: resolver,
		browser:  browser,
	})
	out, err := s.ListSchemas(context.Background(), principal(), connID())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d schemas, want 2", len(out))
	}
	// catalog 由连接 database 派生（P0-06A §7 catalog=目标库名）。
	if out[0].Name != "public" || out[0].Catalog != "app" {
		t.Fatalf("schema DTO mismatch: %+v", out[0])
	}
	if resolver.calls != 1 || browser.calls != 1 {
		t.Fatalf("resolver=%d browser=%d calls; want 1/1", resolver.calls, browser.calls)
	}
}

func TestListSchemas_ResultTooLarge(t *testing.T) {
	over := make([]adapter.Schema, DefaultLimits().MaxEntries+1)
	for i := range over {
		over[i] = adapter.Schema{Name: "s"}
	}
	browser := &countingBrowser{
		schemas: func(context.Context, adapter.ConnectConfig, int) ([]adapter.Schema, error) { return over, nil },
	}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("expected result_too_large, got %v", err)
	}
}

func TestListSchemas_ResponseTooLarge(t *testing.T) {
	// F2（方案 A）：DTO 序列化后超过 8 MiB 字节预算 → result_too_large，不静默截断。
	huge := strings.Repeat("a", MaxResponseBytes+1)
	browser := &countingBrowser{
		schemas: func(context.Context, adapter.ConnectConfig, int) ([]adapter.Schema, error) {
			return []adapter.Schema{{Name: huge}}, nil
		},
	}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: browser,
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("expected result_too_large, got %v", err)
	}
}

func TestAdapterBrowser_NilManager(t *testing.T) {
	// F3：AdapterBrowser 未注入 Manager 时不得 nil 解引用，返回 internal_error。
	b := AdapterBrowser{}
	_, err := b.Schemas(context.Background(), adapter.ConnectConfig{}, DefaultLimits().MaxEntries+1)
	if !errors.Is(err, ErrInternalError) {
		t.Fatalf("expected internal_error, got %v", err)
	}
}

func TestListSchemas_Cancelled(t *testing.T) {
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: &countingBrowser{
			schemas: func(ctx context.Context, _ adapter.ConnectConfig, _ int) ([]adapter.Schema, error) {
				return nil, context.Canceled
			},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.ListSchemas(ctx, principal(), connID())
	if !errors.Is(err, ErrQueryCancelled) {
		t.Fatalf("expected query_cancelled, got %v", err)
	}
}

func TestListSchemas_Timeout(t *testing.T) {
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: &countingBrowser{
			schemas: func(ctx context.Context, _ adapter.ConnectConfig, _ int) ([]adapter.Schema, error) {
				return nil, context.DeadlineExceeded
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := s.ListSchemas(ctx, principal(), connID())
	if !errors.Is(err, ErrQueryTimeout) {
		t.Fatalf("expected query_timeout, got %v", err)
	}
}

func TestListSchemas_AdapterDatabaseError(t *testing.T) {
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: &countingBrowser{
			schemas: func(context.Context, adapter.ConnectConfig, int) ([]adapter.Schema, error) {
				return nil, &adapter.AdapterError{Code: adapter.ErrDatabaseError}
			},
		},
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrDatabaseError) {
		t.Fatalf("expected database_error, got %v", err)
	}
}

func TestListSchemas_AdapterConnectionBusy(t *testing.T) {
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: &countingBrowser{
			schemas: func(context.Context, adapter.ConnectConfig, int) ([]adapter.Schema, error) {
				// 模拟真实池耗尽错误链：AdapterError 被外层包装（真实场景 cause 可为
				// deadline）；mapAdapterError 必须先 errors.As 提取 code，不被 context 掩盖（P2-4）。
				return nil, fmt.Errorf("outer: %w", &adapter.AdapterError{Code: adapter.ErrConnPoolExhausted, Message: "pool exhausted"})
			},
		},
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrConnectionBusy) {
		t.Fatalf("expected connection_busy, got %v", err)
	}
}

func TestListSchemas_AdapterConnectionFailed(t *testing.T) {
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: &countingBrowser{
			schemas: func(context.Context, adapter.ConnectConfig, int) ([]adapter.Schema, error) {
				return nil, &adapter.AdapterError{Code: adapter.ErrConnectionFailed}
			},
		},
	})
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrConnectionUnavailable) {
		t.Fatalf("expected connection_unavailable, got %v", err)
	}
}

func TestListTables_Success(t *testing.T) {
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: &countingBrowser{
			tables: func(context.Context, adapter.ConnectConfig, string, int) ([]adapter.Table, error) {
				return []adapter.Table{{Schema: "public", Name: "users", Type: adapter.TableTypeTable}}, nil
			},
		},
	})
	out, err := s.ListTables(context.Background(), principal(), connID(), "public")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "users" || out[0].Type != string(adapter.TableTypeTable) {
		t.Fatalf("tables DTO mismatch: %+v", out)
	}
}

func TestListColumns_Success(t *testing.T) {
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: &countingBrowser{
			columns: func(context.Context, adapter.ConnectConfig, string, string, int) ([]adapter.Column, error) {
				return []adapter.Column{{Name: "id", Ordinal: 1, NativeType: "int4", Nullable: false, HasDefault: true}}, nil
			},
		},
	})
	out, err := s.ListColumns(context.Background(), principal(), connID(), "public", "users")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "id" || out[0].Ordinal != 1 || out[0].NativeType != "int4" {
		t.Fatalf("columns DTO mismatch: %+v", out)
	}
}

func TestListTables_EmptySchemaRejected(t *testing.T) {
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: &countingBrowser{},
	})
	_, err := s.ListTables(context.Background(), principal(), connID(), "")
	if !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("expected invalid_scope, got %v", err)
	}
}

func TestListColumns_EmptyTableRejected(t *testing.T) {
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{byID: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.Connection, error) {
			return testConn(), nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{fn: func(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
			return credentials.CredentialPayload{User: "u", Password: "p"}, nil
		}},
		browser: &countingBrowser{},
	})
	_, err := s.ListColumns(context.Background(), principal(), connID(), "public", "")
	if !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("expected invalid_scope, got %v", err)
	}
}

// ---- 有界 limit 下传（WEB-36 P1）---------------------------------------------

// TestListSchemas_PassesSentinelLimit 验证 Service 向 Browser 传 MaxEntries+1，
// 使查询层能在超限前停止，而非先累积完整 catalog 再拒绝。
func TestListSchemas_PassesSentinelLimit(t *testing.T) {
	var got int
	browser := &countingBrowser{
		schemas: func(_ context.Context, _ adapter.ConnectConfig, limit int) ([]adapter.Schema, error) {
			got = limit
			return nil, nil
		},
	}
	s := newService(t, allowedDeps(browser))
	if _, err := s.ListSchemas(context.Background(), principal(), connID()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != DefaultLimits().MaxEntries+1 {
		t.Fatalf("Schemas limit=%d, want %d (MaxEntries+1)", got, DefaultLimits().MaxEntries+1)
	}
}

func TestListTables_PassesSentinelLimit(t *testing.T) {
	var got int
	browser := &countingBrowser{
		tables: func(_ context.Context, _ adapter.ConnectConfig, _ string, limit int) ([]adapter.Table, error) {
			got = limit
			return nil, nil
		},
	}
	s := newService(t, allowedDeps(browser))
	if _, err := s.ListTables(context.Background(), principal(), connID(), "public"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != DefaultLimits().MaxEntries+1 {
		t.Fatalf("Tables limit=%d, want %d (MaxEntries+1)", got, DefaultLimits().MaxEntries+1)
	}
}

func TestListColumns_PassesSentinelLimit(t *testing.T) {
	var got int
	browser := &countingBrowser{
		columns: func(_ context.Context, _ adapter.ConnectConfig, _, _ string, limit int) ([]adapter.Column, error) {
			got = limit
			return nil, nil
		},
	}
	s := newService(t, allowedDeps(browser))
	if _, err := s.ListColumns(context.Background(), principal(), connID(), "public", "users"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != DefaultLimits().MaxEntries+1 {
		t.Fatalf("Columns limit=%d, want %d (MaxEntries+1)", got, DefaultLimits().MaxEntries+1)
	}
}

// TestListSchemas_ExactlyAtLimitOK 验证 sentinel 语义：恰好 MaxEntries 条属于正常，
// 不触发 result_too_large（区别于 MaxEntries+1 的超限）。
func TestListSchemas_ExactlyAtLimitOK(t *testing.T) {
	at := make([]adapter.Schema, DefaultLimits().MaxEntries)
	for i := range at {
		at[i] = adapter.Schema{Name: "s"}
	}
	browser := &countingBrowser{
		schemas: func(_ context.Context, _ adapter.ConnectConfig, _ int) ([]adapter.Schema, error) { return at, nil },
	}
	s := newService(t, allowedDeps(browser))
	out, err := s.ListSchemas(context.Background(), principal(), connID())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != DefaultLimits().MaxEntries {
		t.Fatalf("got %d schemas, want %d", len(out), DefaultLimits().MaxEntries)
	}
}

func TestListSchemas_EmptyOK(t *testing.T) {
	browser := &countingBrowser{
		schemas: func(_ context.Context, _ adapter.ConnectConfig, _ int) ([]adapter.Schema, error) { return nil, nil },
	}
	s := newService(t, allowedDeps(browser))
	out, err := s.ListSchemas(context.Background(), principal(), connID())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("got %d schemas, want 0", len(out))
	}
}

func TestListTables_ResultTooLarge(t *testing.T) {
	over := make([]adapter.Table, DefaultLimits().MaxEntries+1)
	browser := &countingBrowser{
		tables: func(_ context.Context, _ adapter.ConnectConfig, _ string, _ int) ([]adapter.Table, error) {
			return over, nil
		},
	}
	s := newService(t, allowedDeps(browser))
	_, err := s.ListTables(context.Background(), principal(), connID(), "public")
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("expected result_too_large, got %v", err)
	}
}

func TestListColumns_ResultTooLarge(t *testing.T) {
	over := make([]adapter.Column, DefaultLimits().MaxEntries+1)
	browser := &countingBrowser{
		columns: func(_ context.Context, _ adapter.ConnectConfig, _, _ string, _ int) ([]adapter.Column, error) {
			return over, nil
		},
	}
	s := newService(t, allowedDeps(browser))
	_, err := s.ListColumns(context.Background(), principal(), connID(), "public", "users")
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("expected result_too_large, got %v", err)
	}
}

// ---- 数据库错误映射保留取消语义（WEB-36 P1）----------------------------------

// TestListSchemas_MidStreamCancellationMapped 验证流式读取中 rows.Err() 包装的取消
// cause 被识别为 query_cancelled（而非 database_error/500）。
func TestListSchemas_MidStreamCancellationMapped(t *testing.T) {
	browser := &countingBrowser{
		schemas: func(_ context.Context, _ adapter.ConnectConfig, _ int) ([]adapter.Schema, error) {
			return nil, adapter.WrapDatabaseError(context.Canceled)
		},
	}
	s := newService(t, allowedDeps(browser))
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrQueryCancelled) {
		t.Fatalf("expected query_cancelled, got %v", err)
	}
}

func TestListSchemas_MidStreamTimeoutMapped(t *testing.T) {
	browser := &countingBrowser{
		schemas: func(_ context.Context, _ adapter.ConnectConfig, _ int) ([]adapter.Schema, error) {
			return nil, adapter.WrapDatabaseError(context.DeadlineExceeded)
		},
	}
	s := newService(t, allowedDeps(browser))
	_, err := s.ListSchemas(context.Background(), principal(), connID())
	if !errors.Is(err, ErrQueryTimeout) {
		t.Fatalf("expected query_timeout, got %v", err)
	}
}

// TestBoundedSentinel 验证 MaxEntries+1 sentinel 的溢出防御：非正数或 int 最大值
// 原样返回，避免 +1 回绕成负值传入 LIMIT。
func TestBoundedSentinel(t *testing.T) {
	if got := boundedSentinel(1000); got != 1001 {
		t.Fatalf("boundedSentinel(1000)=%d, want 1001", got)
	}
	if got := boundedSentinel(0); got != 1 {
		t.Fatalf("boundedSentinel(0)=%d, want 1 (防御性正边界，NewService 会归一化为 1000)", got)
	}
	if got := boundedSentinel(-5); got != -5 {
		t.Fatalf("boundedSentinel(-5)=%d, want -5", got)
	}
	maxInt := int(^uint(0) >> 1)
	if got := boundedSentinel(maxInt); got != maxInt {
		t.Fatalf("boundedSentinel(MaxInt)=%d, want MaxInt（不溢出）", got)
	}
}

// TestValidIdent 表驱动验证标识符白名单（CT-16，P0-06A §7）：空值、空白、分号、
// 注释、引号、点号、连字符、Unicode 及超长均拒绝；合法标识符与恰好 MaxIdentLen
// 的边界值接受。
func TestValidIdent(t *testing.T) {
	long := strings.Repeat("a", MaxIdentLen)
	rejected := []string{
		"", " ", " a", "a ", "a;b", "--", "/*x*/", "'foo'", `"foo"`,
		"a.b", "a-b", "a+b", `a\b`, "表", "名前", "\x00",
		strings.Repeat("a", MaxIdentLen+1),
	}
	for _, tc := range rejected {
		if ValidIdent(tc) {
			t.Errorf("ValidIdent(%q)=true, want false", tc)
		}
	}
	accepted := []string{
		"users", "a", "a_$b", "A1_B2", long,
	}
	for _, tc := range accepted {
		if !ValidIdent(tc) {
			t.Errorf("ValidIdent(%q)=false, want true", tc)
		}
	}
}

// ---- 连接列表 ---------------------------------------------------------------

func TestListConnections_FiltersPolicy(t *testing.T) {
	// 过滤已下沉到 SQL（ListConnectionsAllowed，JOIN policies WHERE allow_read=true）；
	// 服务层只把仓储给的已授权连接转 DTO，并用 limit=MaxConnections+1 保证集合有界。
	c1 := testConn()
	var gotLimit int
	resolver := &countingResolver{}
	browser := &countingBrowser{}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{listAllowed: func(_ context.Context, ws uuid.UUID, limit int) ([]metadata.Connection, error) {
			if ws != wsID() {
				t.Fatalf("queried ws %v, want %v", ws, wsID())
			}
			gotLimit = limit
			return []metadata.Connection{*c1}, nil
		}},
		policies: stubPolicies{},
		resolver: resolver,
		browser:  browser,
	})
	out, err := s.ListConnections(context.Background(), principal())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != c1.ID {
		t.Fatalf("expected only allowed connection, got %+v", out)
	}
	if gotLimit != DefaultLimits().MaxConnections+1 {
		t.Fatalf("limit=%d, want %d", gotLimit, DefaultLimits().MaxConnections+1)
	}
	// 列表不解析凭证、不访问目标库。
	if resolver.calls != 0 || browser.calls != 0 {
		t.Fatalf("resolver=%d browser=%d calls; want 0/0", resolver.calls, browser.calls)
	}
}

func TestListConnections_DTOOnlyPublic(t *testing.T) {
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{list: func(context.Context, uuid.UUID) ([]metadata.Connection, error) {
			return []metadata.Connection{*testConn()}, nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{},
		browser:  &countingBrowser{},
	})
	out, err := s.ListConnections(context.Background(), principal())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d, want 1", len(out))
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, forbidden := range []string{"host", "port", "secret_ref", "secret_version", "created_by", "workspace_id", "created_at", "updated_at", "password"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("DTO leaks %q: %s", forbidden, body)
		}
	}
	for _, allowed := range []string{"id", "name", "engine", "environment", "database"} {
		if !strings.Contains(body, allowed) {
			t.Fatalf("DTO missing %q: %s", allowed, body)
		}
	}
}

func TestListConnections_ResultTooLarge(t *testing.T) {
	over := make([]metadata.Connection, DefaultLimits().MaxConnections+1)
	for i := range over {
		c := testConn()
		c.ID = uuid.New()
		over[i] = *c
	}
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{list: func(context.Context, uuid.UUID) ([]metadata.Connection, error) {
			return over, nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{},
		browser:  &countingBrowser{},
	})
	_, err := s.ListConnections(context.Background(), principal())
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("expected result_too_large, got %v", err)
	}
}

func TestListConnections_NonMember(t *testing.T) {
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			return nil, sql.ErrNoRows
		}},
		conns: stubConns{list: func(context.Context, uuid.UUID) ([]metadata.Connection, error) {
			t.Fatal("ListConnections must not be called for non-member")
			return nil, nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{},
		browser:  &countingBrowser{},
	})
	_, err := s.ListConnections(context.Background(), principal())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden, got %v", err)
	}
}

func TestListConnections_WorkspaceScoped(t *testing.T) {
	// 服务层只按传入 wsID 查询；跨工作区连接天然不返回（防枚举）。
	var queried uuid.UUID
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{list: func(_ context.Context, ws uuid.UUID) ([]metadata.Connection, error) {
			queried = ws
			return nil, nil
		}},
		policies: stubPolicies{byConn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.ConnectionPolicy, error) {
			return readPolicy(true), nil
		}},
		resolver: &countingResolver{},
		browser:  &countingBrowser{},
	})
	if _, err := s.ListConnections(context.Background(), principal()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if queried != wsID() {
		t.Fatalf("ListConnections queried ws %v, want %v", queried, wsID())
	}
}

func TestListConnections_Timeout(t *testing.T) {
	// F6：连接列表元数据库查询超时 → query_timeout。
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{listAllowed: func(context.Context, uuid.UUID, int) ([]metadata.Connection, error) {
			return nil, context.DeadlineExceeded
		}},
		policies: stubPolicies{},
		resolver: &countingResolver{},
		browser:  &countingBrowser{},
	})
	_, err := s.ListConnections(context.Background(), principal())
	if !errors.Is(err, ErrQueryTimeout) {
		t.Fatalf("expected query_timeout, got %v", err)
	}
}

func TestListConnections_Cancelled(t *testing.T) {
	// F6：连接列表元数据库查询取消 → query_cancelled。
	s := newService(t, browseDeps{
		members: stubMembers{fn: func(context.Context, uuid.UUID, uuid.UUID) (*metadata.WorkspaceMember, error) {
			m := readRole()
			return &m, nil
		}},
		conns: stubConns{listAllowed: func(context.Context, uuid.UUID, int) ([]metadata.Connection, error) {
			return nil, context.Canceled
		}},
		policies: stubPolicies{},
		resolver: &countingResolver{},
		browser:  &countingBrowser{},
	})
	_, err := s.ListConnections(context.Background(), principal())
	if !errors.Is(err, ErrQueryCancelled) {
		t.Fatalf("expected query_cancelled, got %v", err)
	}
}
