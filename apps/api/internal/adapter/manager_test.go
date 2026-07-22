//go:build integration

package adapter

import (
	"context"
	"os"
	"testing"
)

func envDef(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func pgCfg() ConnectConfig {
	return ConnectConfig{
		ConnectionID: "pg1", SecretVersion: 1, ConfigRevision: 1,
		Engine: EnginePostgreSQL, Host: envDef("DEMO_PG_HOST", "localhost"), Port: 5433,
		User: envDef("DEMO_PG_USER", "demo_reader"), Password: envDef("DEMO_PG_PASSWORD", "change_me"),
		Database: envDef("DEMO_PG_NAME", "webdb_demo"), TLS: TLSDisable, MaxOpen: 2, MaxIdle: 1,
	}
}
func myCfg() ConnectConfig {
	return ConnectConfig{
		ConnectionID: "my1", SecretVersion: 1, ConfigRevision: 1,
		Engine: EngineMySQL, Host: envDef("DEMO_MYSQL_HOST", "localhost"), Port: 3306,
		User: envDef("DEMO_MYSQL_USER", "demo_reader"), Password: envDef("DEMO_MYSQL_PASSWORD", "change_me"),
		Database: envDef("DEMO_MYSQL_NAME", "webdb_demo"), TLS: TLSDisable, MaxOpen: 2, MaxIdle: 1,
	}
}

func TestManager_NewAdapterManager(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	if m == nil {
		t.Fatal("nil")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestManager_Get_PostgreSQL(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h, err := m.Get(context.Background(), pgCfg())
	if err != nil {
		t.Fatalf("Get PG: %v", err)
	}
	h.Release()
}
func TestManager_Get_MySQL(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h, err := m.Get(context.Background(), myCfg())
	if err != nil {
		t.Fatalf("Get MySQL: %v", err)
	}
	h.Release()
}
func TestManager_UnsupportedEngine(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	c := pgCfg()
	c.Engine = "sqlite"
	if _, err := m.Get(context.Background(), c); err == nil {
		t.Fatal("expected error")
	}
}
func TestManager_TLS_Disable_Denied(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: false})
	defer m.Close(context.Background())
	c := pgCfg()
	c.TLS = TLSDisable
	if _, err := m.Get(context.Background(), c); err == nil {
		t.Fatal("expected error")
	}
}
func TestManager_StaleConfig(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	c := pgCfg()
	c.ConfigRevision = 5
	h, err := m.Get(context.Background(), c)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	h.Release()
	c.ConfigRevision = 3
	if _, err = m.Get(context.Background(), c); err == nil {
		t.Fatal("expected stale error")
	}
}

func TestSchemas_PG(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h, err := m.Get(context.Background(), pgCfg())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer h.Release()
	schemas, err := h.Schemas(context.Background())
	if err != nil {
		t.Fatalf("Schemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("expected at least 1 schema")
	}
	t.Logf("PG schemas: %v", schemas)
}

func TestSchemas_MySQL(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h, err := m.Get(context.Background(), myCfg())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer h.Release()
	schemas, err := h.Schemas(context.Background())
	if err != nil {
		t.Fatalf("Schemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("expected at least 1 schema")
	}
	t.Logf("MySQL schemas: %v", schemas)
}

func TestTables_PG(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h, _ := m.Get(context.Background(), pgCfg())
	defer h.Release()
	tables, err := h.Tables(context.Background(), "public")
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("expected tables in public schema")
	}
	t.Logf("PG tables: %d", len(tables))
}

func TestTables_MySQL(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h, _ := m.Get(context.Background(), myCfg())
	defer h.Release()
	tables, err := h.Tables(context.Background(), "webdb_demo")
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("expected tables")
	}
	t.Logf("MySQL tables: %d", len(tables))
}
