package queryplan

import (
	"testing"
)

func FuzzVerifySortPlanNoPanic(f *testing.F) {
	meta := &TableMetadata{
		Schema: "public", Table: "users",
		Columns: []Column{
			{Name: "id", Ordinal: 1, Nullable: false},
			{Name: "tenant_id", Ordinal: 2, Nullable: false},
			{Name: "email", Ordinal: 3, Nullable: true},
		},
		PrimaryKey: &PrimaryKey{Columns: []string{"id"}},
		UniqueConstraints: []UniqueConstraint{
			{Name: "uq_t_e", Columns: []string{"tenant_id", "email"}},
		},
	}
	snap, err := NewSchemaSnapshot("conn-1", DialectPostgreSQL, 1, meta)
	if err != nil {
		f.Fatalf("NewSchemaSnapshot: %v", err)
	}
	shape := &QueryShape{BaseSchema: "public", BaseTable: "users", SelectStar: true}

	f.Add("id", "ASC")
	f.Add("tenant_id", "DESC")
	f.Add("email", "ASC")
	f.Add("nonexistent", "ASC")
	f.Add("", "ASC")
	f.Add("id", "SIDEWAYS")

	f.Fuzz(func(t *testing.T, col string, dir string) {
		keys := []SortKey{{Column: col, Direction: SortDirection(dir), NullsLast: len(col)%2 == 0}}
		p, err := VerifySortPlan(snap, shape, keys)
		if err == nil {
			if !IsValidVerifiedPlan(p) {
				t.Fatalf("VerifySortPlan returned invalid plan for %q", col)
			}
			if p.Version() != sortPlanVersion {
				t.Fatalf("wrong version %d", p.Version())
			}
		}
	})
}
