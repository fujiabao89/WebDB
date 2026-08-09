//go:build integration

package adapter

import (
	"context"
	"testing"
)

// TestMetadataBrowseSentinelLimit 在真实 PostgreSQL/MySQL catalog 上运行时确认
// 元数据浏览按 sentinel 上限停止读取：limit 小于 catalog 实际大小时，查询层
// `LIMIT ?/$n` 使目标库只返回 limit 行，而非先累积完整 catalog 再拒绝
// （WEB-36 P1：数据库端有界，不得无界取回）。
func TestMetadataBrowseSentinelLimit(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())

	for _, tc := range []struct {
		name string
		cfg  ConnectConfig
	}{
		{"postgresql", pgCfg()},
		{"mysql", myCfg()},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h := mustGet(t, m, tc.cfg)
			ensureEmployees(t, h)
			defer h.Release()
			scope := UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"}
			// PG 表默认在 public；MySQL 表在连接数据库。
			schema := tc.cfg.Database
			if h.entry.pgPool != nil {
				schema = "public"
			}

			// 每类元数据 catalog 大小都 > 1（PG/MySQL 至少含演示库/表、employees 有两列），
			// 因此 limit=1 若数据库未在 sentinel 停止会返回 >1 行。
			schemas, err := h.Schemas(context.Background(), scope, 1)
			if err != nil {
				t.Fatalf("Schemas(limit=1): %v", err)
			}
			if len(schemas) != 1 {
				t.Fatalf("Schemas(limit=1) = %d rows, want exactly 1 (sentinel must stop scan)", len(schemas))
			}

			tables, err := h.Tables(context.Background(), scope, schema, 1)
			if err != nil {
				t.Fatalf("Tables(%q, limit=1): %v", schema, err)
			}
			if len(tables) != 1 {
				t.Fatalf("Tables(limit=1) = %d rows, want exactly 1", len(tables))
			}

			cols, err := h.Columns(context.Background(), scope, schema, "employees", 1)
			if err != nil {
				t.Fatalf("Columns(%q, employees, limit=1): %v", schema, err)
			}
			if len(cols) != 1 {
				t.Fatalf("Columns(limit=1) = %d rows, want exactly 1", len(cols))
			}
		})
	}
}
