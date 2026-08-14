package seeddemo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/connections"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- 测试配置 ----------------------------------------------------------------

func newTestConfig() Config {
	return Config{
		WorkspaceID: demoWorkspaceID,
		UserID:      demoUserID,
		Connections: []ConnectionSpec{
			{
				ID:                 demoPGConnID,
				Name:               "demo_reader (PostgreSQL)",
				Engine:             metadata.EnginePostgreSQL,
				Host:               "demo-pg",
				Port:               5432,
				Database:           "webdb_demo",
				Environment:        metadata.EnvDevelopment,
				CredentialUser:     "demo_reader",
				CredentialPassword: "demo_pg_secret",
			},
			{
				ID:                 demoMySQLConnID,
				Name:               "demo_reader (MySQL)",
				Engine:             metadata.EngineMySQL,
				Host:               "demo-mysql",
				Port:               3306,
				Database:           "webdb_demo",
				Environment:        metadata.EnvDevelopment,
				CredentialUser:     "demo_reader",
				CredentialPassword: "demo_mysql_secret",
			},
		},
	}
}

// ---- fake 凭证服务（credentialCreator + credentialResolver + envelopeReader） --

type fakeCredService struct {
	envs        map[string]*metadata.CredentialEnvelope
	payloads    map[string]credentials.CredentialPayload
	createCalls int
}

func newFakeCredService() *fakeCredService {
	return &fakeCredService{
		envs:     map[string]*metadata.CredentialEnvelope{},
		payloads: map[string]credentials.CredentialPayload{},
	}
}

func (c *fakeCredService) Create(_ context.Context, wsID, _ uuid.UUID, payload credentials.CredentialPayload) (*metadata.CredentialEnvelope, error) {
	c.createCalls++
	env := &metadata.CredentialEnvelope{
		WorkspaceID:   wsID,
		SecretRef:     uuid.New(),
		Version:       1,
		EnvelopeSuite: "AES256GCM-v1",
		KEKVersion:    1,
		CreatedAt:     time.Now().UTC(),
	}
	c.envs[env.SecretRef.String()] = env
	c.payloads[env.SecretRef.String()] = payload
	return env, nil
}

func (c *fakeCredService) ResolveCredential(_ context.Context, _ uuid.UUID, secretRef uuid.UUID, _ int) (credentials.CredentialPayload, error) {
	// 模拟 LifecycleManager.Resolve：retired envelope 拒绝解析（与生产语义一致）。
	if env, ok := c.envs[secretRef.String()]; ok && env.RetiredAt != nil {
		return credentials.CredentialPayload{}, errors.New("credential retired")
	}
	p, ok := c.payloads[secretRef.String()]
	if !ok {
		return credentials.CredentialPayload{}, sql.ErrNoRows
	}
	return p, nil
}

func (c *fakeCredService) ListEnvelopes(_ context.Context, wsID uuid.UUID) ([]metadata.CredentialEnvelope, error) {
	var out []metadata.CredentialEnvelope
	for _, e := range c.envs {
		if e.WorkspaceID == wsID {
			out = append(out, *e)
		}
	}
	return out, nil
}

// ---- 综合 fake（identity + connection + policy + 读取） ------------------------

type seedFake struct {
	workspaces map[uuid.UUID]*metadata.Workspace
	users      map[uuid.UUID]*metadata.User
	members    map[string]*metadata.WorkspaceMember
	conns      map[uuid.UUID]*metadata.Connection
	policies   map[uuid.UUID]*metadata.ConnectionPolicy
	envelopes  map[uuid.UUID]*metadata.CredentialEnvelope

	createConnCalls   int
	createPolicyCalls int
	connCreateErr     error

	cred *fakeCredService
}

func newSeedFake() *seedFake {
	return &seedFake{
		workspaces: map[uuid.UUID]*metadata.Workspace{},
		users:      map[uuid.UUID]*metadata.User{},
		members:    map[string]*metadata.WorkspaceMember{},
		conns:      map[uuid.UUID]*metadata.Connection{},
		policies:   map[uuid.UUID]*metadata.ConnectionPolicy{},
		envelopes:  map[uuid.UUID]*metadata.CredentialEnvelope{},
		cred:       newFakeCredService(),
	}
}

func (f *seedFake) fakeDeps() Deps {
	return Deps{
		Identity:    f,
		Credentials: f.cred,
		Connector:   f,
		Policies:    f,
		ConnReader:  f,
		PolicyRead:  f,
		Envelopes:   f.cred,
		Resolver:    f.cred,
	}
}

