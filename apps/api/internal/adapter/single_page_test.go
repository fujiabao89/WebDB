package adapter

import (
	"errors"
	"strings"
	"testing"
)

func TestPrepareFirstPageSortSinglePageWithoutVerifiedKey(t *testing.T) {
	t.Parallel()

	specs, singlePage, err := prepareFirstPageSort(FirstPageRequest{
		PageSize: 250,
		MaxRows:  250,
	})
	if err != nil {
		t.Fatalf("prepareFirstPageSort() error = %v", err)
	}
	if !singlePage {
		t.Fatal("prepareFirstPageSort() singlePage = false, want true")
	}
	if specs != nil {
		t.Fatalf("prepareFirstPageSort() specs = %+v, want nil", specs)
	}
}

func TestPrepareFirstPageSortRejectsCrossPageWithoutVerifiedKey(t *testing.T) {
	t.Parallel()

	_, _, err := prepareFirstPageSort(FirstPageRequest{
		PageSize: 100,
		MaxRows:  500,
	})
	var adapterErr *AdapterError
	if !errors.As(err, &adapterErr) || adapterErr.Code != ErrUnsupportedQuery {
		t.Fatalf("prepareFirstPageSort() error = %v, want %s", err, ErrUnsupportedQuery)
	}
}

func TestBuildWrappedSQLWithoutSortHasNoOrderBy(t *testing.T) {
	t.Parallel()

	query, args, err := buildWrappedSQL(
		"SELECT value FROM synthetic_table",
		nil,
		EnginePostgreSQL,
		nil,
		[]any{"arg"},
		251,
	)
	if err != nil {
		t.Fatalf("buildWrappedSQL() error = %v", err)
	}
	if strings.Contains(strings.ToUpper(query), "ORDER BY") {
		t.Fatalf("buildWrappedSQL() query unexpectedly contains ORDER BY:\n%s", query)
	}
	if !strings.Contains(query, "LIMIT $2") {
		t.Fatalf("buildWrappedSQL() query = %q, want LIMIT $2", query)
	}
	if len(args) != 2 || args[1] != 251 {
		t.Fatalf("buildWrappedSQL() args = %#v, want original arg plus limit", args)
	}
}
