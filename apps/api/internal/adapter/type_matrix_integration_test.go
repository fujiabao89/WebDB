//go:build integration

package adapter

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// 类型矩阵种子表 webdb_type_matrix 由 deploy/compose/init 以管理员身份预置并
// 授予 demo_reader SELECT。测试仅执行只读 SELECT，不要求 demo_reader 有 DDL。
// 数据均为合成数据。

const mysqlTypeMatrixSQL = `SELECT id, c_char, c_varchar, c_text, c_longtext,
	c_binary, c_varbinary, c_blob, c_longblob, c_json, c_null_col,
	c_signed_int, c_unsigned_int, c_bigint, c_decimal, c_float, c_double,
	c_bool, c_bit, c_date, c_datetime, c_timestamp, c_time,
	c_empty_str, c_empty_binary, c_invalid_utf8,
	c_tinytext, c_mediumtext, c_tinyblob, c_mediumblob, c_enum, c_set
FROM webdb_type_matrix`

const pgTypeMatrixSQL = `SELECT id, c_text, c_varchar, c_char, c_bytea, c_jsonb, c_null_col,
	c_smallint, c_int, c_bigint, c_numeric, c_float4, c_float8, c_bool,
	c_date, c_time, c_timestamp, c_timestamptz, c_uuid,
	c_empty_text, c_empty_bytea, c_invalid_utf8
FROM webdb_type_matrix`

func typeMatrixReq(sql string) FirstPageRequest {
	return FirstPageRequest{
		Scope:    UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:      sql,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc, NullsLast: false, Unique: true}},
		PageSize: 100,
		MaxRows:  100,
	}
}

