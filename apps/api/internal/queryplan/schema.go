package queryplan

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
)

// SchemaSnapshot 绑定 connection/dialect/generation 的可信表元数据（ADR-014）。
//
// 由服务层从 adapter 读取的可信 TableMetadata 构造；queryplan 不连接目标数据库。
// 构造时计算 schema generation（内容哈希）；元数据缺失、过期、模糊或
// generation 不一致时相关验证函数 fail-closed。
type SchemaSnapshot struct {
	ConnectionID     string
	Dialect          Dialect
	PoolGeneration   int64
	SchemaGeneration string        // 表元数据内容哈希（列/PK/唯一约束/lineage）
	table            TableMetadata // 私有，accessor 深拷贝返回
}

// Columns 返回列的深拷贝。
func (s *SchemaSnapshot) Columns() []Column {
	if s == nil {
		return nil
	}
	out := make([]Column, len(s.table.Columns))
	copy(out, s.table.Columns)
	return out
}

// PrimaryKey 返回主键列名的深拷贝（无主键返回 nil）。
func (s *SchemaSnapshot) PrimaryKey() []string {
	if s == nil || s.table.PrimaryKey == nil {
		return nil
	}
	return append([]string(nil), s.table.PrimaryKey.Columns...)
}

// UniqueConstraints 返回唯一约束的深拷贝。
func (s *SchemaSnapshot) UniqueConstraints() []UniqueConstraint {
	if s == nil {
		return nil
	}
	out := make([]UniqueConstraint, len(s.table.UniqueConstraints))
	for i, uq := range s.table.UniqueConstraints {
		out[i] = UniqueConstraint{Name: uq.Name, Columns: append([]string(nil), uq.Columns...)}
	}
	return out
}

// SchemaName 返回表 lineage 的 schema。
func (s *SchemaSnapshot) SchemaName() string {
	if s == nil {
		return ""
	}
	return s.table.Schema
}

// TableName 返回表 lineage 的 table。
func (s *SchemaSnapshot) TableName() string {
	if s == nil {
		return ""
	}
	return s.table.Table
}

// TableMetadataCopy 返回完整元数据的深拷贝（仅供信任的内部调用方使用）。
func (s *SchemaSnapshot) TableMetadataCopy() *TableMetadata {
	if s == nil {
		return nil
	}
	return cloneTableMetadata(&s.table)
}

func cloneTableMetadata(m *TableMetadata) *TableMetadata {
	if m == nil {
		return nil
	}
	cp := *m
	cp.Columns = append([]Column(nil), m.Columns...)
	if m.PrimaryKey != nil {
		cp.PrimaryKey = &PrimaryKey{Columns: append([]string(nil), m.PrimaryKey.Columns...)}
	}
	cp.UniqueConstraints = make([]UniqueConstraint, len(m.UniqueConstraints))
	for i, uq := range m.UniqueConstraints {
		cp.UniqueConstraints[i] = UniqueConstraint{Name: uq.Name, Columns: append([]string(nil), uq.Columns...)}
	}
	return &cp
}

// NewSchemaSnapshot 校验输入、计算 schema generation，构造不可变 SchemaSnapshot。
func NewSchemaSnapshot(connectionID string, dialect Dialect, poolGeneration int64, meta *TableMetadata) (*SchemaSnapshot, error) {
	if connectionID == "" {
		return nil, fmt.Errorf("schema snapshot: empty connection id")
	}
	if dialect != DialectPostgreSQL && dialect != DialectMySQL {
		return nil, fmt.Errorf("schema snapshot: unknown dialect %q", dialect)
	}
	if poolGeneration < 0 {
		return nil, fmt.Errorf("schema snapshot: invalid pool generation %d", poolGeneration)
	}
	if err := meta.Validate(); err != nil {
		return nil, fmt.Errorf("schema snapshot: %w", err)
	}
	gen := computeSchemaGeneration(meta)
	return &SchemaSnapshot{
		ConnectionID:     connectionID,
		Dialect:          dialect,
		PoolGeneration:   poolGeneration,
		SchemaGeneration: gen,
		table:            *cloneTableMetadata(meta),
	}, nil
}

// Validate 对 Snapshot 绑定做 fail-closed 校验。
func (s *SchemaSnapshot) Validate() error {
	if s == nil {
		return fmt.Errorf("schema snapshot is nil")
	}
	if s.ConnectionID == "" {
		return fmt.Errorf("schema snapshot: empty connection id")
	}
	if s.Dialect != DialectPostgreSQL && s.Dialect != DialectMySQL {
		return fmt.Errorf("schema snapshot: unknown dialect %q", s.Dialect)
	}
	if s.PoolGeneration < 0 {
		return fmt.Errorf("schema snapshot: invalid pool generation %d", s.PoolGeneration)
	}
	if s.SchemaGeneration == "" {
		return fmt.Errorf("schema snapshot: empty schema generation")
	}
	if err := s.table.Validate(); err != nil {
		return fmt.Errorf("schema snapshot: %w", err)
	}
	return nil
}

// computeSchemaGeneration 对表元数据做规范编码并计算内容哈希。
// 相同元数据必然得到相同 generation；任一列/PK/唯一约束/lineage 变化则变化。
func computeSchemaGeneration(m *TableMetadata) string {
	h := sha256.New()
	writeStr := func(s string) {
		var lenBuf [8]byte
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(s)))
		h.Write(lenBuf[:])
		h.Write([]byte(s))
	}
	writeStr(m.Schema)
	writeStr(m.Table)
	// 列按 ordinal 排序后写入（防御重复 ordinal）；ordinal 相同列用 name 作确定性次级键，
	// 并把 ordinal 一并写入摘要，使列顺序变化必然影响 generation。
	cols := append([]Column(nil), m.Columns...)
	sort.Slice(cols, func(i, j int) bool {
		if cols[i].Ordinal != cols[j].Ordinal {
			return cols[i].Ordinal < cols[j].Ordinal
		}
		return cols[i].Name < cols[j].Name
	})
	for _, c := range cols {
		writeStr(c.Name)
		var ord [4]byte
		binary.BigEndian.PutUint32(ord[:], uint32(c.Ordinal))
		h.Write(ord[:])
		var flags [1]byte
		if c.Nullable {
			flags[0] = 1
		}
		h.Write(flags[:])
	}
	// 主键
	if m.PrimaryKey != nil {
		h.Write([]byte{1})
		for _, name := range m.PrimaryKey.Columns {
			writeStr(name)
		}
	} else {
		h.Write([]byte{0})
	}
	// 唯一约束按名称排序后写入；同名约束按列集合内容比较，保证排序唯一、哈希确定。
	uqs := append([]UniqueConstraint(nil), m.UniqueConstraints...)
	sort.Slice(uqs, func(i, j int) bool {
		if uqs[i].Name != uqs[j].Name {
			return uqs[i].Name < uqs[j].Name
		}
		return strings.Join(uqs[i].Columns, "\x00") < strings.Join(uqs[j].Columns, "\x00")
	})
	for _, uq := range uqs {
		writeStr(uq.Name)
		for _, name := range uq.Columns {
			writeStr(name)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
