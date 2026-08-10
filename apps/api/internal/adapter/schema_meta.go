package adapter

import (
	"context"
	"database/sql"
	"sort"

	"github.com/fujiabao89/webdb/internal/queryplan"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rowIter 是 pgx.Rows 与 *sql.Rows 的最小公共迭代接口，使行扫描纯函数可单测。
// Close 因两驱动签名不同（pgx.Rows.Close() vs *sql.Rows.Close() error）不在接口内，
// 由 closeRows 用类型断言统一关闭。
type rowIter interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

// closeRows 统一关闭 pgx.Rows（Close()）与 *sql.Rows（Close() error）。
func closeRows(rows rowIter) {
	if c, ok := rows.(interface{ Close() }); ok {
		c.Close()
		return
	}
	if c, ok := rows.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

// queryFn 统一 PG/MySQL 查询执行形态。
type queryFn func(ctx context.Context, query string, args ...any) (rowIter, error)

// metadataSQL 常量：全部使用参数绑定，禁止标识符拼接。
// PostgreSQL 使用 $1/$2；MySQL 使用 ?。
const (
	metaColumnsSQL = `SELECT c.column_name, c.ordinal_position, c.is_nullable
FROM information_schema.columns c
WHERE c.table_schema = ? AND c.table_name = ? ORDER BY c.ordinal_position`

	metaColumnsPG = `SELECT c.column_name, c.ordinal_position, c.is_nullable
FROM information_schema.columns c
WHERE c.table_schema = $1 AND c.table_name = $2 ORDER BY c.ordinal_position`

	metaPKSQL = `SELECT kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name
 AND tc.table_schema = kcu.table_schema
 AND tc.table_name = kcu.table_name
WHERE tc.table_schema = ? AND tc.table_name = ? AND tc.constraint_type = 'PRIMARY KEY'
ORDER BY kcu.ordinal_position`

	// PostgreSQL 主键/唯一约束经 pg_catalog.pg_constraint 读取：系统目录对任意
	// 已认证用户可读（最小权限账号也能看到约束），而 information_schema.
	// table_constraints 对非表属主可能不可见（PG 权限模型），会导致分页证明
	// 在只读账号下恒失败。pg_constraint 天然排除表达式/partial index。
	metaPKPG = `SELECT a.attname
FROM pg_constraint con
JOIN unnest(con.conkey) WITH ORDINALITY AS k(attnum, ordinality) ON true
JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
WHERE con.conrelid = (SELECT c.oid FROM pg_class c
                      JOIN pg_namespace n ON n.oid = c.relnamespace
                      WHERE c.relname = $2 AND n.nspname = $1)
  AND con.contype = 'p'
ORDER BY k.ordinality`

	metaPGUniqueSQL = `SELECT con.conname, a.attname
FROM pg_constraint con
JOIN unnest(con.conkey) WITH ORDINALITY AS k(attnum, ordinality) ON true
JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
WHERE con.conrelid = (SELECT c.oid FROM pg_class c
                      JOIN pg_namespace n ON n.oid = c.relnamespace
                      WHERE c.relname = $2 AND n.nspname = $1)
  AND con.contype = 'u'
ORDER BY con.conname, k.ordinality`

	// MySQL 唯一键经 STATISTICS 取 NON_UNIQUE=0，并排除 PRIMARY。SUB_PART/EXPRESSION
	// 的危险判定在 scanMySQLUniquePairs 中按整个 INDEX_NAME 剔除：若任意 key part 是
	// 前缀（SUB_PART）或表达式/函数（EXPRESSION），整个索引都不作为完整唯一约束，
	// 不得只过滤危险行而把剩余列误当“完整唯一键”（Codex P1-A）。
	metaMySQLUniqueSQL = `SELECT s.INDEX_NAME, s.COLUMN_NAME, s.SUB_PART, s.EXPRESSION
FROM information_schema.STATISTICS s
WHERE s.TABLE_SCHEMA = ? AND s.TABLE_NAME = ? AND s.NON_UNIQUE = 0
  AND s.INDEX_NAME <> 'PRIMARY'
ORDER BY s.INDEX_NAME, s.SEQ_IN_INDEX`

	// metaMySQLUniqueSQLLegacy 兼容 MySQL 8.0.0–8.0.12：STATISTICS 无 EXPRESSION 列
	// （8.0.13 才引入，此前无函数索引），以 NULL AS EXPRESSION 占位，使同一
	// scanMySQLUniquePairs 直接可用（expr 恒为无效 NullString，仅 SUB_PART 判定危险）。
	metaMySQLUniqueSQLLegacy = `SELECT s.INDEX_NAME, s.COLUMN_NAME, s.SUB_PART, NULL AS EXPRESSION
FROM information_schema.STATISTICS s
WHERE s.TABLE_SCHEMA = ? AND s.TABLE_NAME = ? AND s.NON_UNIQUE = 0
  AND s.INDEX_NAME <> 'PRIMARY'
ORDER BY s.INDEX_NAME, s.SEQ_IN_INDEX`
)

func scanColumns(rows rowIter) ([]queryplan.Column, error) {
	var out []queryplan.Column
	for rows.Next() {
		var name, nullable string
		var ord int
		if err := rows.Scan(&name, &ord, &nullable); err != nil {
			return nil, wrapError(ErrDatabaseError, err)
		}
		out = append(out, queryplan.Column{Name: name, Ordinal: ord, Nullable: nullable == "YES"})
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError(ErrDatabaseError, err)
	}
	return out, nil
}

func scanPK(rows rowIter) ([]string, error) {
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, wrapError(ErrDatabaseError, err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError(ErrDatabaseError, err)
	}
	return out, nil
}

// scanUniquePairs 读取 (name, column) 行并分组；保持每约束内列顺序（扫描顺序），
// 同时去重同一 (name, column)（防分区表重复行）。
func scanUniquePairs(rows rowIter) ([]queryplan.UniqueConstraint, error) {
	var out []queryplan.UniqueConstraint
	idx := make(map[string]int)
	for rows.Next() {
		var name, col string
		if err := rows.Scan(&name, &col); err != nil {
			return nil, wrapError(ErrDatabaseError, err)
		}
		i, ok := idx[name]
		if !ok {
			idx[name] = len(out)
			out = append(out, queryplan.UniqueConstraint{Name: name})
			i = len(out) - 1
		}
		cur := out[i].Columns
		dup := false
		for _, c := range cur {
			if c == col {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		out[i].Columns = append(cur, col)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError(ErrDatabaseError, err)
	}
	return out, nil
}

// scanMySQLUniquePairs 读取 MySQL 唯一索引 (name, column, sub_part, expression) 行并按
// INDEX_NAME 分组。若某个 key part 带 SUB_PART（前缀索引）或 EXPRESSION（函数/表达式
// 索引），整个索引必须排除——不能把剩余列重建成“完整唯一约束”（例如 UNIQUE(name(10),
// tenant_id) 若只过滤 name 行会留下 tenant_id 并错误签发 VerifiedSortPlan，导致跨页
// 重复/漏行）。去重同一 (name, column)（防分区表重复行），保持每约束内列顺序。
func scanMySQLUniquePairs(rows rowIter) ([]queryplan.UniqueConstraint, error) {
	type group struct {
		columns   []string
		dangerous bool
	}
	var order []string
	groups := make(map[string]*group)
	for rows.Next() {
		var name string
		var col sql.NullString
		var subPart sql.NullInt64
		var expr sql.NullString
		if err := rows.Scan(&name, &col, &subPart, &expr); err != nil {
			return nil, wrapError(ErrDatabaseError, err)
		}
		g, ok := groups[name]
		if !ok {
			g = &group{}
			groups[name] = g
			order = append(order, name)
		}
		// 前缀（SUB_PART）/表达式/函数（EXPRESSION）key part，或 COLUMN_NAME 为
		// NULL（MySQL 函数索引的隐藏 key part）：整个索引不得作为完整唯一约束。
		if subPart.Valid || expr.Valid || !col.Valid {
			g.dangerous = true
		}
		if g.dangerous {
			continue
		}
		c := col.String
		dup := false
		for _, existing := range g.columns {
			if existing == c {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		g.columns = append(g.columns, c)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError(ErrDatabaseError, err)
	}
	out := make([]queryplan.UniqueConstraint, 0, len(order))
	for _, name := range order {
		g := groups[name]
		if g.dangerous {
			continue
		}
		out = append(out, queryplan.UniqueConstraint{Name: name, Columns: g.columns})
	}
	return out, nil
}

// assembleTableMetadata 汇总三路扫描结果并做 fail-closed 校验。
// 列元数据缺失（表不存在或不可见）时返回错误，保证快照不会半成品。
func assembleTableMetadata(schema, table string, cols []queryplan.Column, pk []string, uniquePairs map[string][]string) (*queryplan.TableMetadata, error) {
	meta := &queryplan.TableMetadata{Schema: schema, Table: table, Columns: cols}
	if len(pk) > 0 {
		meta.PrimaryKey = &queryplan.PrimaryKey{Columns: pk}
	}
	names := make([]string, 0, len(uniquePairs))
	for n := range uniquePairs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		meta.UniqueConstraints = append(meta.UniqueConstraints, queryplan.UniqueConstraint{Name: n, Columns: uniquePairs[n]})
	}
	if err := meta.Validate(); err != nil {
		return nil, newError(ErrUnsupportedQuery, "untrusted table metadata: "+err.Error(), nil)
	}
	return meta, nil
}

// pgQuery 将 pgxpool.Query 收窄为 queryFn（返回值协变）。
func pgQuery(pool *pgxpool.Pool) queryFn {
	return func(ctx context.Context, sql string, args ...any) (rowIter, error) {
		rows, err := pool.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		return rows, nil
	}
}

// myQuery 将 sql.DB.QueryContext 收窄为 queryFn（返回值协变）。
func myQuery(db *sql.DB) queryFn {
	return func(ctx context.Context, sql string, args ...any) (rowIter, error) {
		rows, err := db.QueryContext(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		return rows, nil
	}
}

// runQuery 执行一次带超时的元数据查询并扫描。
func runQuery[A any](ctx context.Context, q queryFn, sql string, args []any, scan func(rowIter) (A, error)) (A, error) {
	var zero A
	qctx, cancel := context.WithTimeout(ctx, connAcquireTimeout)
	defer cancel()
	rows, err := q(qctx, sql, args...)
	if err != nil {
		return zero, mapAcquireError(err)
	}
	defer closeRows(rows)
	v, err := scan(rows)
	if err != nil {
		return zero, err
	}
	return v, nil
}

func loadPGTableMeta(ctx context.Context, pool *pgxpool.Pool, schema, table string) (*queryplan.TableMetadata, error) {
	args := []any{schema, table}
	cols, err := runQuery(ctx, pgQuery(pool), metaColumnsPG, args, scanColumns)
	if err != nil {
		return nil, err
	}
	pk, err := runQuery(ctx, pgQuery(pool), metaPKPG, args, scanPK)
	if err != nil {
		return nil, err
	}
	uqs, err := runQuery(ctx, pgQuery(pool), metaPGUniqueSQL, args, scanUniquePairs)
	if err != nil {
		return nil, err
	}
	umap := make(map[string][]string, len(uqs))
	for _, uq := range uqs {
		umap[uq.Name] = uq.Columns
	}
	return assembleTableMetadata(schema, table, cols, pk, umap)
}

// hasMySQLExpressionColumn 探测 MySQL 8.0.13+ 的 STATISTICS.EXPRESSION 列。
// 8.0.0–8.0.12 无此列（无函数索引），直接引用会报错导致所有分页元数据失败（Codex P1）。
func hasMySQLExpressionColumn(ctx context.Context, db *sql.DB) (bool, error) {
	qctx, cancel := context.WithTimeout(ctx, connAcquireTimeout)
	defer cancel()
	var n int
	if err := db.QueryRowContext(qctx,
		`SELECT COUNT(*) FROM information_schema.COLUMNS
		 WHERE TABLE_SCHEMA = 'information_schema' AND TABLE_NAME = 'STATISTICS' AND COLUMN_NAME = 'EXPRESSION'`,
	).Scan(&n); err != nil {
		return false, mapAcquireError(err)
	}
	return n > 0, nil
}

func loadMySQLTableMeta(ctx context.Context, db *sql.DB, schema, table string) (*queryplan.TableMetadata, error) {
	args := []any{schema, table}
	cols, err := runQuery(ctx, myQuery(db), metaColumnsSQL, args, scanColumns)
	if err != nil {
		return nil, err
	}
	pk, err := runQuery(ctx, myQuery(db), metaPKSQL, args, scanPK)
	if err != nil {
		return nil, err
	}
	// 按服务器能力选择唯一键查询：8.0.13+ 读 EXPRESSION 列；8.0.0–8.0.12
	// 用 NULL AS EXPRESSION 占位（同一 scanMySQLUniquePairs，仅 SUB_PART 判定危险）。
	hasExpr, err := hasMySQLExpressionColumn(ctx, db)
	if err != nil {
		return nil, err
	}
	query := metaMySQLUniqueSQL
	if !hasExpr {
		query = metaMySQLUniqueSQLLegacy
	}
	uqs, err := runQuery(ctx, myQuery(db), query, args, scanMySQLUniquePairs)
	if err != nil {
		return nil, err
	}
	umap := make(map[string][]string, len(uqs))
	for _, uq := range uqs {
		umap[uq.Name] = uq.Columns
	}
	return assembleTableMetadata(schema, table, cols, pk, umap)
}

// LoadTableMetadata 读取目标表的可信元数据（列/PK/唯一约束）。
// 使用最小权限账号、参数绑定、有界超时；结果不进入日志/审计。
func (h *PoolHandle) LoadTableMetadata(ctx context.Context, schema, table string) (*queryplan.TableMetadata, error) {
	if err := h.check(); err != nil {
		return nil, err
	}
	if !validIdent(schema) || !validIdent(table) {
		return nil, newError(ErrInvalidConfig, "invalid schema/table identifier", nil)
	}
	switch h.entry.cfg.Engine {
	case EnginePostgreSQL:
		return loadPGTableMeta(ctx, h.entry.pgPool, schema, table)
	case EngineMySQL:
		return loadMySQLTableMeta(ctx, h.entry.sqlDB, schema, table)
	default:
		return nil, newError(ErrUnsupportedEngine, "", nil)
	}
}
