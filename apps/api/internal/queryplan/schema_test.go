package queryplan

import (
	"strings"
	"testing"
)

func validTableMeta() *TableMetadata {
	return &TableMetadata{
		Schema: "public",
		Table:  "users",
		Columns: []Column{
			{Name: "id", Ordinal: 1, Nullable: false},
			{Name: "tenant_id", Ordinal: 2, Nullable: false},
			{Name: "email", Ordinal: 3, Nullable: true},
		},
		PrimaryKey: &PrimaryKey{Columns: []string{"id"}},
		UniqueConstraints: []UniqueConstraint{
			{Name: "uq_tenant_email", Columns: []string{"tenant_id", "email"}},
		},
	}
}

func TestNewSchemaSnapshotValid(t *testing.T) {
	t.Parallel()
	s, err := NewSchemaSnapshot("conn-1", DialectPostgreSQL, 7, validTableMeta())
	if err != nil {
		t.Fatalf("NewSchemaSnapshot() error = %v", err)
	}
	if s == nil {
		t.Fatal("NewSchemaSnapshot() = nil")
	}
	if s.ConnectionID != "conn-1" || s.Dialect != DialectPostgreSQL || s.PoolGeneration != 7 {
		t.Fatalf("binding mismatch: %+v", s)
	}
	if s.SchemaGeneration == "" {
		t.Fatal("SchemaGeneration empty")
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestNewSchemaSnapshotRejectsInvalidBinding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*TableMetadata)
		connID     string
		dialect    Dialect
		poolGen    int64
		wantSubstr string
	}{
		{
			name:       "empty connection id",
			connID:     "",
			dialect:    DialectPostgreSQL,
			poolGen:    1,
			wantSubstr: "connection",
		},
		{
			name:       "unknown dialect",
			connID:     "c1",
			dialect:    Dialect("oracle"),
			poolGen:    1,
			wantSubstr: "dialect",
		},
		{
			name:       "negative pool generation",
			connID:     "c1",
			dialect:    DialectPostgreSQL,
			poolGen:    -1,
			wantSubstr: "generation",
		},
		{
			name:       "empty table schema",
			connID:     "c1",
			dialect:    DialectPostgreSQL,
			poolGen:    1,
			mutate:     func(m *TableMetadata) { m.Schema = "" },
			wantSubstr: "schema",
		},
		{
			name:       "empty table name",
			connID:     "c1",
			dialect:    DialectPostgreSQL,
			poolGen:    1,
			mutate:     func(m *TableMetadata) { m.Table = "" },
			wantSubstr: "table",
		},
		{
			name:       "no columns",
			connID:     "c1",
			dialect:    DialectPostgreSQL,
			poolGen:    1,
			mutate:     func(m *TableMetadata) { m.Columns = nil },
			wantSubstr: "columns",
		},
		{
			name:       "duplicate column names",
			connID:     "c1",
			dialect:    DialectPostgreSQL,
			poolGen:    1,
			mutate:     func(m *TableMetadata) { m.Columns[1].Name = m.Columns[0].Name },
			wantSubstr: "duplicate",
		},
		{
			name:       "empty column name",
			connID:     "c1",
			dialect:    DialectPostgreSQL,
			poolGen:    1,
			mutate:     func(m *TableMetadata) { m.Columns[0].Name = "" },
			wantSubstr: "column",
		},
		{
			name:       "pk references unknown column",
			connID:     "c1",
			dialect:    DialectPostgreSQL,
			poolGen:    1,
			mutate:     func(m *TableMetadata) { m.PrimaryKey.Columns = []string{"nope"} },
			wantSubstr: "primary",
		},
		{
			name:       "empty pk columns",
			connID:     "c1",
			dialect:    DialectPostgreSQL,
			poolGen:    1,
			mutate:     func(m *TableMetadata) { m.PrimaryKey.Columns = nil },
			wantSubstr: "primary",
		},
		{
			name:       "unique references unknown column",
			connID:     "c1",
			dialect:    DialectPostgreSQL,
			poolGen:    1,
			mutate:     func(m *TableMetadata) { m.UniqueConstraints[0].Columns = []string{"bogus"} },
			wantSubstr: "unique",
		},
		{
			name:       "empty unique constraint columns",
			connID:     "c1",
			dialect:    DialectPostgreSQL,
			poolGen:    1,
			mutate:     func(m *TableMetadata) { m.UniqueConstraints[0].Columns = nil },
			wantSubstr: "unique",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			meta := validTableMeta()
			if tt.mutate != nil {
				tt.mutate(meta)
			}
			_, err := NewSchemaSnapshot(tt.connID, tt.dialect, tt.poolGen, meta)
			if err == nil {
				t.Fatal("NewSchemaSnapshot() error = nil, want fail-closed")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("NewSchemaSnapshot() error = %q, want substring %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

func TestSchemaGenerationDeterministic(t *testing.T) {
	t.Parallel()
	s1, err := NewSchemaSnapshot("c1", DialectPostgreSQL, 1, validTableMeta())
	if err != nil {
		t.Fatal(err)
	}
	s2, err := NewSchemaSnapshot("c1", DialectPostgreSQL, 99, validTableMeta())
	if err != nil {
		t.Fatal(err)
	}
	if s1.SchemaGeneration != s2.SchemaGeneration {
		t.Fatalf("generation differs for identical metadata: %q vs %q", s1.SchemaGeneration, s2.SchemaGeneration)
	}
}

func TestSchemaGenerationChangesOnMetadataChange(t *testing.T) {
	t.Parallel()
	metaA := validTableMeta()
	metaB := validTableMeta()
	metaB.Columns[2].Nullable = false // email 从 nullable 改为 NOT NULL
	metaB.UniqueConstraints = append(metaB.UniqueConstraints, UniqueConstraint{Name: "uq_extra", Columns: []string{"tenant_id"}})

	sA, err := NewSchemaSnapshot("c1", DialectPostgreSQL, 1, metaA)
	if err != nil {
		t.Fatal(err)
	}
	sB, err := NewSchemaSnapshot("c1", DialectPostgreSQL, 1, metaB)
	if err != nil {
		t.Fatal(err)
	}
	if sA.SchemaGeneration == sB.SchemaGeneration {
		t.Fatal("generation must change when nullable/unique metadata changes")
	}
}

func TestSchemaGenerationDiffersByTableLineage(t *testing.T) {
	t.Parallel()
	metaA := validTableMeta()
	metaB := validTableMeta()
	metaB.Table = "users_copy"
	sA, err := NewSchemaSnapshot("c1", DialectPostgreSQL, 1, metaA)
	if err != nil {
		t.Fatal(err)
	}
	sB, err := NewSchemaSnapshot("c1", DialectPostgreSQL, 1, metaB)
	if err != nil {
		t.Fatal(err)
	}
	if sA.SchemaGeneration == sB.SchemaGeneration {
		t.Fatal("generation must differ for different table lineage")
	}
}

// TableMetadata 派生自 adapter 加载结果时不得把内部 slice 暴露给调用方修改。
func TestSchemaSnapshotTableMetadataIsolated(t *testing.T) {
	t.Parallel()
	meta := validTableMeta()
	s, err := NewSchemaSnapshot("c1", DialectPostgreSQL, 1, meta)
	if err != nil {
		t.Fatal(err)
	}
	// 修改外部 meta 不应影响 snapshot 已持有的元数据（深拷贝）。
	meta.Columns[0].Name = "hacked"
	meta.PrimaryKey.Columns[0] = "hacked"
	meta.UniqueConstraints[0].Columns[0] = "hacked"

	cols := s.Columns()
	if len(cols) == 0 || cols[0].Name != "id" {
		t.Fatalf("snapshot columns mutated by caller: %+v", cols)
	}
	pk := s.PrimaryKey()
	if len(pk) == 0 || pk[0] != "id" {
		t.Fatalf("snapshot pk mutated by caller: %+v", pk)
	}
	uqs := s.UniqueConstraints()
	if len(uqs) == 0 || uqs[0].Columns[0] != "tenant_id" {
		t.Fatalf("snapshot unique constraints mutated by caller: %+v", uqs)
	}
}

func TestSnapshotUniqueConstraintCopyIsolation(t *testing.T) {
	t.Parallel()
	meta := validTableMeta()
	s, err := NewSchemaSnapshot("c1", DialectPostgreSQL, 1, meta)
	if err != nil {
		t.Fatal(err)
	}
	// 修改返回的切片副本不得影响 snapshot 内部状态。
	uqs := s.UniqueConstraints()
	uqs[0].Columns[1] = "tampered"
	uqs2 := s.UniqueConstraints()
	if uqs2[0].Columns[1] != "email" {
		t.Fatalf("unique constraint mutated via returned copy: %+v", uqs2[0].Columns)
	}
}