// identityStore
func (f *seedFake) WorkspaceByID(_ context.Context, id uuid.UUID) (*metadata.Workspace, error) {
	ws, ok := f.workspaces[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return ws, nil
}

func (f *seedFake) createWorkspaceWithID(_ context.Context, ws *metadata.Workspace) error {
	if _, ok := f.workspaces[ws.ID]; ok {
		return nil // DO NOTHING（与生产 ON CONFLICT DO NOTHING 一致）
	}
	now := time.Now().UTC()
	ws.CreatedAt = now
	ws.UpdatedAt = now
	f.workspaces[ws.ID] = ws
	return nil
}

func (f *seedFake) UserByID(_ context.Context, id uuid.UUID) (*metadata.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return u, nil
}

func (f *seedFake) createUserWithID(_ context.Context, u *metadata.User) error {
	if _, ok := f.users[u.ID]; ok {
		return nil // DO NOTHING
	}
	now := time.Now().UTC()
	u.CreatedAt = now
	u.UpdatedAt = now
	f.users[u.ID] = u
	return nil
}

func (f *seedFake) MemberByWorkspaceAndUser(_ context.Context, wsID, userID uuid.UUID) (*metadata.WorkspaceMember, error) {
	m, ok := f.members[memberKey(wsID, userID)]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return m, nil
}

func (f *seedFake) addMemberIfAbsent(_ context.Context, m *metadata.WorkspaceMember) error {
	if _, ok := f.members[memberKey(m.WorkspaceID, m.UserID)]; ok {
		return nil
	}
	m.CreatedAt = time.Now().UTC()
	f.members[memberKey(m.WorkspaceID, m.UserID)] = m
	return nil
}

func (f *seedFake) createEnvelopeWithID(_ context.Context, env *metadata.CredentialEnvelope) error {
	if _, ok := f.envelopes[env.SecretRef]; ok {
		return nil // DO NOTHING
	}
	f.envelopes[env.SecretRef] = env
	return nil
}

func (f *seedFake) createConnectionWithID(_ context.Context, conn *metadata.Connection) error {
	if _, ok := f.conns[conn.ID]; ok {
		return nil // DO NOTHING
	}
	f.conns[conn.ID] = conn
	return nil
}

func (f *seedFake) connectionByID(_ context.Context, wsID, id uuid.UUID) (*metadata.Connection, error) {
	c, ok := f.conns[id]
	if !ok || c.WorkspaceID != wsID {
		return nil, sql.ErrNoRows
	}
	return c, nil
}

func (f *seedFake) envelopeByRef(_ context.Context, wsID, secretRef uuid.UUID, version int) (*metadata.CredentialEnvelope, error) {
	e, ok := f.envelopes[secretRef]
	if !ok || e.WorkspaceID != wsID || e.Version != version {
		return nil, sql.ErrNoRows
	}
	return e, nil
}

// connectionCreator
func (f *seedFake) Create(_ context.Context, p connections.Principal, conn *metadata.Connection) (*metadata.Connection, error) {
	if f.connCreateErr != nil {
		return nil, f.connCreateErr
	}
	f.createConnCalls++
	conn.WorkspaceID = p.WorkspaceID
	conn.CreatedBy = p.UserID
	now := time.Now().UTC()
	conn.CreatedAt = now
	conn.UpdatedAt = now
	f.conns[conn.ID] = conn
	return conn, nil
}

// policyWriter
func (f *seedFake) CreatePolicy(_ context.Context, p *metadata.ConnectionPolicy) error {
	f.createPolicyCalls++
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	f.policies[p.ConnectionID] = p
	return nil
}

// connectionReader
func (f *seedFake) ConnectionByID(_ context.Context, wsID, id uuid.UUID) (*metadata.Connection, error) {
	c, ok := f.conns[id]
	if !ok || c.WorkspaceID != wsID {
		return nil, sql.ErrNoRows
	}
	return c, nil
}

func (f *seedFake) ListConnections(_ context.Context, wsID uuid.UUID) ([]metadata.Connection, error) {
	var out []metadata.Connection
	for _, c := range f.conns {
		if c.WorkspaceID == wsID {
			out = append(out, *c)
		}
	}
	return out, nil
}

// policyReader
func (f *seedFake) PolicyByConnection(_ context.Context, _ uuid.UUID, connID uuid.UUID) (*metadata.ConnectionPolicy, error) {
	p, ok := f.policies[connID]
	if !ok {
		return nil, nil // 缺失策略 → 调用方默认拒绝
	}
	return p, nil
}

func memberKey(wsID, userID uuid.UUID) string { return wsID.String() + ":" + userID.String() }

// ---- helper：预填一致演示数据 -------------------------------------------------

// preseedConsistent 在 fake 中预填与 cfg 完全一致的身份/连接/策略/凭证。
func preseedConsistent(t *testing.T, f *seedFake, cfg Config) {
	t.Helper()
	f.workspaces[cfg.WorkspaceID] = &metadata.Workspace{ID: cfg.WorkspaceID, Name: "Demo Workspace", Settings: json.RawMessage("{}")}
	f.users[cfg.UserID] = &metadata.User{ID: cfg.UserID, Email: "demo@example.local", Status: metadata.UserStatusActive}
	f.members[memberKey(cfg.WorkspaceID, cfg.UserID)] = &metadata.WorkspaceMember{WorkspaceID: cfg.WorkspaceID, UserID: cfg.UserID, Role: metadata.RoleOwner}
	for _, spec := range cfg.Connections {
		env := &metadata.CredentialEnvelope{
			WorkspaceID:   cfg.WorkspaceID,
			SecretRef:     uuid.New(),
			Version:       1,
			EnvelopeSuite: "AES256GCM-v1",
			KEKVersion:    1,
		}
		f.cred.envs[env.SecretRef.String()] = env
		f.cred.payloads[env.SecretRef.String()] = credentials.CredentialPayload{User: spec.CredentialUser, Password: spec.CredentialPassword}
		conn := &metadata.Connection{
			ID: spec.ID, WorkspaceID: cfg.WorkspaceID, Name: spec.Name, Engine: spec.Engine,
			Host: spec.Host, Port: spec.Port, Database: spec.Database, Environment: spec.Environment,
			SecretRef: env.SecretRef, SecretVersion: env.Version, CreatedBy: cfg.UserID,
		}
		f.conns[spec.ID] = conn
		tr := true
		fl := false
		f.policies[spec.ID] = &metadata.ConnectionPolicy{
			WorkspaceID: cfg.WorkspaceID, ConnectionID: spec.ID, AllowRead: &tr,
			AllowWrite: &fl, AllowExport: &fl,
			StatementTimeoutMs: policyStatementTimeoutMs, MaxRows: policyMaxRows,
		}
	}
	preseedForeignFixture(f)
}

// preseedForeignFixture 预置第二合成租户（与 foreign_fixture.go 期望一致），
// 供二次运行 no-op 测试与漂移 fail-closed 测试复用。
func preseedForeignFixture(f *seedFake) {
	fws := mustParseUUID(DemoForeignWorkspaceID)
	fuser := mustParseUUID(DemoForeignUserID)
	fconn := mustParseUUID(DemoForeignConnectionID)
	fsecret := mustParseUUID(demoForeignSecretRef)
	f.workspaces[fws] = &metadata.Workspace{ID: fws, Name: "Foreign Workspace", Settings: json.RawMessage("{}")}
	f.users[fuser] = &metadata.User{ID: fuser, Email: "foreign@example.local", PasswordHash: demoPasswordHash, Status: metadata.UserStatusActive}
	f.members[memberKey(fws, fuser)] = &metadata.WorkspaceMember{WorkspaceID: fws, UserID: fuser, Role: metadata.RoleOwner}
	f.envelopes[fsecret] = &metadata.CredentialEnvelope{WorkspaceID: fws, SecretRef: fsecret, Version: 1, Ciphertext: []byte{0}, DataNonce: []byte{0}, WrappedDEK: []byte{0}, WrapNonce: []byte{0}, EnvelopeSuite: "AES256GCM-v1", KEKVersion: 1}
	f.conns[fconn] = &metadata.Connection{ID: fconn, WorkspaceID: fws, Name: "foreign (PostgreSQL)", Engine: metadata.EnginePostgreSQL, Host: "foreign-demo-pg", Port: 5432, Database: "foreign_db", Environment: metadata.EnvDevelopment, SecretRef: fsecret, SecretVersion: 1, CreatedBy: fuser}
}

// ---- 测试：演示开关门控 --------------------------------------------------------

func TestValidateDemoSwitch(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"缺失", "", true},
		{"false", "false", true},
		{"TRUE 大小写", "TRUE", true},
		{"非法值", "yes", true},
		{"显式 true", "true", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDemoSwitch(tc.value)
			if tc.wantErr && err == nil {
				t.Fatalf("值 %q 应被拒绝", tc.value)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("值 %q 应通过，实际错误: %v", tc.value, err)
			}
		})
	}
}

