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

func TestFinalizeResultSignalsSentinelRow(t *testing.T) {
	t.Parallel()

	rows := make([][]any, 500)
	// 单页 600 行（rc=501 > effPage=500）：检测到哨兵行（readAhead=true），
	// 但公开 HasMore 因 total==maxRows 为 false（由单页路径据此返回 result_too_large）。
	withSentinel := finalizeResult(nil, rows, 501, 500, 0, 500)
	if !withSentinel.readAhead {
		t.Fatal("readAhead = false, want true when rc(501) > effPage(500) indicates a sentinel row")
	}
	if withSentinel.HasMore {
		t.Fatal("HasMore = true, want false when total(500) == maxRows(500)")
	}
	if withSentinel.ReturnedRows != 500 {
		t.Fatalf("ReturnedRows = %d, want 500 (sentinel row discarded)", withSentinel.ReturnedRows)
	}

	exact := finalizeResult(nil, rows, 500, 500, 0, 500)
	if exact.readAhead {
		t.Fatal("readAhead = true, want false when rc == effPage (no sentinel)")
	}
	if exact.HasMore {
		t.Fatal("HasMore = true, want false when no sentinel")
	}
}

func TestFinalizeResultHasMoreGatedByCumulativeCap(t *testing.T) {
	t.Parallel()

	rows := make([][]any, 100)

	// 分页第 1 页（cumCount=0, effPage=100, maxRows=500）：有后续页 → readAhead 与 HasMore 均 true
	page1 := finalizeResult(nil, rows, 101, 100, 0, 500)
	if !page1.readAhead || !page1.HasMore {
		t.Fatalf("page1 readAhead=%v HasMore=%v, want both true (sentinel + total<maxRows)", page1.readAhead, page1.HasMore)
	}

	// 分页第 5 页（cumCount=400，累计 500 行）：底层还有数据但已达累计上限 → HasMore=false
	page5 := finalizeResult(nil, rows, 101, 100, 400, 500)
	if !page5.readAhead {
		t.Fatal("page5 readAhead = false, want true (sentinel row present)")
	}
	if page5.HasMore {
		t.Fatal("page5 HasMore = true, want false (total 500 reached maxRows 500)")
	}
}
