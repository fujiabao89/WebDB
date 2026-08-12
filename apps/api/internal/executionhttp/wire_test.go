package executionhttp

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
)

// TestWireRowLimitCountsEscapedBytes 验证 text/json 单元格的行限按 JSON 编码后
// 长度计（Codex P2）：4×256 KiB `<` 字符未转义长度恰为 1 MiB 会通过，但 JSON
// 编码为 `<` 后约 6 MiB，必须触发 result_too_large（防转义绕过 1 MiB 行限）。
func TestWireRowLimitCountsEscapedBytes(t *testing.T) {
	cell := strings.Repeat("<", 256*1024)
	qr := &adapter.QueryResult{
		Columns: []adapter.ColumnInfo{{Name: "c", DataType: "25"}}, // text
		Rows:    [][]any{{cell, cell, cell, cell}},
	}
	_, err := toWireResult("postgresql", qr)
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("4×256KiB '<' cells 应触发 result_too_large（JSON 转义后 ~6 MiB > 1 MiB 行限），实际 err=%v", err)
	}
}

// TestWireCellUUIDBytes 验证 PG uuid 解码为 [16]byte 时编码为规范 UUID 字符串（Codex P1）。
func TestWireCellUUIDBytes(t *testing.T) {
	u := [16]byte{0xa0, 0xee, 0xbc, 0x99, 0x9c, 0x0b, 0x4e, 0xf8, 0xbb, 0x6d, 0x6b, 0xb9, 0xbd, 0x38, 0x0a, 0x11}
	got, _, err := wireCell("uuid", u)
	if err != nil {
		t.Fatalf("uuid [16]byte: %v", err)
	}
	if got != "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11" {
		t.Fatalf("uuid = %v, want a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", got)
	}
}

// TestWireMySQLTimestampText 验证 MySQL DATETIME/TIMESTAMP 文本规范化为 wire 格式（Codex P2）。
func TestWireMySQLTimestampText(t *testing.T) {
	got, _, err := wireCell("timestamp", []byte("2026-08-09 12:34:56"))
	if err != nil {
		t.Fatalf("timestamp: %v", err)
	}
	if got != "2026-08-09T12:34:56" {
		t.Fatalf("timestamp = %v, want 2026-08-09T12:34:56", got)
	}
}

// TestWireCellCapCountsEscapedBytes 验证 wire 编码后单单元格超过 256 KiB cell 上限
// 返回 result_too_large（Codex P2：150 KiB 换行文本 json.Marshal 编码为 \\n 约 2×，
// adapter copyAndMeasure 按未编码长度放行，wire 层须最终校验）。
func TestWireCellCapCountsEscapedBytes(t *testing.T) {
	cell := strings.Repeat("\n", 150*1024)
	qr := &adapter.QueryResult{
		Columns: []adapter.ColumnInfo{{Name: "c", DataType: "25"}},
		Rows:    [][]any{{cell}},
	}
	_, err := toWireResult("postgresql", qr)
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("150KiB '\\n' 编码后 ~300KiB 应触发 cell 上限 result_too_large，实际 err=%v", err)
	}
}

// TestWireJSONMapOuterEncodedCellLimit 验证 escape-heavy JSONB map（换行/引号/反斜杠）
// 内层+外层 JSON 编码后超过 256 KiB cell 上限返回 result_too_large（Codex：
// 返回字符串在外层 JSON 编码再次转义，须按外层编码后长度计 cell 上限）。
func TestWireJSONMapOuterEncodedCellLimit(t *testing.T) {
	// 内层编码（map→JSON）不超过 MaxCellBytes，外层编码（字符串→JSON）超过——
	// 确保仅外层字符串编码触发 cell 上限（Codex）。
	big := strings.Repeat("\n", 100*1024)
	inner, err := json.Marshal(map[string]interface{}{"data": big})
	if err != nil {
		t.Fatalf("inner marshal: %v", err)
	}
	if len(inner) > MaxCellBytes {
		t.Fatalf("inner encoded len=%d must not exceed MaxCellBytes=%d", len(inner), MaxCellBytes)
	}
	outer, err := json.Marshal(string(inner))
	if err != nil {
		t.Fatalf("outer marshal: %v", err)
	}
	if len(outer) <= MaxCellBytes {
		t.Fatalf("outer encoded len=%d must exceed MaxCellBytes=%d", len(outer), MaxCellBytes)
	}

	qr := &adapter.QueryResult{
		Columns: []adapter.ColumnInfo{{Name: "j", DataType: "3802"}}, // jsonb
		Rows:    [][]any{{map[string]interface{}{"data": big}}},
	}
	_, err = toWireResult("postgresql", qr)
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("escape-heavy JSONB map 外层编码后应触发 cell 上限 result_too_large，实际 err=%v", err)
	}
}