// ---- 测试：LoadConfig ----------------------------------------------------------

func demoEnv() map[string]string {
	return map[string]string{
		"DEMO_PRINCIPAL_WORKSPACE_ID": DemoWorkspaceID,
		"DEMO_PRINCIPAL_USER_ID":      DemoUserID,
		"DEMO_PG_HOST":                "demo-pg",
		"DEMO_PG_PORT":                "5432",
		"DEMO_PG_NAME":                "webdb_demo",
		"DEMO_PG_READER_PASSWORD":     "demo_pg_secret",
		"DEMO_MYSQL_HOST":             "demo-mysql",
		"DEMO_MYSQL_PORT":             "3306",
		"DEMO_MYSQL_NAME":             "webdb_demo",
		"DEMO_MYSQL_USER":             "demo_reader",
		"DEMO_MYSQL_READER_PASSWORD":  "demo_mysql_secret",
	}
}

func envFromMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfig_fixedIDMismatch(t *testing.T) {
	env := demoEnv()
	env["DEMO_PRINCIPAL_WORKSPACE_ID"] = "00000000-0000-0000-0000-000000000000"
	_, err := LoadConfig(envFromMap(env))
	if err == nil {
		t.Fatal("固定 workspace UUID 与权威来源不一致时应拒绝")
	}
	if !strings.Contains(err.Error(), "DEMO_PRINCIPAL_WORKSPACE_ID") {
		t.Errorf("错误应命名变量 DEMO_PRINCIPAL_WORKSPACE_ID，实际: %v", err)
	}
}

