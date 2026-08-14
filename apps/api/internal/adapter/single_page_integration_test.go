//go:build integration

package adapter

import (
	"context"
	"errors"
	"testing"
)

// TestSinglePageSentinel_PG 验证 P0-04 哨兵契约：单页请求（无 SortPlan，MaxRows<=PageSize）
// 读到 effPage+1 哨兵行时返回 result_too_large，而非静默截断到前 500 行。
// 用 generate_series 合成 500/600 行，不新增表，不依赖管理员权限。
func TestSinglePageSentinel_PG(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	defer h.Release()

	scope := UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"}

	// 恰好 500 行：成功，无哨兵（HasMore=false），返回全部 500 行。
	exact := queryMustSucceed(t, h, FirstPageRequest{
		Scope:    scope,
		SQL:      "SELECT generate_series(1, 500) AS id",
		PageSize: 500,
		MaxRows:  500,
	})
	if exact.ReturnedRows != 500 || exact.HasMore {
		t.Fatalf("恰好 500 行应成功且无 HasMore，got rows=%d hasMore=%v", exact.ReturnedRows, exact.HasMore)
	}

	// 600 行：读到第 501 行哨兵 → 返回 ErrResultTooLarge，不返回前 500 行结果。
	_, err := h.Query(context.Background(), FirstPageRequest{
		Scope:    scope,
		SQL:      "SELECT generate_series(1, 600) AS id",
		PageSize: 500,
		MaxRows:  500,
	})
	var adapterErr *AdapterError
	if !errors.As(err, &adapterErr) || adapterErr.Code != ErrResultTooLarge {
		t.Fatalf("600 行应返回 ErrResultTooLarge，got err=%v", err)
	}
}