// TestMySQL_TypeMatrixNormalization 覆盖 MySQL 文本→string、二进制→防御性复制 []byte，
// 以及 NULL/整数/decimal/浮点/布尔/bit/时间/空串/零长 binary/非法 UTF-8 的稳定语义。
func TestMySQL_TypeMatrixNormalization(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	defer h.Release()
	requireTypeMatrixTable(t, h, EngineMySQL)

	res := queryMustSucceed(t, h, typeMatrixReq(mysqlTypeMatrixSQL))
	if res.ReturnedRows != 2 {
		t.Fatalf("expected 2 seeded rows, got %d", res.ReturnedRows)
	}
	row1, row2 := res.Rows[0], res.Rows[1]

	// 列索引与 SELECT 顺序一致
	assertCell(t, "id", row1[0], int64(1))

	// 文本列 → string
	assertCell(t, "c_char", row1[1], "ch")
	assertCell(t, "c_varchar", row1[2], "hello")
	assertCell(t, "c_text", row1[3], "text body")
	assertCell(t, "c_longtext", row1[4], "long text body")

	// 二进制列 → []byte（防御性复制）
	assertBytes(t, "c_binary", row1[5], []byte{0x01, 0x02, 0x03, 0x04})
	assertBytes(t, "c_varbinary", row1[6], []byte{0x01, 0x02, 0xFF})
	assertBytes(t, "c_blob", row1[7], []byte{0xDE, 0xAD, 0xBE, 0xEF})
	assertBytes(t, "c_longblob", row1[8], []byte{0x00, 0xFF})

	// JSON → string（文本语义；值归一化后含键 a=1）
	if s, ok := row1[9].(string); !ok {
		t.Errorf("c_json: want string, got %T", row1[9])
	} else {
		var m map[string]int
		if err := json.Unmarshal([]byte(s), &m); err != nil || m["a"] != 1 {
			t.Errorf("c_json: unexpected JSON value %q", s)
		}
	}

	// NULL → nil
	if row1[10] != nil {
		t.Errorf("c_null_col: want nil, got %T %v", row1[10], row1[10])
	}

	// 整数（signed/unsigned）。注意：go-sql-driver 对 INT（fieldTypeLong）无论
	// unsigned 标志都按 strconv.ParseInt → int64；仅 BIGINT UNSIGNED 走 ParseUint
	// → uint64。这里断言 int64 即驱动现有语义（3000000000 < 2^63，无精度损失）。
	assertCell(t, "c_signed_int", row1[11], int64(-42))
	assertCell(t, "c_unsigned_int", row1[12], int64(3000000000))
	assertCell(t, "c_bigint", row1[13], int64(9223372036854775807))

	// decimal → 保持驱动原始 []byte（十进制数值字符串），不被当作文本转 string
	assertBytes(t, "c_decimal", row1[14], []byte("12345.6789"))

	// 浮点
	assertCell(t, "c_float", row1[15], float32(1.5))
	assertCell(t, "c_double", row1[16], float64(2.25))

	// 布尔（MySQL BOOLEAN = TINYINT(1)）→ int64，保持现有语义
	assertCell(t, "c_bool", row1[17], int64(1))

	// bit → []byte，位域非文本，保持字节
	assertBytes(t, "c_bit", row1[18], []byte{0xAA})

	// 时间 → []byte（当前驱动无 parseTime 语义，保持现状）
	assertBytes(t, "c_date", row1[19], []byte("2026-08-09"))
	assertBytes(t, "c_datetime", row1[20], []byte("2026-08-09 12:34:56"))
	assertBytes(t, "c_timestamp", row1[21], []byte("2026-08-09 12:34:56"))
	assertBytes(t, "c_time", row1[22], []byte("12:34:56"))

	// 空字符串 → string ""
	assertCell(t, "c_empty_str", row1[23], "")
	// 零长度 binary → []byte len 0
	assertBytes(t, "c_empty_binary", row1[24], []byte{})
	// 非法 UTF-8 字节 → []byte 原样保留，不转 string
	assertBytes(t, "c_invalid_utf8", row1[25], []byte{0xFF, 0xFE, 0x00})

	// 补充矩阵列：TINYTEXT/MEDIUMTEXT → string；TINYBLOB/MEDIUMBLOB → []byte；ENUM/SET → string
	assertCell(t, "c_tinytext", row1[26], "tiny text")
	assertCell(t, "c_mediumtext", row1[27], "medium text")
	assertBytes(t, "c_tinyblob", row1[28], []byte{0x01, 0x02})
	assertBytes(t, "c_mediumblob", row1[29], []byte{0xAB, 0xCD, 0xEF})
	assertCell(t, "c_enum", row1[30], "green")
	assertCell(t, "c_set", row1[31], "a,c")

	// 防御性复制：第二行读取后第一行的 []byte 不被驱动复用缓冲区覆盖
	assertBytes(t, "row2 c_binary", row2[5], []byte{0x0A, 0x0B, 0x0C, 0x0D})
	assertBytes(t, "row1 c_binary (after row2)", row1[5], []byte{0x01, 0x02, 0x03, 0x04})
	assertBytes(t, "row2 c_blob", row2[7], []byte{0x11, 0x22, 0x33, 0x44})
	assertBytes(t, "row1 c_blob (after row2)", row1[7], []byte{0xDE, 0xAD, 0xBE, 0xEF})
}

