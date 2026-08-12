package executionhttp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/jackc/pgx/v5/pgtype"
)

// WireType 稳定 wire 类型（P0-06A §13.1 D08 已批准；对齐 WEB-37 contracts.ts）。
type WireType string

const (
	WireInt        WireType = "int"
	WireDecimal    WireType = "decimal"
	WireBoolean    WireType = "boolean"
	WireFloat      WireType = "float"
	WireDate       WireType = "date"
	WireTime       WireType = "time"
	WireTimestamp  WireType = "timestamp"
	WireTimestampz WireType = "timestamptz"
	WireJSONText   WireType = "json_text"
	WireBinary     WireType = "binary"
	WireText       WireType = "text"
	WireUUID       WireType = "uuid"
)

// MaxRowBytes 单行字节上限（P0-06A §13.2 D06a：1 MiB，含单元格加总）。
const MaxRowBytes = 1 << 20

// WireColumn 结果列 wire 元数据。
type WireColumn struct {
	Name     string `json:"name"`
	WireType string `json:"wire_type"`
	DataType string `json:"data_type,omitempty"`
}

// WireResult 结果 wire DTO（P0-06A §8.2/§13）。
// rows 中单元格为 JSON 可表示值；不直接暴露 [][]any 或驱动私有类型。
type WireResult struct {
	Columns       []WireColumn `json:"columns"`
	Rows          [][]any      `json:"rows"`
	ReturnedRows  int          `json:"returned_rows"`
	TotalReturned int          `json:"total_returned"`
}

// wireTypeFor 依据方言与列 DataType 派生稳定 wire_type（P0-06A §13.1 D08）。
// PG 的 DataType 为 OID 字符串；MySQL 为 DatabaseTypeName。未知类型 fallback
// 到 text（安全，不泄露原生类型语义）。
func wireTypeFor(engine string, dt string) WireType {
	switch engine {
	case "postgresql":
		return pgWireType(dt)
	case "mysql":
		return mysqlWireType(dt)
	default:
		return WireText
	}
}

// pgWireType 依据 PostgreSQL OID 派生 wire_type。
func pgWireType(oid string) WireType {
	switch oid {
	case "21", "23", "20": // int2/int4/int8
		return WireInt
	case "700", "701": // float4/float8
		return WireFloat
	case "1700": // numeric
		return WireDecimal
	case "16": // bool
		return WireBoolean
	case "1082": // date
		return WireDate
	case "1083", "1266": // time/timetz
		return WireTime
	case "1114": // timestamp
		return WireTimestamp
	case "1184": // timestamptz
		return WireTimestampz
	case "114", "3802": // json/jsonb
		return WireJSONText
	case "17": // bytea
		return WireBinary
	case "2950": // uuid
		return WireUUID
	default: // text/varchar/bpchar 及未知 → text
		return WireText
	}
}

// mysqlWireType 依据 MySQL DatabaseTypeName 派生 wire_type。
func mysqlWireType(dt string) WireType {
	switch dt {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "YEAR":
		return WireInt
	case "FLOAT", "DOUBLE", "REAL":
		return WireFloat
	case "DECIMAL", "NUMERIC", "DEC", "FIXED":
		return WireDecimal
	case "BOOL", "BOOLEAN":
		return WireBoolean
	case "DATE":
		return WireDate
	case "TIME":
		return WireTime
	case "DATETIME", "TIMESTAMP":
		return WireTimestamp
	case "JSON":
		return WireJSONText
	// BIT 驱动值为 []byte（位域字节），走 WireBinary/Base64，而非 WireBoolean
	//（WireBoolean 期望 bool，[]byte 会 errUnrepresentable → database_error，
	// CodeRabbit #18）。BOOL/BOOLEAN/TINYINT 行为不变（BOOL 底层为 TINYINT(1)，驱动报 TINYINT → WireInt）。
	case "BIT", "BINARY", "VARBINARY", "TINYBLOB", "BLOB", "MEDIUMBLOB", "LONGBLOB":
		return WireBinary
	case "UUID":
		return WireUUID
	default: // CHAR/VARCHAR/TEXT 家族/ENUM/SET 及未知 → text
		return WireText
	}
}

// wireCode 返回 wire 转换错误的稳定错误码。
// 确定性结果超限保留 result_too_large（422，Codex P1：与数据库失败可区分）；
// 其余表示性失败折叠为 database_error（脱敏，不泄露值）。
func wireCode(err error) StableErrorCode {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrResultTooLarge) {
		return ErrResultTooLarge
	}
	return ErrDatabaseError
}

