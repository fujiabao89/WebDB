package queryplan

import (
	"strings"
	"testing"
)

func snapshotWithPK(pk ...string) *SchemaSnapshot {
	meta := &TableMetadata{
		Schema: "public",
		Table:  "users",
		Columns: []Column{
			{Name: "id", Ordinal: 1, Nullable: false},
			{Name: "tenant_id", Ordinal: 2, Nullable: false},
			{Name: "email", Ordinal: 3, Nullable: true},
			{Name: "name", Ordinal: 4, Nullable: false},
		},
		PrimaryKey: &PrimaryKey{Columns: pk},
	}
	s, err := NewSchemaSnapshot("conn-1", DialectPostgreSQL, 1, meta)
	if err != nil {
		panic(err)
	}
	return s
}

func starShape(table string) *QueryShape {
	return &QueryShape{BaseSchema: "public", BaseTable: table, SelectStar: true}
}

func key(col string, dir SortDirection, nullsLast bool) SortKey {
	return SortKey{Column: col, Direction: dir, NullsLast: nullsLast}
}

func TestVerifySortPlanSingleColumnPK(t *testing.T) {
	t.Parallel()
	p, err := VerifySortPlan(snapshotWithPK("id"), starShape("users"), []SortKey{key("id", SortAsc, false)})
	if err != nil {
		t.Fatalf("VerifySortPlan() error = %v", err)
	}
	if !p.Valid() {
		t.Fatal("plan invalid")
	}
	if !IsValidVerifiedPlan(p) {
		t.Fatal("IsValidVerifiedPlan() = false")
	}
	specs := p.SortSpecs()
	if len(specs) != 1 || specs[0].Column != "id" || !specs[0].Asc || specs[0].NullsLast {
		t.Fatalf("specs = %+v", specs)
	}
}

func TestVerifySortPlanCompositePKFullyCovered(t *testing.T) {
	t.Parallel()
	keys := []SortKey{key("tenant_id", SortAsc, false), key("id", SortAsc, false)}
	p, err := VerifySortPlan(snapshotWithPK("tenant_id", "id"), starShape("users"), keys)
	if err != nil {
		t.Fatalf("VerifySortPlan() error = %v", err)
	}
	if !p.Valid() {
		t.Fatal("plan invalid")
	}
}

func TestVerifySortPlanNotNullUniqueConstraint(t *testing.T) {
	t.Parallel()
	// 无主键，但 (tenant_id, name) 全 NOT NULL 唯一约束。
	meta := &TableMetadata{
		Schema: "public",
		Table:  "users",
		Columns: []Column{
			{Name: "tenant_id", Ordinal: 1, Nullable: false},
			{Name: "name", Ordinal: 2, Nullable: false},
			{Name: "email", Ordinal: 3, Nullable: true},
		},
		UniqueConstraints: []UniqueConstraint{{Name: "uq_tn", Columns: []string{"tenant_id", "name"}}},
	}
	s, err := NewSchemaSnapshot("conn-1", DialectPostgreSQL, 1, meta)
	if err != nil {
		t.Fatal(err)
	}
	keys := []SortKey{key("name", SortDesc, true), key("tenant_id", SortAsc, false)}
	p, err := VerifySortPlan(s, starShape("users"), keys)
	if err != nil {
		t.Fatalf("VerifySortPlan() error = %v", err)
	}
	if !p.Valid() {
		t.Fatal("plan invalid")
	}
}

