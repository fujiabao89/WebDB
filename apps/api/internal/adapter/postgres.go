package adapter

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

func pgSchemas(ctx context.Context, pool *pgxpool.Pool) ([]Schema, error) {
	q := `SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('pg_catalog','information_schema') ORDER BY schema_name`
	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, wrapError(ErrDatabaseError, err)
	}
	defer rows.Close()
	var out []Schema
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, wrapError(ErrDatabaseError, err)
		}
		out = append(out, Schema{Name: name})
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError(ErrDatabaseError, err)
	}
	return out, nil
}

func pgTables(ctx context.Context, pool *pgxpool.Pool, schema string) ([]Table, error) {
	q := `SELECT table_name, table_type FROM information_schema.tables WHERE table_schema=$1 ORDER BY table_name`
	rows, err := pool.Query(ctx, q, schema)
	if err != nil {
		return nil, wrapError(ErrDatabaseError, err)
	}
	defer rows.Close()
	var out []Table
	for rows.Next() {
		var name, tt string
		if err := rows.Scan(&name, &tt); err != nil {
			return nil, wrapError(ErrDatabaseError, err)
		}
		t := TableTypeTable
		if tt == "VIEW" {
			t = TableTypeView
		}
		out = append(out, Table{Schema: schema, Name: name, Type: t})
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError(ErrDatabaseError, err)
	}
	return out, nil
}

func pgColumns(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]Column, error) {
	q := `SELECT c.column_name, c.ordinal_position, c.data_type, c.is_nullable, c.column_default IS NOT NULL
FROM information_schema.columns c WHERE c.table_schema=$1 AND c.table_name=$2 ORDER BY c.ordinal_position`
	rows, err := pool.Query(ctx, q, schema, table)
	if err != nil {
		return nil, wrapError(ErrDatabaseError, err)
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var name, dt, nullable string
		var ord int
		var hasDef bool
		if err := rows.Scan(&name, &ord, &dt, &nullable, &hasDef); err != nil {
			return nil, wrapError(ErrDatabaseError, err)
		}
		out = append(out, Column{Name: name, Ordinal: ord, NativeType: dt, Nullable: nullable == "YES", HasDefault: hasDef})
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError(ErrDatabaseError, err)
	}
	return out, nil
}