// TestWireJSONMapCellLimit 验证解码 JSONB map 序列化后超过 256 KiB cell 上限返回
// result_too_large（Codex P1：copyAndMeasure 对 map 按固定 fallback 计，未查实际大小）。
func TestWireJSONMapCellLimit(t *testing.T) {
	big := strings.Repeat("x", 300*1024)
	qr := &adapter.QueryResult{
		Columns: []adapter.ColumnInfo{{Name: "j", DataType: "3802"}}, // jsonb
		Rows:    [][]any{{map[string]interface{}{"data": big}}},
	}
	_, err := toWireResult("postgresql", qr)
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("300KiB JSONB map 应触发 result_too_large（>256 KiB cell 上限），实际 err=%v", err)
	}
}

// TestWireCellTypes 验证 wire cell 转换契约（P0-06A §13.1 D08）：
// NULL→null、int/decimal→十进制字符串、bool→boolean、有限 float→number、
// 时间→规范字符串、binary→Base64、json→透传、text/uuid→string。
func TestWireCellTypes(t *testing.T) {
	cases := []struct {
		name     string
		wt       string
		in       any
		wantJSON string
	}{
		{"int null", "int", nil, "null"},
		{"int64", "int", int64(9223372036854775807), `"9223372036854775807"`},
		{"int32", "int", int32(-2147483648), `"-2147483648"`},
		{"decimal string", "decimal", "123.4500", `"123.4500"`},
		{"decimal float", "decimal", 123.45, `"123.45"`},
		{"bool true", "boolean", true, "true"},
		{"bool false", "boolean", false, "false"},
		{"float finite", "float", 3.14, "3.14"},
		{"float whole", "float", 100.0, "100"},
		{"date", "date", time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), `"2024-01-02"`},
		{"time", "time", time.Date(0, 0, 0, 13, 4, 5, 123000, time.UTC), `"13:04:05.000123"`},
		{"timestamp", "timestamp", time.Date(2024, 1, 2, 3, 4, 5, 0, time.FixedZone("X", 8*3600)), `"2024-01-02T03:04:05.000000"`},
		{"timestamptz", "timestamptz", time.Date(2024, 1, 2, 3, 4, 5, 0, time.FixedZone("X", 8*3600)), `"2024-01-01T19:04:05Z"`},
		{"binary", "binary", []byte{0xde, 0xad, 0xbe, 0xef}, `"3q2+7w=="`},
		// json_text 以合法 JSON 字符串返回（Codex P1：浏览器 isWireCell 仅接受 string/number/boolean/null）。
		{"json text", "json_text", `{"a":1}`, `"{\"a\":1}"`},
		{"text", "text", "hello", `"hello"`},
		{"uuid", "uuid", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", `"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := wireCell(c.wt, c.in)
			if err != nil {
				t.Fatalf("wireCell(%q, %v) error: %v", c.wt, c.in, err)
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal %v: %v", got, err)
			}
			if string(b) != c.wantJSON {
				t.Errorf("wireCell(%q) = %s, want %s", c.wt, b, c.wantJSON)
			}
		})
	}
}

// TestWireCellRejectsNonFiniteFloat 验证非有限浮点返回错误（不产生非法 JSON）。
func TestWireCellRejectsNonFiniteFloat(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, _, err := wireCell("float", v); err == nil {
			t.Errorf("wireCell(float, %v) 应返回错误", v)
		}
	}
}