// TestPG_TypeMatrixRegression 确认 PostgreSQL 现有类型不被机械转换破坏：
// 文本→string、bytea→[]byte、typed 数值/时间/布尔，NULL→nil，防御性复制。
func TestPG_TypeMatrixRegression(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	defer h.Release()
	requireTypeMatrixTable(t, h, EnginePostgreSQL)

	res := queryMustSucceed(t, h, typeMatrixReq(pgTypeMatrixSQL))
	if res.ReturnedRows != 2 {
		t.Fatalf("expected 2 seeded rows, got %d", res.ReturnedRows)
	}
	row1, row2 := res.Rows[0], res.Rows[1]

	assertCell(t, "id", row1[0], int32(1))

	// 文本列 → string
	assertCell(t, "c_text", row1[1], "text body")
	assertCell(t, "c_varchar", row1[2], "hello")
	assertCell(t, "c_char", row1[3], "ch")

	// bytea → []byte
	assertBytes(t, "c_bytea", row1[4], []byte{0xDE, 0xAD, 0xBE, 0xEF})
	// jsonb → pgx 默认 JSON 解码为 Go 值 map[string]interface{}（现有行为，不被机械转换）
	if m, ok := row1[5].(map[string]interface{}); !ok || m["a"] != float64(1) {
		t.Errorf("c_jsonb: got %T %v, want decoded map with a=1", row1[5], row1[5])
	}
	// NULL → nil
	if row1[6] != nil {
		t.Errorf("c_null_col: want nil, got %T %v", row1[6], row1[6])
	}

	// 整数
	assertCell(t, "c_smallint", row1[7], int16(-42))
	assertCell(t, "c_int", row1[8], int32(123456789))
	assertCell(t, "c_bigint", row1[9], int64(9223372036854775807))

	// numeric → pgtype.Numeric（任意精度十进制，不被降级）
	if n, ok := row1[10].(pgtype.Numeric); !ok {
		t.Errorf("c_numeric: want pgtype.Numeric, got %T", row1[10])
	} else if !n.Valid || n.Int.String() != "123456789" || n.Exp != -4 {
		t.Errorf("c_numeric: unexpected value Int=%v Exp=%d Valid=%v", n.Int, n.Exp, n.Valid)
	}

	// 浮点
	assertCell(t, "c_float4", row1[11], float32(1.5))
	assertCell(t, "c_float8", row1[12], float64(2.25))
	// 布尔
	assertCell(t, "c_bool", row1[13], true)

	// 时间 → time.Time
	date, ok := row1[14].(time.Time)
	if !ok || date.Format("2006-01-02") != "2026-08-09" {
		t.Errorf("c_date: want time.Time 2026-08-09, got %T %v", row1[14], row1[14])
	}
	// TIME → pgtype.Time（微秒数自午夜），不被机械转换
	if tm, ok := row1[15].(pgtype.Time); !ok || !tm.Valid || tm.Microseconds != 45296000000 {
		t.Errorf("c_time: want pgtype.Time 12:34:56, got %T %v", row1[15], row1[15])
	}
	ts, ok := row1[16].(time.Time)
	if !ok || ts.Format("2006-01-02 15:04:05") != "2026-08-09 12:34:56" {
		t.Errorf("c_timestamp: want time.Time 2026-08-09 12:34:56, got %T %v", row1[16], row1[16])
	}
	tstz, ok := row1[17].(time.Time)
	if !ok || !tstz.Equal(time.Date(2026, 8, 9, 12, 34, 56, 0, time.UTC)) {
		t.Errorf("c_timestamptz: want 2026-08-09T12:34:56Z, got %T %v", row1[17], row1[17])
	}
	// uuid → [16]byte
	if u, ok := row1[18].([16]byte); !ok {
		t.Errorf("c_uuid: want [16]byte, got %T", row1[18])
	} else if hex.EncodeToString(u[:]) != "550e8400e29b41d4a716446655440000" {
		t.Errorf("c_uuid: got %x", u[:])
	}

	// 空文本 → string ""；零长 bytea → []byte{}；非法 UTF-8 bytea → 原样
	assertCell(t, "c_empty_text", row1[19], "")
	assertBytes(t, "c_empty_bytea", row1[20], []byte{})
	assertBytes(t, "c_invalid_utf8", row1[21], []byte{0xFF, 0xFE, 0x00})

	// 防御性复制
	assertBytes(t, "row2 c_bytea", row2[4], []byte{0x11, 0x22, 0x33, 0x44})
	assertBytes(t, "row1 c_bytea (after row2)", row1[4], []byte{0xDE, 0xAD, 0xBE, 0xEF})
}