func TestVerifySortPlanRejects(t *testing.T) {
	t.Parallel()

	nullableUniqueMeta := func() *SchemaSnapshot {
		meta := &TableMetadata{
			Schema: "public", Table: "users",
			Columns: []Column{
				{Name: "id", Ordinal: 1, Nullable: false},
				{Name: "email", Ordinal: 2, Nullable: true},
			},
			PrimaryKey: &PrimaryKey{Columns: []string{"id"}},
			UniqueConstraints: []UniqueConstraint{
				{Name: "uq_email", Columns: []string{"email"}},
			},
		}
		s, _ := NewSchemaSnapshot("conn-1", DialectPostgreSQL, 1, meta)
		return s
	}

	noProofMeta := func() *SchemaSnapshot {
		meta := &TableMetadata{
			Schema: "public", Table: "users",
			Columns: []Column{{Name: "id", Ordinal: 1, Nullable: false}},
		}
		s, _ := NewSchemaSnapshot("conn-1", DialectPostgreSQL, 1, meta)
		return s
	}

	tests := []struct {
		name       string
		snapshot   *SchemaSnapshot
		shape      *QueryShape
		keys       []SortKey
		wantSubstr string
	}{
		{
			name:       "empty sort keys",
			snapshot:   snapshotWithPK("id"),
			shape:      starShape("users"),
			keys:       nil,
			wantSubstr: "sort",
		},
		{
			name:       "sort column not a base column",
			snapshot:   snapshotWithPK("id"),
			shape:      starShape("users"),
			keys:       []SortKey{key("nonexistent", SortAsc, false)},
			wantSubstr: "base column",
		},
		{
			name:       "sort column not exposed in result",
			snapshot:   snapshotWithPK("id"),
			shape:      &QueryShape{BaseSchema: "public", BaseTable: "users", Columns: map[string]string{"name": "name"}},
			keys:       []SortKey{key("id", SortAsc, false)},
			wantSubstr: "exposed",
		},
		{
			name:       "sort column is computed expression",
			snapshot:   snapshotWithPK("id"),
			shape:      &QueryShape{BaseSchema: "public", BaseTable: "users", Columns: map[string]string{"id": ""}},
			keys:       []SortKey{key("id", SortAsc, false)},
			wantSubstr: "base column",
		},
		{
			name:       "partial composite unique only",
			snapshot:   snapshotWithPK("tenant_id", "id"),
			shape:      starShape("users"),
			keys:       []SortKey{key("tenant_id", SortAsc, false)},
			wantSubstr: "unique",
		},
		{
			name:       "nullable unique column",
			snapshot:   nullableUniqueMeta(),
			shape:      starShape("users"),
			keys:       []SortKey{key("email", SortAsc, false)},
			wantSubstr: "unique",
		},
		{
			name:       "no unique proof available",
			snapshot:   noProofMeta(),
			shape:      starShape("users"),
			keys:       []SortKey{key("id", SortAsc, false)},
			wantSubstr: "unique",
		},
		{
			name:       "duplicate sort columns",
			snapshot:   snapshotWithPK("id"),
			shape:      starShape("users"),
			keys:       []SortKey{key("id", SortAsc, false), key("id", SortDesc, false)},
			wantSubstr: "duplicate",
		},
		{
			name:       "invalid sort direction",
			snapshot:   snapshotWithPK("id"),
			shape:      starShape("users"),
			keys:       []SortKey{{Column: "id", Direction: SortDirection("SIDEWAYS")}},
			wantSubstr: "direction",
		},
		{
			name:       "shape table mismatch",
			snapshot:   snapshotWithPK("id"),
			shape:      starShape("orders"),
			keys:       []SortKey{key("id", SortAsc, false)},
			wantSubstr: "table",
		},
		{
			name:       "nil snapshot",
			snapshot:   nil,
			shape:      starShape("users"),
			keys:       []SortKey{key("id", SortAsc, false)},
			wantSubstr: "snapshot",
		},
		{
			name:       "nil shape",
			snapshot:   snapshotWithPK("id"),
			shape:      nil,
			keys:       []SortKey{key("id", SortAsc, false)},
			wantSubstr: "shape",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := VerifySortPlan(tt.snapshot, tt.shape, tt.keys)
			if err == nil {
				t.Fatal("VerifySortPlan() error = nil, want fail-closed")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("VerifySortPlan() error = %q, want substring %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

func TestVerifySortPlanClientUniqueFlagHasNoEffect(t *testing.T) {
	t.Parallel()
	// 客户端排序键不含 Unique 字段；这里验证无任何唯一声明也能通过唯一性证明。
	p, err := VerifySortPlan(snapshotWithPK("id"), starShape("users"), []SortKey{key("id", SortAsc, false)})
	if err != nil {
		t.Fatalf("VerifySortPlan() error = %v", err)
	}
	if !p.Valid() {
		t.Fatal("plan invalid")
	}
}

func TestIsValidVerifiedPlanRejectsNilAndTypedNil(t *testing.T) {
	t.Parallel()
	if IsValidVerifiedPlan(nil) {
		t.Fatal("IsValidVerifiedPlan(nil) = true")
	}
	var tn *verifiedSortPlan
	if IsValidVerifiedPlan(tn) {
		t.Fatal("IsValidVerifiedPlan(typed-nil) = true")
	}
}

func TestSortSpecsDeepCopy(t *testing.T) {
	t.Parallel()
	p, err := VerifySortPlan(snapshotWithPK("id"), starShape("users"), []SortKey{key("id", SortAsc, false)})
	if err != nil {
		t.Fatal(err)
	}
	specs := p.SortSpecs()
	specs[0].Column = "tampered"
	specs2 := p.SortSpecs()
	if specs2[0].Column != "id" {
		t.Fatalf("SortSpecs mutated via returned copy: %+v", specs2)
	}
}

func TestVerifiedSortPlanVersionStable(t *testing.T) {
	t.Parallel()
	p, err := VerifySortPlan(snapshotWithPK("id"), starShape("users"), []SortKey{key("id", SortAsc, false)})
	if err != nil {
		t.Fatal(err)
	}
	if p.Version() != sortPlanVersion {
		t.Fatalf("Version() = %d, want %d", p.Version(), sortPlanVersion)
	}
}

func TestVerifySortPlanSnapshotGenerationBound(t *testing.T) {
	t.Parallel()
	snap := snapshotWithPK("id")
	p, err := VerifySortPlan(snap, starShape("users"), []SortKey{key("id", SortAsc, false)})
	if err != nil {
		t.Fatal(err)
	}
	b := p.SnapshotBinding()
	if b.ConnectionID != snap.ConnectionID || b.Dialect != snap.Dialect ||
		b.PoolGeneration != snap.PoolGeneration || b.SchemaGeneration != snap.SchemaGeneration {
		t.Fatalf("binding = %+v, want snapshot binding", b)
	}
}