// TestWireCellRejectsUnrepresentable 验证驱动私有类型（非 Stringer 结构等）拒绝。
func TestWireCellRejectsUnrepresentable(t *testing.T) {
	type privateType struct{ x int }
	for _, wt := range []string{"text", "int", "boolean", "float"} {
		if _, _, err := wireCell(wt, privateType{1}); err == nil {
			t.Errorf("wireCell(%q, privateType) 应返回错误", wt)
		}
	}
}

// TestToWireResultRowByteBudget 验证单行超过 1 MiB 返回 result_too_large。
func TestToWireResultRowByteBudget(t *testing.T) {
	big := strings.Repeat("x", 600<<10) // 单 cell 600KiB，两列合计 > 1MiB
	qr := &adapter.QueryResult{
		Columns:      []adapter.ColumnInfo{{Name: "a", DataType: "25"}, {Name: "b", DataType: "25"}},
		Rows:         [][]any{{big, big}},
		ReturnedRows: 1,
	}
	_, err := toWireResult("postgresql", qr)
	if !isCode(err, ErrResultTooLarge) {
		t.Fatalf("err = %v, want result_too_large", err)
	}
}

// TestToWireResultNilResult 验证 nil 结果返回 internal_error。
func TestToWireResultNilResult(t *testing.T) {
	if _, err := toWireResult("postgresql", nil); err == nil {
		t.Fatal("nil 结果应返回错误")
	}
}

// TestToWireResultBinaryBudgetUsesBase64Length 验证 binary 单元格的 1 MiB 行预算
// 按 Base64 编码后长度计算（CodeRabbit #19）。单 binary cell 边界：
//
//	base64(786429B) = 1048572 < 1 MiB → 放行；
//	base64(786432B) = 1048576 = 恰好 1 MiB → 放行；
//	base64(786433B) = 1048580 > 1 MiB → result_too_large（按原始长度则误放行）。
//
// （单 cell 256 KiB 上限由 adapter MaxCellBytes 上游强制；本测试只验证行预算。）
func TestToWireResultBinaryBudgetUsesBase64Length(t *testing.T) {
	cases := []struct {
		name         string
		size         int
		wantTooLarge bool
	}{
		{"under boundary", 786429, false},
		{"exactly 1 MiB", 786432, false},
		{"over boundary", 786433, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			qr := &adapter.QueryResult{
				Columns:      []adapter.ColumnInfo{{Name: "b", DataType: "17"}},
				Rows:         [][]any{{make([]byte, c.size)}},
				ReturnedRows: 1,
			}
			_, err := toWireResult("postgresql", qr)
			if c.wantTooLarge != isCode(err, ErrResultTooLarge) {
				t.Fatalf("size=%d: err=%v, want result_too_large=%v", c.size, err, c.wantTooLarge)
			}
		})
	}
}

// TestWireCellRejectsInvalidJSON 验证 json_text 透传前必须校验合法 JSON：非法/截断
// JSON 返回 database_error，避免产生截断的 200 响应（CodeRabbit #20）；合法 JSON
// 以字符串透传（Codex P1：浏览器 isWireCell 仅接受 string/number/boolean/null）。
func TestWireCellRejectsInvalidJSON(t *testing.T) {
	for _, s := range []string{`{"a":`, `not json`, `{"a":1`} {
		if _, _, err := wireCell("json_text", s); err == nil {
			t.Errorf("wireCell(json_text, %q) 应返回 database_error（非法 JSON 不得透传）", s)
		}
	}
	got, _, err := wireCell("json_text", `{"a":1}`)
	if err != nil {
		t.Fatalf("合法 JSON 应透传: %v", err)
	}
	if s, ok := got.(string); !ok || s != `{"a":1}` {
		t.Fatalf("合法 JSON 透传值 = %#v, want string {\"a\":1}", got)
	}
}

// TestWireTypeForPG 验证 PG OID 派生。
func TestWireTypeForPG(t *testing.T) {
	cases := map[string]WireType{
		"23": WireInt, "20": WireInt, "21": WireInt,
		"1700": WireDecimal, "16": WireBoolean, "700": WireFloat, "701": WireFloat,
		"1082": WireDate, "1083": WireTime, "1114": WireTimestamp, "1184": WireTimestampz,
		"114": WireJSONText, "3802": WireJSONText, "17": WireBinary, "2950": WireUUID,
		"25": WireText, "99999": WireText,
	}
	for oid, want := range cases {
		if got := pgWireType(oid); got != want {
			t.Errorf("pgWireType(%s) = %s, want %s", oid, got, want)
		}
	}
}

