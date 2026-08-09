package adapter

// 元数据浏览查询（Schemas/Tables/Columns）的 Query 错误用 mapExecError 映射：
// 语句执行截止/取消 → query_timeout/query_cancelled（504/499），而非连接获取
// 语义的 connection_busy（429）。pool.Query 在同一调用内完成获取+执行，执行阶段
// 超时无法与获取阶段区分；本路径优先保证慢 catalog 语句超时不被误报为 429
// （WEB-36 P1，Codex 审查）。连接获取阶段的真实截止由此折中为 query_timeout。

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

func pgSchemas(ctx context.Context, pool *pgxpool.Pool, limit int) ([]Schema, error) {
	q := `SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('pg_catalog','information_schema') ORDER BY schema_name LIMIT $1`
	rows, err := pool.Query(ctx, q, limit)
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

func pgTables(ctx context.Context, pool *pgxpool.Pool, schema string, limit int) ([]Table, error) {
	q := `SELECT table_name, table_type FROM information_schema.tables WHERE table_schema=$1 ORDER BY table_name LIMIT $2`
	rows, err := pool.Query(ctx, q, schema, limit)
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

func pgColumns(ctx context.Context, pool *pgxpool.Pool, schema, table string, limit int) ([]Column, error) {
	q := `SELECT c.column_name, c.ordinal_position, c.data_type, c.is_nullable, c.column_default IS NOT NULL
FROM information_schema.columns c WHERE c.table_schema=$1 AND c.table_name=$2 ORDER BY c.ordinal_position LIMIT $3`
	rows, err := pool.Query(ctx, q, schema, table, limit)
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
