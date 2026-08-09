// Package queryplan 定义分页排序的唯一性证明与续页计划。
//
// 本包是中立的底层包：不 import adapter / sqlpolicy / execution，避免依赖环。
// ADR-014/015 要求的 VerifiedSortPlan / VerifiedNextPagePlan 采用 exported
// sealed interface + 私有不可变实现；唯一创建入口是 VerifySortPlan 等验证函数，
// 包外无法构造具体实现，也无法伪造唯一性证明。
package queryplan

import (
	"fmt"
)

// Dialect 方言标识 —— 仅从服务端 Connection.Engine 派生，不接受客户端输入。
type Dialect string

const (
	DialectPostgreSQL Dialect = "postgresql"
	DialectMySQL      Dialect = "mysql"
)

// SortDirection 排序方向。
type SortDirection string

const (
	SortAsc  SortDirection = "ASC"
	SortDesc SortDirection = "DESC"
)

// SortKey 客户端排序意图（ADR-014）。
//
// 注意：SortKey 不携带任何唯一性声明（无 Unique 字段）。唯一性只由
// VerifySortPlan 依据可信 SchemaSnapshot 证明，客户端提交的等价字段一律忽略。
type SortKey struct {
	Column    string
	Direction SortDirection
	NullsLast bool
}

// Column 表列元数据（可信 SchemaSnapshot 的一部分）。
type Column struct {
	Name     string
	Ordinal  int
	Nullable bool
}

// PrimaryKey 主键（按约束列顺序）。
type PrimaryKey struct {
	Columns []string
}

// UniqueConstraint 完整唯一约束（按约束列顺序）。
type UniqueConstraint struct {
	Name    string
	Columns []string
}

// TableMetadata 目标表的可信元数据（SchemaSnapshot 的内容部分）。
// 由 adapter 经最小权限账号从 information_schema 读取，服务层构造。
type TableMetadata struct {
	Schema            string
	Table             string
	Columns           []Column
	PrimaryKey        *PrimaryKey
	UniqueConstraints []UniqueConstraint
}

// validateIdent 校验 SQL 标识符形状（与 adapter 侧一致的保守白名单）。
func validateIdent(s string) error {
	if len(s) == 0 || len(s) > 63 {
		return fmt.Errorf("invalid identifier length")
	}
	for i, r := range s {
		if i == 0 && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
			return fmt.Errorf("invalid identifier start")
		}
		if i > 0 && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return fmt.Errorf("invalid identifier character")
		}
	}
	return nil
}

// Validate 对 TableMetadata 做 fail-closed 校验。
func (m *TableMetadata) Validate() error {
	if m == nil {
		return fmt.Errorf("table metadata is nil")
	}
	if err := validateIdent(m.Schema); err != nil {
		return fmt.Errorf("table schema: %w", err)
	}
	if err := validateIdent(m.Table); err != nil {
		return fmt.Errorf("table name: %w", err)
	}
	if len(m.Columns) == 0 {
		return fmt.Errorf("table metadata has no columns")
	}
	colSet := make(map[string]struct{}, len(m.Columns))
	for _, c := range m.Columns {
		if err := validateIdent(c.Name); err != nil {
			return fmt.Errorf("column %q: %w", c.Name, err)
		}
		if c.Ordinal < 1 {
			return fmt.Errorf("column %q ordinal %d < 1", c.Name, c.Ordinal)
		}
		if _, dup := colSet[c.Name]; dup {
			return fmt.Errorf("duplicate column %q", c.Name)
		}
		colSet[c.Name] = struct{}{}
	}
	if m.PrimaryKey != nil {
		if len(m.PrimaryKey.Columns) == 0 {
			return fmt.Errorf("primary key has no columns")
		}
		for _, name := range m.PrimaryKey.Columns {
			if _, ok := colSet[name]; !ok {
				return fmt.Errorf("primary key references unknown column %q", name)
			}
		}
	}
	for _, uq := range m.UniqueConstraints {
		if uq.Name == "" {
			return fmt.Errorf("unique constraint has empty name")
		}
		if len(uq.Columns) == 0 {
			return fmt.Errorf("unique constraint %q has no columns", uq.Name)
		}
		for _, name := range uq.Columns {
			if _, ok := colSet[name]; !ok {
				return fmt.Errorf("unique constraint %q references unknown column %q", uq.Name, name)
			}
		}
	}
	return nil
}

// isNotNullColumn 返回列是否 NOT NULL。
func isNotNullColumn(cols []Column, name string) bool {
	for _, c := range cols {
		if c.Name == name {
			return !c.Nullable
		}
	}
	return false
}

// constraintColumnsNotNull 返回唯一约束的所有列是否均 NOT NULL。
func constraintColumnsNotNull(meta *TableMetadata, cols []string) bool {
	for _, name := range cols {
		if !isNotNullColumn(meta.Columns, name) {
			return false
		}
	}
	return true
}

// uniqueProof 唯一性证明使用的键（主键或 NOT NULL 唯一约束）。
type uniqueProof struct {
	columns []string
	kind    string // "primary_key" 或 "unique_constraint"
}

// findUniqueProofs 在可信元数据中返回所有能作为全局唯一顺序证明的键：
// 完整主键，以及所有列均 NOT NULL 的完整唯一约束。
// 返回全部而非只取第一个：表可能同时有 PRIMARY KEY(id) 与 UNIQUE(email)，
// 排序键只需完整覆盖其中任意一个证明（ADR-014：完整主键或完整唯一约束任一覆盖即可）。
func findUniqueProofs(meta *TableMetadata) []uniqueProof {
	if meta == nil {
		return nil
	}
	var out []uniqueProof
	if meta.PrimaryKey != nil {
		ok := true
		for _, name := range meta.PrimaryKey.Columns {
			if !isNotNullColumn(meta.Columns, name) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, uniqueProof{columns: append([]string(nil), meta.PrimaryKey.Columns...), kind: "primary_key"})
		}
	}
	for _, uq := range meta.UniqueConstraints {
		if constraintColumnsNotNull(meta, uq.Columns) {
			out = append(out, uniqueProof{columns: append([]string(nil), uq.Columns...), kind: "unique_constraint"})
		}
	}
	return out
}