// TestWireTypeForMySQL 验证 MySQL DatabaseTypeName 派生。
func TestWireTypeForMySQL(t *testing.T) {
	cases := map[string]WireType{
		"BIGINT": WireInt, "INT": WireInt, "TINYINT": WireInt,
		"DECIMAL": WireDecimal, "DOUBLE": WireFloat, "FLOAT": WireFloat,
		"BOOL": WireBoolean, "DATE": WireDate, "TIME": WireTime,
		"DATETIME": WireTimestamp, "TIMESTAMP": WireTimestamp, "JSON": WireJSONText,
		// BIT 驱动值为 []byte，走 WireBinary（CodeRabbit #18）。
		"BIT": WireBinary, "BLOB": WireBinary, "VARBINARY": WireBinary, "UUID": WireUUID,
		"VARCHAR": WireText, "TEXT": WireText, "UNKNOWN_TYPE": WireText,
	}
	for dt, want := range cases {
		if got := mysqlWireType(dt); got != want {
			t.Errorf("mysqlWireType(%s) = %s, want %s", dt, got, want)
		}
	}
}

// TestMySQLBITGoesBinaryBase64 验证 MySQL BIT 列驱动值为 []byte，正确走
// WireBinary/Base64（CodeRabbit #18），不产生 WireBoolean 的 unrepresentable 错误。
func TestMySQLBITGoesBinaryBase64(t *testing.T) {
	qr := &adapter.QueryResult{
		Columns:      []adapter.ColumnInfo{{Name: "flag", DataType: "BIT"}},
		Rows:         [][]any{{[]byte{0x0f}}},
		ReturnedRows: 1,
	}
	w, err := toWireResult("mysql", qr)
	if err != nil {
		t.Fatalf("BIT 列应成功转换: %v", err)
	}
	if w.Columns[0].WireType != string(WireBinary) {
		t.Fatalf("BIT wire_type = %q, want binary", w.Columns[0].WireType)
	}
	// []byte{0x0f} → base64 "Dw=="。
	if got, ok := w.Rows[0][0].(string); !ok || got != "Dw==" {
		t.Fatalf("BIT 值 = %v, want Base64 \"Dw==\"", w.Rows[0][0])
	}
}

// TestToWireResultRoundTrip 验证完整转换并 JSON 序列化成功。
func TestToWireResultRoundTrip(t *testing.T) {
	qr := &adapter.QueryResult{
		Columns: []adapter.ColumnInfo{
			{Name: "id", DataType: "23"},
			{Name: "name", DataType: "25"},
			{Name: "flag", DataType: "16"},
		},
		Rows:          [][]any{{int64(1), "alice", true}, {int64(2), nil, false}},
		ReturnedRows:  2,
		TotalReturned: 2,
	}
	wr, err := toWireResult("postgresql", qr)
	if err != nil {
		t.Fatalf("toWireResult: %v", err)
	}
	if wr.Columns[0].WireType != "int" || wr.Columns[1].WireType != "text" || wr.Columns[2].WireType != "boolean" {
		t.Errorf("columns = %+v", wr.Columns)
	}
	if wr.ReturnedRows != 2 || wr.TotalReturned != 2 {
		t.Errorf("counts = %d/%d", wr.ReturnedRows, wr.TotalReturned)
	}
	if _, err := json.Marshal(wr); err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	// NULL 保持 null。
	if wr.Rows[1][1] != nil {
		t.Errorf("row[1][1] = %v, want nil", wr.Rows[1][1])
	}
}

// TestDecimalStringInt64 验证 int64 边界精确十进制字符串。
func TestDecimalStringInt64(t *testing.T) {
	for _, v := range []int64{math.MaxInt64, math.MinInt64, 0, -1} {
		s, _, err := decimalString(v)
		if err != nil {
			t.Fatalf("decimalString(%d): %v", v, err)
		}
		if s.(string) != strconv.FormatInt(v, 10) {
			t.Errorf("decimalString(%d) = %v", v, s)
		}
	}
}