func TestLoadConfig_missingPrincipalEnv(t *testing.T) {
	env := demoEnv()
	delete(env, "DEMO_PRINCIPAL_USER_ID")
	_, err := LoadConfig(envFromMap(env))
	if err == nil {
		t.Fatal("缺失 DEMO_PRINCIPAL_USER_ID 时应拒绝")
	}
	if !strings.Contains(err.Error(), "DEMO_PRINCIPAL_USER_ID") {
		t.Errorf("错误应命名缺失变量，实际: %v", err)
	}
}

// CodeRabbit P0-06A 回归项：MySQL 演示账号必须是固定只读角色，拒绝 root/任意用户。
func TestLoadConfig_mysqlUserMustBeReadOnlyRole(t *testing.T) {
	env := demoEnv()
	env["DEMO_MYSQL_USER"] = "root"
	_, err := LoadConfig(envFromMap(env))
	if err == nil {
		t.Fatal("DEMO_MYSQL_USER=root 时应拒绝（最小权限边界）")
	}
	if !strings.Contains(err.Error(), "DEMO_MYSQL_USER") {
		t.Errorf("错误应命名 DEMO_MYSQL_USER，实际: %v", err)
	}
}

// 默认 demo_reader 与显式 demo_reader 均合法。
func TestLoadConfig_mysqlUserDefaultAndExplicit(t *testing.T) {
	env := demoEnv()
	delete(env, "DEMO_MYSQL_USER") // 缺失 → 默认 demo_reader
	if _, err := LoadConfig(envFromMap(env)); err != nil {
		t.Fatalf("缺失 DEMO_MYSQL_USER 时应默认 demo_reader，实际: %v", err)
	}
	env["DEMO_MYSQL_USER"] = "demo_reader" // 显式合法
	if _, err := LoadConfig(envFromMap(env)); err != nil {
		t.Fatalf("DEMO_MYSQL_USER=demo_reader 应合法，实际: %v", err)
	}
}

func TestLoadConfig_missingSecretsNotLeaked(t *testing.T) {
	env := demoEnv()
	env["DEMO_PG_READER_PASSWORD"] = "" // 显式空
	_, err := LoadConfig(envFromMap(env))
	if err == nil {
		t.Fatal("缺失演示密码时应拒绝")
	}
	if !strings.Contains(err.Error(), "DEMO_PG_READER_PASSWORD") {
		t.Errorf("错误应命名缺失变量 DEMO_PG_READER_PASSWORD，实际: %v", err)
	}
	// 错误信息不得泄露任何演示密码值
	for _, secret := range []string{"demo_pg_secret", "demo_mysql_secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("错误信息不得泄露 secret %q，实际: %v", secret, err)
		}
	}
}