// toWireResult 把 adapter 规范化结果转换为公共 wire DTO（P0-06A §8.2/§13）。
// 强制：
//   - cell 上限由 adapter MaxCellBytes 保证（256 KiB）
//   - row 字节上限 1 MiB（含单元格加总），超限 result_too_large
//   - 序列化失败 / 无法表示的值 → 返回错误（调用方不返回半成功响应）
//
// 不直接序列化 [][]any、驱动私有类型或数据库原始错误。
func toWireResult(engine string, qr *adapter.QueryResult) (*WireResult, error) {
	if qr == nil {
		return nil, codef(ErrInternalError, "nil query result")
	}
	cols := make([]WireColumn, len(qr.Columns))
	for i, c := range qr.Columns {
		cols[i] = WireColumn{Name: c.Name, WireType: string(wireTypeFor(engine, c.DataType)), DataType: c.DataType}
	}
	rows := make([][]any, 0, len(qr.Rows))
	for _, src := range qr.Rows {
		dst := make([]any, len(src))
		var rowBytes int
		for i, v := range src {
			cv, cb, err := wireCell(cols[i].WireType, v)
			if err != nil {
				return nil, err
			}
			dst[i] = cv
			rowBytes += cb
			if rowBytes > MaxRowBytes {
				return nil, codef(ErrResultTooLarge, "row byte limit exceeded")
			}
		}
		rows = append(rows, dst)
	}
	return &WireResult{
		Columns:       cols,
		Rows:          rows,
		ReturnedRows:  qr.ReturnedRows,
		TotalReturned: qr.TotalReturned,
	}, nil
}

// wireCell 把单个规范化值转换为 wire 表示，返回 JSON 值与其字节数。
// SQL NULL → nil。无法表示的值返回错误（不静默截断、不产生非法 JSON）。
func wireCell(wt string, v any) (any, int, error) {
	if v == nil {
		return nil, 0, nil
	}
	switch WireType(wt) {
	case WireInt, WireDecimal:
		return decimalString(v)
	case WireBoolean:
		if b, ok := v.(bool); ok {
			return b, 1, nil
		}
		return nil, 0, errUnrepresentable(wt, v)
	case WireFloat:
		f, ok := asFloat(v)
		if !ok {
			return nil, 0, errUnrepresentable(wt, v)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			// 非有限浮点不得产生非法 JSON（P0-06A §13.1）。
			return nil, 0, codef(ErrDatabaseError, "non-finite float value in result")
		}
		return f, 8, nil
	case WireDate, WireTime, WireTimestamp, WireTimestampz:
		return timeString(wt, v)
	case WireBinary:
		switch t := v.(type) {
		case []byte:
			// 字节预算按 Base64 编码后长度计算（CodeRabbit #19）：实际输出是
			// Base64 字符串（约为原始 4/3），按原始长度计会低估响应体字节。
			b := base64.StdEncoding.EncodeToString(t)
			return b, len(b), nil
		case string:
			b := base64.StdEncoding.EncodeToString([]byte(t))
			return b, len(b), nil
		}
		return nil, 0, errUnrepresentable(wt, v)
	case WireJSONText:
		if s, ok := asString(v); ok {
			// 合法 JSON 以字符串返回（Codex P1）：浏览器契约 isWireCell 仅接受
			// string/number/boolean/null，json.RawMessage（对象/数组）会被客户端丢弃；
			// 仍校验 JSON 合法，非法/截断 JSON 在写响应前返回 database_error，
			// 避免透传无效 JSON 产生截断的 200 响应（CodeRabbit #20）。
			// 字节预算按实际 JSON 编码后长度计（Codex P2）：含换行/控制符/`<` 等
			// 转义字符时 len(s) 低估 2-6 倍，会绕过 1 MiB 行限。
			if !json.Valid([]byte(s)) {
				return nil, 0, codef(ErrDatabaseError, "invalid json value in result")
			}
			eb, _ := json.Marshal(s)
			return s, len(eb), nil
		}
		// pgx v5 把 PG jsonb 解码为 map[string]interface{} 等 JSON 值（非 string）；
		// JSON 序列化为字符串透传（浏览器 isWireCell 仅接受 string，Codex P1 #2）。
		// json.Marshal 输出恒为合法 JSON，无需重复 Valid 校验；len(b) 即编码后长度。
		if b, err := json.Marshal(v); err == nil {
			return string(b), len(b), nil
		}
		return nil, 0, errUnrepresentable(wt, v)
	default: // text/uuid
		if s, ok := asString(v); ok {
			// text 单元格同样按 JSON 编码后长度计（Codex P2，防转义绕过行限）。
			eb, _ := json.Marshal(s)
			return s, len(eb), nil
		}
		return nil, 0, errUnrepresentable(wt, v)
	}
}

