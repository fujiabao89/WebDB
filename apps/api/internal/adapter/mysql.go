package adapter

import (
	"context"
	"database/sql"
)

func mysqlSchemas(ctx context.Context, db *sql.DB) ([]Schema, error) {
	q := `SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('information_schema','mysql','performance_schema','sys') ORDER BY schema_name`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, mapAcquireError(err)
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

func mysqlTables(ctx context.Context, db *sql.DB, schema string) ([]Table, error) {
	q := `SELECT table_name, table_type FROM information_schema.tables WHERE table_schema=? ORDER BY table_name`
	rows, err := db.QueryContext(ctx, q, schema)
	if err != nil {
		return nil, mapAcquireError(err)
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

func mysqlColumns(ctx context.Context, db *sql.DB, schema, table string) ([]Column, error) {
	q := `SELECT column_name, ordinal_position, data_type, is_nullable, column_default IS NOT NULL
FROM information_schema.columns WHERE table_schema=? AND table_name=? ORDER BY ordinal_position`
	rows, err := db.QueryContext(ctx, q, schema, table)
	if err != nil {
		return nil, mapAcquireError(err)
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