func TestLoadConfig_ok(t *testing.T) {
	cfg, err := LoadConfig(envFromMap(demoEnv()))
	if err != nil {
		t.Fatalf("LoadConfig 不应报错: %v", err)
	}
	if cfg.WorkspaceID != demoWorkspaceID || cfg.UserID != demoUserID {
		t.Fatalf("固定 UUID 解析错误: ws=%s user=%s", cfg.WorkspaceID, cfg.UserID)
	}
	if len(cfg.Connections) != 2 {
		t.Fatalf("应有两个演示连接，实际 %d", len(cfg.Connections))
	}
	if cfg.Connections[0].Engine != metadata.EnginePostgreSQL || cfg.Connections[1].Engine != metadata.EngineMySQL {
		t.Fatalf("连接引擎错误: %+v", cfg.Connections)
	}
}

// ---- 测试：Run（幂等 / 冲突 / 部分失败） --------------------------------------

func TestRun_freshCreates(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	deps := f.fakeDeps()
	cfg := newTestConfig()

	if err := Run(ctx, cfg, deps); err != nil {
		t.Fatalf("首次 seed 不应报错: %v", err)
	}
	if len(f.workspaces) != 2 || len(f.users) != 2 || len(f.members) != 2 {
		t.Fatalf("身份数据数量错误（演示 + foreign 租户）: ws=%d user=%d member=%d", len(f.workspaces), len(f.users), len(f.members))
	}
	if f.users[cfg.UserID].Status != metadata.UserStatusActive {
		t.Fatalf("demo user 必须为 active，实际 %s", f.users[cfg.UserID].Status)
	}
	if f.members[memberKey(cfg.WorkspaceID, cfg.UserID)].Role != metadata.RoleOwner {
		t.Fatalf("demo member 必须为 owner")
	}
	if len(f.conns) != 3 {
		t.Fatalf("应创建两个演示连接 + 一个 foreign 连接，实际 %d", len(f.conns))
	}
	if fc, ok := f.conns[mustParseUUID(DemoForeignConnectionID)]; !ok || fc.WorkspaceID != mustParseUUID(DemoForeignWorkspaceID) {
		t.Fatalf("foreign 连接应存在且属于第二 workspace，实际 %+v", fc)
	}
	if len(f.policies) != 2 {
		t.Fatalf("应创建两个策略，实际 %d", len(f.policies))
	}
	if len(f.cred.envs) != 2 {
		t.Fatalf("应创建两个凭证信封，实际 %d", len(f.cred.envs))
	}
	if f.cred.createCalls != 2 || f.createConnCalls != 2 || f.createPolicyCalls != 2 {
		t.Fatalf("创建调用次数异常: cred=%d conn=%d policy=%d", f.cred.createCalls, f.createConnCalls, f.createPolicyCalls)
	}
	// 连接的 secret_ref 必须指向实际创建的 envelope（演示连接在 f.cred.envs，foreign 在 f.envelopes）
	for _, c := range f.conns {
		if _, ok := f.cred.envs[c.SecretRef.String()]; !ok {
			if _, ok2 := f.envelopes[c.SecretRef]; !ok2 {
				t.Fatalf("连接 %s 引用了不存在的 envelope %s", c.ID, c.SecretRef)
			}
		}
	}
}

func TestRun_secondRunNoop(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	cfg := newTestConfig()
	preseedConsistent(t, f, cfg)
	deps := f.fakeDeps()

	if err := Run(ctx, cfg, deps); err != nil {
		t.Fatalf("二次 seed（一致数据）不应报错: %v", err)
	}
	if f.cred.createCalls != 0 || f.createConnCalls != 0 || f.createPolicyCalls != 0 {
		t.Fatalf("一致数据应为 no-op，实际新增: cred=%d conn=%d policy=%d", f.cred.createCalls, f.createConnCalls, f.createPolicyCalls)
	}
}