// decimalString 把整数/decimal 值转为十进制字符串（防精度丢失，D08）。
// 兼容 int 系列、float64（decimal 近似）、string/json.Number（已规范化）、
// 以及带 String() 的驱动类型（pgtype.Numeric 等）。
func decimalString(v any) (any, int, error) {
	switch t := v.(type) {
	case int64:
		s := strconv.FormatInt(t, 10)
		return s, len(s), nil
	case int32:
		s := strconv.FormatInt(int64(t), 10)
		return s, len(s), nil
	case int:
		s := strconv.Itoa(t)
		return s, len(s), nil
	case int16:
		s := strconv.FormatInt(int64(t), 10)
		return s, len(s), nil
	case uint64:
		s := strconv.FormatUint(t, 10)
		return s, len(s), nil
	case []byte:
		// MySQL DECIMAL 等驱动默认返回 []byte（文本字节），按 UTF-8 文本转十进制字符串。
		return string(t), len(t), nil
	case string:
		return t, len(t), nil
	case json.Number:
		return t.String(), len(t.String()), nil
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return nil, 0, codef(ErrDatabaseError, "non-finite numeric value in result")
		}
		s := strconv.FormatFloat(t, 'f', -1, 64)
		return s, len(s), nil
	case pgtype.Numeric:
		// pgx 把 PG numeric 解码为 pgtype.Numeric 结构（不实现 fmt.Stringer），
		// 必须显式转为十进制字符串（D08：防精度丢失），不得把驱动私有类型序列化到响应。
		s, ok := pgNumericString(t)
		if !ok {
			return nil, 0, errUnrepresentable("int/decimal", v)
		}
		return s, len(s), nil
	default:
		if st, ok := v.(fmt.Stringer); ok {
			s := st.String()
			return s, len(s), nil
		}
		return nil, 0, errUnrepresentable("int/decimal", v)
	}
}

// pgNumericString 把 pgx 的 pgtype.Numeric 结构转为十进制字符串（D08 十进制字符串语义）。
// 处理 NaN / Infinity / 整数指数 / 负指数（插入小数点）；非法（Invalid 或空系数）返回 false。
func pgNumericString(n pgtype.Numeric) (string, bool) {
	if !n.Valid {
		return "", false
	}
	if n.NaN {
		return "NaN", true
	}
	switch n.InfinityModifier {
	case pgtype.NegativeInfinity:
		return "-Infinity", true
	case pgtype.Infinity:
		return "Infinity", true
	}
	if n.Int == nil {
		return "", false
	}
	if n.Exp == 0 {
		return n.Int.String(), true
	}
	s := n.Int.String()
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	switch {
	case n.Exp > 0:
		// 整数部分后补零（Int × 10^Exp）
		s += strings.Repeat("0", int(n.Exp))
	default: // n.Exp < 0：插入小数点
		exp := -int(n.Exp)
		if exp >= len(s) {
			s = "0." + strings.Repeat("0", exp-len(s)) + s
		} else {
			idx := len(s) - exp
			s = s[:idx] + "." + s[idx:]
		}
	}
	if neg {
		s = "-" + s
	}
	return s, true
}

// asFloat 把值转为 float64。
func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}

// timeString 把时间值转为规范字符串（P0-06A §13.1 D08）：
// date YYYY-MM-DD；time HH:MM:SS[.ffffff]；timestamp 无时区后缀；timestamptz RFC3339 UTC。
// 兼容 time.Time / string / []byte（MySQL 未 parseTime 时返回 []byte）/ 驱动类型。
func timeString(wt string, v any) (any, int, error) {
	switch t := v.(type) {
	case time.Time:
		return formatTime(wt, t), 32, nil
	case string:
		return t, len(t), nil
	case []byte:
		return string(t), len(t), nil
	default:
		if st, ok := v.(fmt.Stringer); ok {
			s := st.String()
			return s, len(s), nil
		}
		return nil, 0, errUnrepresentable(wt, v)
	}
}

// formatTime 按 wire 类型格式化 time.Time（保留原语义，不 UTC 归一化，
// date/time/timestamp 无时区；timestamptz 输出 RFC3339 UTC）。
func formatTime(wt string, t time.Time) string {
	switch WireType(wt) {
	case WireDate:
		return t.Format("2006-01-02")
	case WireTime:
		return t.Format("15:04:05.000000")
	case WireTimestamp:
		return t.Format("2006-01-02T15:04:05.000000")
	case WireTimestampz:
		return t.UTC().Format(time.RFC3339Nano)
	default:
		return t.Format(time.RFC3339Nano)
	}
}

// asString 把值转为 string（[]byte 视为 UTF-8 文本）。
func asString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case []byte:
		return string(t), true
	}
	return "", false
}

// errUnrepresentable 构造不可表示值错误（驱动私有类型等）。
func errUnrepresentable(wt string, v any) error {
	return codef(ErrDatabaseError, "unrepresentable value of type %T for wire type %s", v, wt)
}