// TestNextPage_MySQL_TextSortKey 验证分页 token 保存的文本排序值（规范化后为 string）
// 与 MySQL keyset 续页兼容：首页 + 续页无重复、无遗漏。
func TestNextPage_MySQL_TextSortKey(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	ensureEmployees(t, h)
	defer h.Release()
	scope := UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"}
	req := FirstPageRequest{
		Scope:    scope,
		SQL:      "SELECT id, first_name FROM employees",
		SortKeys: []SortKey{{Column: "first_name", Order: SortAsc, NullsLast: false, Unique: true}},
		PageSize: 3,
		MaxRows:  100,
	}
	r1 := queryMustSucceed(t, h, req)
	if r1.NextToken == nil {
		t.Fatal("expected next token")
	}
	// 首页 first_name 应为规范化后的 string
	for _, row := range r1.Rows {
		if _, ok := row[1].(string); !ok {
			t.Fatalf("first_name: want string, got %T", row[1])
		}
	}
	r2, err := h.NextPage(context.Background(), scope, *r1.NextToken)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if r2.ReturnedRows == 0 {
		t.Fatal("expected rows in page 2")
	}
	seen := map[string]bool{}
	for _, row := range r1.Rows {
		seen[row[1].(string)] = true
	}
	for _, row := range r2.Rows {
		name, ok := row[1].(string)
		if !ok {
			t.Fatalf("page2 first_name: want string, got %T", row[1])
		}
		if seen[name] {
			t.Fatalf("duplicate first_name %q across pages", name)
		}
		seen[name] = true
	}
	t.Logf("text sort key pagination OK: %d + %d rows", r1.ReturnedRows, r2.ReturnedRows)
}

// requireTypeMatrixTable 是集成环境预检：检查类型矩阵种子表 webdb_type_matrix
// 是否存在。表不存在表示环境未预置（未起 Compose 或未运行 init），这是唯一允许
// 的 Skip 场景；表存在后，查询阶段的任何错误必须由 queryMustSucceed 作为真实
// 失败处理（t.Fatalf），不得标记为 Skip——缺表、权限错误或规范化回归都会让
// 测试失败而非产生绿色 CI。
func requireTypeMatrixTable(t *testing.T, h *PoolHandle, engine Engine) {
	t.Helper()
	var n int64
	switch engine {
	case EngineMySQL:
		err := h.entry.sqlDB.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'webdb_type_matrix'").Scan(&n)
		if err != nil {
			t.Skipf("skip: type matrix preflight failed: %v", err)
		}
	case EnginePostgreSQL:
		err := h.entry.pgPool.QueryRow(context.Background(),
			"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'webdb_type_matrix'").Scan(&n)
		if err != nil {
			t.Skipf("skip: type matrix preflight failed: %v", err)
		}
	default:
		t.Skipf("skip: unsupported engine %v", engine)
	}
	if n == 0 {
		t.Skipf("skip: webdb_type_matrix seed table missing（集成环境未预置）")
	}
}

// queryMustSucceed 在环境预检（mustGet + requireTypeMatrixTable）成功后执行查询。
// 查询错误表示缺表、权限、SQL 包装或本次结果规范化回归，必须以 t.Fatalf 使测试
// 失败；不得用 t.Skipf 隐藏，否则会产生绿色 CI。
func queryMustSucceed(t *testing.T, h *PoolHandle, req FirstPageRequest) *QueryResult {
	t.Helper()
	res, err := h.Query(context.Background(), req)
	if err != nil {
		t.Fatalf("type matrix query failed: %v", err)
	}
	return res
}

// TestTypeMatrix_QueryErrorSurfaced 提供查询阶段错误的聚焦回归证据：环境预检
// 成功后，受控的坏查询必须被 h.Query 返回错误（而非 nil）。该错误由
// queryMustSucceed 以 t.Fatalf 暴露为测试失败，不再被 t.Skipf 隐藏。
func TestTypeMatrix_QueryErrorSurfaced(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	defer h.Release()
	requireTypeMatrixTable(t, h, EngineMySQL)

	_, err := h.Query(context.Background(), typeMatrixReq(
		"SELECT nonexistent_column_xyz FROM webdb_type_matrix"))
	if err == nil {
		t.Fatal("expected query error for nonexistent column, got nil")
	}
}

func assertCell(t *testing.T, name string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %T %v, want %T %v", name, got, got, want, want)
	}
}

func assertBytes(t *testing.T, name string, got any, want []byte) {
	t.Helper()
	b, ok := got.([]byte)
	if !ok {
		t.Errorf("%s: got %T, want []byte", name, got)
		return
	}
	if !bytes.Equal(b, want) {
		t.Errorf("%s: got %x, want %x", name, b, want)
	}
}