func TestRun_foreignFixtureDriftFailsClosed(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig()

	t.Run("connection host 漂移", func(t *testing.T) {
		f := newSeedFake()
		preseedConsistent(t, f, cfg)
		f.conns[mustParseUUID(DemoForeignConnectionID)].Host = "wrong-host"

		if err := Run(ctx, cfg, f.fakeDeps()); !errors.Is(err, ErrDemoSeedRefused) {
			t.Fatalf("foreign connection host 漂移应 fail-closed，got %v", err)
		}
	})

	t.Run("workspace name 漂移", func(t *testing.T) {
		f := newSeedFake()
		preseedConsistent(t, f, cfg)
		f.workspaces[mustParseUUID(DemoForeignWorkspaceID)].Name = "Other Workspace"

		if err := Run(ctx, cfg, f.fakeDeps()); !errors.Is(err, ErrDemoSeedRefused) {
			t.Fatalf("foreign workspace name 漂移应 fail-closed，got %v", err)
		}
	})

	t.Run("envelope suite 漂移", func(t *testing.T) {
		f := newSeedFake()
		preseedConsistent(t, f, cfg)
		f.envelopes[mustParseUUID(demoForeignSecretRef)].EnvelopeSuite = "OTHER-SUITE"

		if err := Run(ctx, cfg, f.fakeDeps()); !errors.Is(err, ErrDemoSeedRefused) {
			t.Fatalf("foreign envelope suite 漂移应 fail-closed，got %v", err)
		}
	})

	t.Run("envelope ciphertext 漂移", func(t *testing.T) {
		f := newSeedFake()
		preseedConsistent(t, f, cfg)
		f.envelopes[mustParseUUID(demoForeignSecretRef)].Ciphertext = []byte{9, 9}

		if err := Run(ctx, cfg, f.fakeDeps()); !errors.Is(err, ErrDemoSeedRefused) {
			t.Fatalf("foreign envelope ciphertext 漂移应 fail-closed，got %v", err)
		}
	})

	t.Run("connection engine 漂移", func(t *testing.T) {
		f := newSeedFake()
		preseedConsistent(t, f, cfg)
		f.conns[mustParseUUID(DemoForeignConnectionID)].Engine = metadata.EngineMySQL

		if err := Run(ctx, cfg, f.fakeDeps()); !errors.Is(err, ErrDemoSeedRefused) {
			t.Fatalf("foreign connection engine 漂移应 fail-closed，got %v", err)
		}
	})

	t.Run("connection secret_version 漂移", func(t *testing.T) {
		f := newSeedFake()
		preseedConsistent(t, f, cfg)
		f.conns[mustParseUUID(DemoForeignConnectionID)].SecretVersion = 99

		if err := Run(ctx, cfg, f.fakeDeps()); !errors.Is(err, ErrDemoSeedRefused) {
			t.Fatalf("foreign connection secret_version 漂移应 fail-closed，got %v", err)
		}
	})
}

func TestRun_workspaceConflict(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	cfg := newTestConfig()
	f.workspaces[cfg.WorkspaceID] = &metadata.Workspace{ID: cfg.WorkspaceID, Name: "Other Workspace", Settings: json.RawMessage("{}")}
	f.users[cfg.UserID] = &metadata.User{ID: cfg.UserID, Email: "demo@example.local", Status: metadata.UserStatusActive}
	f.members[memberKey(cfg.WorkspaceID, cfg.UserID)] = &metadata.WorkspaceMember{WorkspaceID: cfg.WorkspaceID, UserID: cfg.UserID, Role: metadata.RoleOwner}

	err := Run(ctx, cfg, f.fakeDeps())
	if err == nil {
		t.Fatal("固定 workspace ID 已存在但 name 不一致时应 fail-closed")
	}
	if !strings.Contains(err.Error(), cfg.WorkspaceID.String()) {
		t.Errorf("错误应引用冲突的 workspace ID %s，实际: %v", cfg.WorkspaceID, err)
	}
}

func TestRun_connectionConflict(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	cfg := newTestConfig()
	// 预填一致身份，但 PG 连接 host 不一致
	preseedConsistent(t, f, cfg)
	pg := f.conns[cfg.Connections[0].ID]
	pg.Host = "wrong-host"

	err := Run(ctx, cfg, f.fakeDeps())
	if err == nil {
		t.Fatal("固定连接 ID 已存在但字段不一致时应 fail-closed")
	}
	if !strings.Contains(err.Error(), cfg.Connections[0].ID.String()) {
		t.Errorf("错误应引用冲突的连接 ID %s，实际: %v", cfg.Connections[0].ID, err)
	}
}

func TestRun_policyNotReadable(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	cfg := newTestConfig()
	preseedConsistent(t, f, cfg)
	fr := false
	f.policies[cfg.Connections[0].ID] = &metadata.ConnectionPolicy{
		WorkspaceID: cfg.WorkspaceID, ConnectionID: cfg.Connections[0].ID, AllowRead: &fr,
		StatementTimeoutMs: policyStatementTimeoutMs, MaxRows: policyMaxRows,
	}

	err := Run(ctx, cfg, f.fakeDeps())
	if err == nil {
		t.Fatal("已有策略 allow_read=false 时应 fail-closed")
	}
}

