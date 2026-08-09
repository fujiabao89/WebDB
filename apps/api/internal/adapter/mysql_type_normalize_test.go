package adapter

import "testing"

// TestMySQLTextColumnClassification 验证文本/二进制列判定依据驱动
// DatabaseTypeName()（基于 MySQL 协议字段类型 + 字符集），而不是运行时值。
// 文本列：扫描出的 []byte 可安全转换为 string。
// 二进制/其他/未知列：保持 []byte 防御性复制，绝不静默转 UTF-8。
func TestMySQLTextColumnClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		dt   string
		want bool
	}{
		// 文本列：CHAR/VARCHAR/TEXT 家族、ENUM/SET、JSON → string
		{"char", "CHAR", true},
		{"varchar", "VARCHAR", true},
		{"text", "TEXT", true},
		{"tinytext", "TINYTEXT", true},
		{"mediumtext", "MEDIUMTEXT", true},
		{"longtext", "LONGTEXT", true},
		{"enum", "ENUM", true},
		{"set", "SET", true},
		{"json", "JSON", true},
		// 二进制列：BINARY/VARBINARY/BLOB 家族 → 保持 []byte
		{"binary", "BINARY", false},
		{"varbinary", "VARBINARY", false},
		{"tinyblob", "TINYBLOB", false},
		{"blob", "BLOB", false},
		{"mediumblob", "MEDIUMBLOB", false},
		{"longblob", "LONGBLOB", false},
		// BIT 是位域不是文本，字节不代表 UTF-8 → 保持 []byte
		{"bit", "BIT", false},
		// 空间/向量类型是二进制 WKB → 保持 []byte
		{"geometry", "GEOMETRY", false},
		{"vector", "VECTOR", false},
		// 数值/时间类型不由本函数归为文本：保持驱动原始返回值
		{"decimal", "DECIMAL", false},
		{"date", "DATE", false},
		{"datetime", "DATETIME", false},
		{"timestamp", "TIMESTAMP", false},
		{"time", "TIME", false},
		// 驱动 v1.10.0 fields.go typeDatabaseName() 实际返回的整数/年/空类型变体 → 非文本
		{"unsigned-tinyint", "UNSIGNED TINYINT", false},
		{"unsigned-smallint", "UNSIGNED SMALLINT", false},
		{"unsigned-mediumint", "UNSIGNED MEDIUMINT", false},
		{"unsigned-int", "UNSIGNED INT", false},
		{"unsigned-bigint", "UNSIGNED BIGINT", false},
		{"tinyint", "TINYINT", false},
		{"year", "YEAR", false},
		{"null-type", "NULL", false},
		// 未知类型 fail-closed：不转 string（无法判断时不静默转 UTF-8）
		{"unknown-empty", "", false},
		{"unknown-name", "WEIRD_FAKE_TYPE", false},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := isMySQLTextColumn(c.dt); got != c.want {
				t.Fatalf("isMySQLTextColumn(%q) = %v, want %v", c.dt, got, c.want)
			}
		})
	}
}
