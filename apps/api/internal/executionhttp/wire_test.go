package executionhttp

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
)

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
		{"json text", "json_text", `{"a":1}`, `{"a":1}`},
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
		"BLOB": WireBinary, "VARBINARY": WireBinary, "UUID": WireUUID,
		"VARCHAR": WireText, "TEXT": WireText, "UNKNOWN_TYPE": WireText,
	}
	for dt, want := range cases {
		if got := mysqlWireType(dt); got != want {
			t.Errorf("mysqlWireType(%s) = %s, want %s", dt, got, want)
		}
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