// P1（Codex）：策略写入失败遗留"连接已存在但策略缺失"时，二次 seed 应补建而非永久拒绝。
func TestRun_policyMissingRecovered(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	cfg := newTestConfig()
	preseedConsistent(t, f, cfg)
	// 模拟上次 seed 在策略写入前中断：连接/凭证已存在但策略缺失。
	delete(f.policies, cfg.Connections[0].ID)

	if err := Run(ctx, cfg, f.fakeDeps()); err != nil {
		t.Fatalf("策略缺失应作为可恢复阶段补建而非拒绝: %v", err)
	}
	if _, ok := f.policies[cfg.Connections[0].ID]; !ok {
		t.Fatal("缺失策略应被补建")
	}
	if f.createPolicyCalls != 1 {
		t.Fatalf("应补建 1 条策略，实际 %d", f.createPolicyCalls)
	}
}

// P1（Codex 二轮）：matching connection + missing policy + mismatched credential
// → 必须先验证凭证再补建 policy；凭证不匹配时 fail-closed，CreatePolicy 调用为 0。
func TestRun_policyMissingCredentialMismatchNoPolicy(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	cfg := newTestConfig()
	preseedConsistent(t, f, cfg)
	// 模拟上次 seed 在策略写入前中断：连接/凭证已存在但策略缺失。
	delete(f.policies, cfg.Connections[0].ID)
	// 模拟连接引用的凭证与当前合成演示配置不同（如元数据库被其它实例复用/配置变更）。
	spec := cfg.Connections[0]
	conn := f.conns[spec.ID]
	f.cred.payloads[conn.SecretRef.String()] = credentials.CredentialPayload{
		User: "demo_reader", Password: "wrong-demo-secret",
	}

	err := Run(ctx, cfg, f.fakeDeps())
	if !errors.Is(err, ErrDemoSeedRefused) {
		t.Fatalf("凭证不匹配时 seed 应返回 ErrDemoSeedRefused，实际: %v", err)
	}
	if _, ok := f.policies[spec.ID]; ok {
		t.Fatal("凭证不匹配时不得创建 allow_read policy")
	}
	if f.createPolicyCalls != 0 {
		t.Fatalf("凭证不匹配时 CreatePolicy 调用必须为 0，实际 %d", f.createPolicyCalls)
	}
}

// P1（Codex 二轮）：matching connection + missing policy + credential resolve 失败
// （解密/KEK/信封缺失）→ policy 不创建，CreatePolicy 调用为 0。
func TestRun_policyMissingCredentialResolveFailNoPolicy(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	cfg := newTestConfig()
	preseedConsistent(t, f, cfg)
	spec := cfg.Connections[0]
	delete(f.policies, spec.ID)
	// 移除 payload，使 ResolveCredential 返回 ErrNoRows（模拟解密失败/信封缺失）。
	conn := f.conns[spec.ID]
	delete(f.cred.payloads, conn.SecretRef.String())

	err := Run(ctx, cfg, f.fakeDeps())
	if !errors.Is(err, ErrDemoSeedRefused) {
		t.Fatalf("凭证解析失败时 seed 应返回 ErrDemoSeedRefused，实际: %v", err)
	}
	if _, ok := f.policies[spec.ID]; ok {
		t.Fatal("凭证解析失败时不得创建 allow_read policy")
	}
	if f.createPolicyCalls != 0 {
		t.Fatalf("凭证解析失败时 CreatePolicy 调用必须为 0，实际 %d", f.createPolicyCalls)
	}
}

// P1（Codex 二轮）：matching connection + missing policy + retired/orphan credential
// → policy 不创建，CreatePolicy 调用为 0。
func TestRun_policyMissingCredentialRetiredNoPolicy(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	cfg := newTestConfig()
	preseedConsistent(t, f, cfg)
	spec := cfg.Connections[0]
	delete(f.policies, spec.ID)
	// 标记该连接引用的 envelope 为 retired（模拟凭证退役后连接仍引用旧版本）。
	conn := f.conns[spec.ID]
	now := time.Now().UTC()
	f.cred.envs[conn.SecretRef.String()].RetiredAt = &now

	err := Run(ctx, cfg, f.fakeDeps())
	if !errors.Is(err, ErrDemoSeedRefused) {
		t.Fatalf("凭证已退役时 seed 应返回 ErrDemoSeedRefused，实际: %v", err)
	}
	if _, ok := f.policies[spec.ID]; ok {
		t.Fatal("凭证已退役时不得创建 allow_read policy")
	}
	if f.createPolicyCalls != 0 {
		t.Fatalf("凭证已退役时 CreatePolicy 调用必须为 0，实际 %d", f.createPolicyCalls)
	}
}

