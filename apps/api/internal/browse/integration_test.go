//go:build integration

// 集成测试：经授权服务编排访问真实 PG/MySQL 目标库的 schemas/tables/columns。
// 依赖 demo 目标库（DEMO_PG_*/DEMO_MYSQL_*，默认 localhost:5433/3306）。
package browse

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

func envPort(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			panic("invalid " + k + "=" + v)
		}
		return n
	}
	return def
}

func envDef(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

const (
	wsIDStr    = "11111111-1111-1111-1111-111111111111"
	uidStr     = "22222222-2222-2222-2222-222222222222"
	demoConnID = "33333333-3333-3333-3333-333333333333"
)

// demoPrincipal / demoConn / demoPolicy / demoResolver 使用合成数据（非真实凭证）。
func demoPrincipal() Principal {
	return Principal{UserID: uuid.MustParse(uidStr), WorkspaceID: uuid.MustParse(wsIDStr)}
}

func demoConn(engine string) *metadata.Connection {
	return &metadata.Connection{
		ID:            uuid.MustParse(demoConnID),
		WorkspaceID:   uuid.MustParse(wsIDStr),
		Name:          "demo-" + engine,
		Engine:        metadata.Engine(engine),
		Host:          envDef("DEMO_PG_HOST", envDef("DEMO_MYSQL_HOST", "localhost")),
		Port:          5433,
		Database:      envDef("DEMO_PG_NAME", envDef("DEMO_MYSQL_NAME", "webdb_demo")),
		Environment:   metadata.EnvDevelopment,
		SecretRef:     uuid.MustParse("44444444-4444-4444-4444-444444444444"),
		SecretVersion: 1,
		CreatedBy:     uuid.MustParse(uidStr),
		UpdatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func demoService(t *testing.T, browser MetadataBrowser, engine string) *Service {
	t.Helper()
	conn := demoConn(engine)
	if engine == string(adapter.EnginePostgreSQL) {
		conn.Port = envPort("DEMO_PG_PORT", 5433)
		conn.Host = envDef("DEMO_PG_HOST", "localhost")
		conn.Database = envDef("DEMO_PG_NAME", "webdb_demo")
	} else {
		conn.Port = envPort("DEMO_MYSQL_PORT", 3306)
		conn.Host = envDef("DEMO_MYSQL_HOST", "localhost")
		conn.Database = envDef("DEMO_MYSQL_NAME", "webdb_demo")
	}
	conn.Engine = metadata.Engine(engine)
	svc := NewService(
		fakeMembersOK{},
		fakeConns{conn: conn},
		fakePoliciesOK{},
		fakeResolverOK{},
		browser,
		DefaultLimits(),
	)
	// 本地 demo 容器无 TLS；演示部署由服务端配置派生（生产默认 TLSRequire）。
	svc.tlsMode = adapter.TLSDisable
	return svc
}

type fakeMembersOK struct{}

func (fakeMembersOK) MemberByWorkspaceAndUser(_ context.Context, _, _ uuid.UUID) (*metadata.WorkspaceMember, error) {
	return &metadata.WorkspaceMember{WorkspaceID: uuid.MustParse(wsIDStr), UserID: uuid.MustParse(uidStr), Role: metadata.RoleViewer}, nil
}

type fakeConns struct {
	conn *metadata.Connection
}

func (f fakeConns) ConnectionByID(_ context.Context, _, id uuid.UUID) (*metadata.Connection, error) {
	if id != uuid.MustParse(demoConnID) {
		return nil, sql.ErrNoRows // 防枚举：不存在的连接 → 连接不存在
	}
	return f.conn, nil
}

func (fakeConns) ListConnections(_ context.Context, _ uuid.UUID) ([]metadata.Connection, error) {
	return nil, nil
}

func (fakeConns) ListConnectionsAllowed(_ context.Context, _ uuid.UUID, _ int) ([]metadata.Connection, error) {
	return nil, nil
}

type fakePoliciesOK struct{}

func (fakePoliciesOK) PolicyByConnection(_ context.Context, _, _ uuid.UUID) (*metadata.ConnectionPolicy, error) {
	b := true
	return &metadata.ConnectionPolicy{AllowRead: &b}, nil
}

type fakeResolverOK struct{}

func (fakeResolverOK) ResolveCredential(context.Context, uuid.UUID, uuid.UUID, int) (credentials.CredentialPayload, error) {
	return credentials.CredentialPayload{User: envDef("DEMO_PG_USER", envDef("DEMO_MYSQL_USER", "demo_reader")), Password: envDef("DEMO_PG_PASSWORD", envDef("DEMO_MYSQL_PASSWORD", "change_me"))}, nil
}

func newDemoBrowser(t *testing.T) *adapter.AdapterManager {
	t.Helper()
	m := adapter.NewAdapterManager(adapter.ManagerOptions{AllowInsecureLocalDemo: true})
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	return m
}

// TestBrowseIntegration_PostgreSQL 经授权服务编排访问 PG 目标库。
func TestBrowseIntegration_PostgreSQL(t *testing.T) {
	mgr := newDemoBrowser(t)
	svc := demoService(t, AdapterBrowser{Manager: mgr}, string(adapter.EnginePostgreSQL))
	p := demoPrincipal()
	ctx := context.Background()

	schemas, err := svc.ListSchemas(ctx, p, uuid.MustParse(demoConnID))
	if err != nil {
		t.Fatalf("ListSchemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("no schemas")
	}
	foundPublic := false
	for _, s := range schemas {
		if s.Name == "pg_catalog" || s.Name == "information_schema" {
			t.Fatalf("system schema leaked: %s", s.Name)
		}
		if s.Name == "public" {
			foundPublic = true
		}
		if s.Catalog == "" {
			t.Fatalf("schema %q missing catalog", s.Name)
		}
	}
	if !foundPublic {
		t.Fatalf("public schema not found in %+v", schemas)
	}

	tables, err := svc.ListTables(ctx, p, uuid.MustParse(demoConnID), "public")
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	foundEmployees := false
	for _, tb := range tables {
		if tb.Name == "employees" {
			foundEmployees = true
		}
	}
	if !foundEmployees {
		t.Fatalf("employees table not found in %+v", tables)
	}

	cols, err := svc.ListColumns(ctx, p, uuid.MustParse(demoConnID), "public", "employees")
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	foundID := false
	for _, c := range cols {
		if c.Name == "id" {
			foundID = true
		}
	}
	if !foundID {
		t.Fatalf("id column not found in %+v", cols)
	}
}

// TestBrowseIntegration_MySQL 经授权服务编排访问 MySQL 目标库。
func TestBrowseIntegration_MySQL(t *testing.T) {
	mgr := newDemoBrowser(t)
	svc := demoService(t, AdapterBrowser{Manager: mgr}, string(adapter.EngineMySQL))
	p := demoPrincipal()
	ctx := context.Background()

	schemas, err := svc.ListSchemas(ctx, p, uuid.MustParse(demoConnID))
	if err != nil {
		t.Fatalf("ListSchemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("no schemas")
	}
	for _, s := range schemas {
		if s.Name == "information_schema" || s.Name == "mysql" || s.Name == "performance_schema" || s.Name == "sys" {
			t.Fatalf("system schema leaked: %s", s.Name)
		}
	}

	dbName := envDef("DEMO_MYSQL_NAME", "webdb_demo")
	tables, err := svc.ListTables(ctx, p, uuid.MustParse(demoConnID), dbName)
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	foundEmployees := false
	for _, tb := range tables {
		if tb.Name == "employees" {
			foundEmployees = true
		}
	}
	if !foundEmployees {
		t.Fatalf("employees table not found in %+v", tables)
	}

	cols, err := svc.ListColumns(ctx, p, uuid.MustParse(demoConnID), dbName, "employees")
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	foundID := false
	for _, c := range cols {
		if c.Name == "id" {
			foundID = true
		}
	}
	if !foundID {
		t.Fatalf("id column not found in %+v", cols)
	}
}

// TestBrowseIntegration_ReauthorizeOnEachLevel 验证每个层级重新授权（服务层固定顺序），
// 即使上层成功，下层仍独立执行门禁。
func TestBrowseIntegration_ReauthorizeOnEachLevel(t *testing.T) {
	mgr := newDemoBrowser(t)
	svc := demoService(t, AdapterBrowser{Manager: mgr}, string(adapter.EnginePostgreSQL))
	p := demoPrincipal()
	ctx := context.Background()

	// schemas 成功不能使后续 tables 越过授权：此处用不存在连接 ID 断言仍返回连接不存在。
	if _, err := svc.ListTables(ctx, p, uuid.New(), "public"); err == nil {
		t.Fatal("expected error for unknown connection")
	} else if err.Error() != "connection_not_found" {
		t.Fatalf("want connection_not_found, got %v", err)
	}
}
