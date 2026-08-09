package adapter

// 元数据浏览查询（Schemas/Tables/Columns）的 Query 错误用 mapExecError 映射：
// 语句执行截止/取消 → query_timeout/query_cancelled（504/499），而非连接获取
// 语义的 connection_busy（429）。QueryContext 在同一调用内完成获取+执行，执行阶段
// 超时无法与获取阶段区分；本路径优先保证慢 catalog 语句超时不被误报为 429
// （WEB-36 P1，Codex 审查）。连接获取阶段的真实截止由此折中为 query_timeout。

import (
	"context"
	"database/sql"
)

func mysqlSchemas(ctx context.Context, db *sql.DB, limit int) ([]Schema, error) {
	q := `SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('information_schema','mysql','performance_schema','sys') ORDER BY schema_name LIMIT ?`
	rows, err := db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, mapExecError(err) // 语句执行截止/取消 → query_timeout/query_cancelled
	}
	defer rows.Close()
	var out []Schema
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, WrapDatabaseError(err)
		}
		out = append(out, Schema{Name: name})
	}
	if err := rows.Err(); err != nil {
		return nil, WrapDatabaseError(err)
	}
	return out, nil
}

func mysqlTables(ctx context.Context, db *sql.DB, schema string, limit int) ([]Table, error) {
	q := `SELECT table_name, table_type FROM information_schema.tables WHERE table_schema=? ORDER BY table_name LIMIT ?`
	rows, err := db.QueryContext(ctx, q, schema, limit)
	if err != nil {
		return nil, mapExecError(err) // 语句执行截止/取消 → query_timeout/query_cancelled
	}
	defer rows.Close()
	var out []Table
	for rows.Next() {
		var name, tt string
		if err := rows.Scan(&name, &tt); err != nil {
			return nil, WrapDatabaseError(err)
		}
		t := TableTypeTable
		if tt == "VIEW" {
			t = TableTypeView
		}
		out = append(out, Table{Schema: schema, Name: name, Type: t})
	}
	if err := rows.Err(); err != nil {
		return nil, WrapDatabaseError(err)
	}
	return out, nil
}

func mysqlColumns(ctx context.Context, db *sql.DB, schema, table string, limit int) ([]Column, error) {
	q := `SELECT column_name, ordinal_position, data_type, is_nullable, column_default IS NOT NULL
FROM information_schema.columns WHERE table_schema=? AND table_name=? ORDER BY ordinal_position LIMIT ?`
	rows, err := db.QueryContext(ctx, q, schema, table, limit)
	if err != nil {
		return nil, mapExecError(err) // 语句执行截止/取消 → query_timeout/query_cancelled
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var name, dt, nullable string
		var ord int
		var hasDef bool
		if err := rows.Scan(&name, &ord, &dt, &nullable, &hasDef); err != nil {
			return nil, WrapDatabaseError(err)
		}
		out = append(out, Column{Name: name, Ordinal: ord, NativeType: dt, Nullable: nullable == "YES", HasDefault: hasDef})
	}
	if err := rows.Err(); err != nil {
		return nil, WrapDatabaseError(err)
	}
	return out, nil
}