// P2（Codex）：已存在固定 ID 策略开启 allow_write 时 fail-closed（DML 必须禁用）。
func TestRun_policyWriteEnabledRejected(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	cfg := newTestConfig()
	preseedConsistent(t, f, cfg)
	tr := true
	f.policies[cfg.Connections[0].ID].AllowWrite = &tr

	if err := Run(ctx, cfg, f.fakeDeps()); err == nil {
		t.Fatal("已存在策略 allow_write=true 时应 fail-closed")
	}
}

// P2（Codex）：已存在固定 ID 策略开启 allow_export 时 fail-closed（导出必须禁用）。
func TestRun_policyExportEnabledRejected(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	cfg := newTestConfig()
	preseedConsistent(t, f, cfg)
	tr := true
	f.policies[cfg.Connections[0].ID].AllowExport = &tr

	if err := Run(ctx, cfg, f.fakeDeps()); err == nil {
		t.Fatal("已存在策略 allow_export=true 时应 fail-closed")
	}
}

func TestRun_orphanEnvelopeRejected(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	cfg := newTestConfig()
	// 身份一致、连接缺失；但存在一个未绑定连接的 active envelope（上次 seed 中断遗留）
	f.workspaces[cfg.WorkspaceID] = &metadata.Workspace{ID: cfg.WorkspaceID, Name: "Demo Workspace", Settings: json.RawMessage("{}")}
	f.users[cfg.UserID] = &metadata.User{ID: cfg.UserID, Email: "demo@example.local", Status: metadata.UserStatusActive}
	f.members[memberKey(cfg.WorkspaceID, cfg.UserID)] = &metadata.WorkspaceMember{WorkspaceID: cfg.WorkspaceID, UserID: cfg.UserID, Role: metadata.RoleOwner}
	f.cred.envs["orphan-1"] = &metadata.CredentialEnvelope{
		WorkspaceID: cfg.WorkspaceID, SecretRef: uuid.New(), Version: 1,
		EnvelopeSuite: "AES256GCM-v1", KEKVersion: 1,
	}

	err := Run(ctx, cfg, f.fakeDeps())
	if err == nil {
		t.Fatal("检测到未绑定连接的 active 凭证信封时应 fail-closed 拒绝")
	}
	if f.cred.createCalls != 0 {
		t.Fatalf("拒绝时应不创建新凭证，实际调用 %d", f.cred.createCalls)
	}
	if !strings.Contains(err.Error(), "凭证信封") {
		t.Errorf("错误应描述凭证信封冲突，实际: %v", err)
	}
}

func TestRun_partialFailureLeavesOrphanThenRefuses(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig()

	// 第一次运行：连接创建中途失败（模拟 credential 已创建、connection 绑定失败）
	f := newSeedFake()
	f.connCreateErr = uerr("simulated connection failure")
	err := Run(ctx, cfg, f.fakeDeps())
	if err == nil {
		t.Fatal("连接创建失败时 seed 应返回错误")
	}
	if len(f.cred.envs) == 0 {
		t.Fatal("模拟失败应已创建至少一个凭证信封（反映真实中途失败状态）")
	}

	// 第二次运行：遗留孤立 envelope 被明确拒绝
	f2 := newSeedFake()
	for _, e := range f.cred.envs {
		f2.cred.envs[e.SecretRef.String()] = e
	}
	f2.workspaces[cfg.WorkspaceID] = &metadata.Workspace{ID: cfg.WorkspaceID, Name: "Demo Workspace", Settings: json.RawMessage("{}")}
	f2.users[cfg.UserID] = &metadata.User{ID: cfg.UserID, Email: "demo@example.local", Status: metadata.UserStatusActive}
	f2.members[memberKey(cfg.WorkspaceID, cfg.UserID)] = &metadata.WorkspaceMember{WorkspaceID: cfg.WorkspaceID, UserID: cfg.UserID, Role: metadata.RoleOwner}

	err = Run(ctx, cfg, f2.fakeDeps())
	if err == nil {
		t.Fatal("遗留孤立凭证信封时二次运行应明确拒绝（不可静默恢复）")
	}
}

func TestRun_secretNotLeakedOnError(t *testing.T) {
	ctx := context.Background()
	f := newSeedFake()
	cfg := newTestConfig()
	preseedConsistent(t, f, cfg)
	// 制造连接冲突
	f.conns[cfg.Connections[0].ID].Name = "different name"

	err := Run(ctx, cfg, f.fakeDeps())
	if err == nil {
		t.Fatal("应有错误")
	}
	for _, secret := range []string{"demo_pg_secret", "demo_mysql_secret", "change_me"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("错误信息不得泄露 secret %q，实际: %v", secret, err)
		}
	}
}

// uerr 构造一个简单 error。
type uerr string

func (e uerr) Error() string { return string(e) }
