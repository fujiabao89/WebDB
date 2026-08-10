package queryplan

import (
	"strings"
	"testing"
)

func validSortPlan(t *testing.T) VerifiedSortPlan {
	t.Helper()
	snap := snapshotWithPK("id")
	p, err := VerifySortPlan(snap, starShape("users"), []SortKey{key("id", SortAsc, false)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewVerifiedNextPagePlanValid(t *testing.T) {
	t.Parallel()
	p, err := NewVerifiedNextPagePlan(validSortPlan(t), []any{false, int64(42)}, "SELECT id FROM users", nil, 100, 500, 100)
	if err != nil {
		t.Fatalf("NewVerifiedNextPagePlan() error = %v", err)
	}
	if !p.Valid() {
		t.Fatal("plan invalid")
	}
	if p.PageSize() != 100 || p.MaxRows() != 500 || p.CumulativeCount() != 100 {
		t.Fatalf("plan bounds wrong")
	}
	lv := p.LastSortValues()
	if len(lv) != 2 || lv[1] != int64(42) {
		t.Fatalf("last values wrong: %+v", lv)
	}
}

func TestNewVerifiedNextPagePlanDeepCopiesLastValues(t *testing.T) {
	t.Parallel()
	inner := []any{false, []int64{1, 2, 3}}
	p, err := NewVerifiedNextPagePlan(validSortPlan(t), inner, "SELECT id FROM users", nil, 100, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	lv := p.LastSortValues()
	s, ok := lv[1].([]int64)
	if !ok {
		t.Fatalf("lv[1] type = %T, want []int64", lv[1])
	}
	s[0] = 999
	lv2 := p.LastSortValues()
	s2, ok := lv2[1].([]int64)
	if !ok {
		t.Fatalf("lv2[1] type = %T, want []int64", lv2[1])
	}
	if s2[0] == 999 {
		t.Fatal("LastSortValues not deep-copied")
	}
}

func TestNewVerifiedNextPagePlanRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		plan     VerifiedSortPlan
		lastVals []any
		pageSize int
		maxRows  int
		cum      int
		wantSub  string
	}{
		{name: "nil sort plan", plan: nil, lastVals: []any{false, int64(1)}, pageSize: 100, maxRows: 500, cum: 0, wantSub: "sort plan"},
		{name: "zero page size", plan: validSortPlan(t), lastVals: []any{false, int64(1)}, pageSize: 0, maxRows: 500, cum: 0, wantSub: "page size"},
		{name: "zero max rows", plan: validSortPlan(t), lastVals: []any{false, int64(1)}, pageSize: 100, maxRows: 0, cum: 0, wantSub: "max rows"},
		{name: "negative cumulative", plan: validSortPlan(t), lastVals: []any{false, int64(1)}, pageSize: 100, maxRows: 500, cum: -1, wantSub: "cumulative"},
		{name: "cumulative at max rows", plan: validSortPlan(t), lastVals: []any{false, int64(1)}, pageSize: 100, maxRows: 500, cum: 500, wantSub: "cumulative"},
		{name: "last values count mismatch", plan: validSortPlan(t), lastVals: []any{false}, pageSize: 100, maxRows: 500, cum: 0, wantSub: "last"},
		{name: "nil last values", plan: validSortPlan(t), lastVals: nil, pageSize: 100, maxRows: 500, cum: 0, wantSub: "last"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewVerifiedNextPagePlan(tt.plan, tt.lastVals, "SELECT id FROM users", nil, tt.pageSize, tt.maxRows, tt.cum)
			if err == nil {
				t.Fatal("NewVerifiedNextPagePlan() error = nil, want fail-closed")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantSub)
			}
		})
	}
}

func TestVerifiedNextPagePlanZeroValueInvalid(t *testing.T) {
	t.Parallel()
	var zero verifiedNextPagePlan
	if zero.Valid() {
		t.Fatal("zero-value VerifiedNextPagePlan must be invalid (ADR-015)")
	}
}
