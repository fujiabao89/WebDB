package browse

import (
	"context"
	"fmt"

	"github.com/fujiabao89/webdb/internal/adapter"
)

// AdapterBrowser 是 MetadataBrowser 的生产实现：包装 AdapterManager.Get 与
// PoolHandle 元数据浏览。每个浏览操作独立获取 handle 并 defer Release 归还连接，
// 不缓存目标库元数据（P0-06A §7：每次浏览重新获取连接，不做无界缓存）。
type AdapterBrowser struct {
	Manager *adapter.AdapterManager
}

// manager 校验注入的 AdapterManager，nil 时返回 internal_error（F3，不允许 nil 解引用）。
func (b AdapterBrowser) manager() (*adapter.AdapterManager, error) {
	if b.Manager == nil {
		return nil, fmt.Errorf("%w", ErrInternalError)
	}
	return b.Manager, nil
}

// Schemas 列出目标库 Schema（catalog 由服务层用连接 database 派生）。
func (b AdapterBrowser) Schemas(ctx context.Context, cfg adapter.ConnectConfig) ([]adapter.Schema, error) {
	mgr, err := b.manager()
	if err != nil {
		return nil, err
	}
	h, err := mgr.Get(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer h.Release()
	return h.Schemas(ctx)
}

// Tables 列出指定 Schema 下的表/视图。
func (b AdapterBrowser) Tables(ctx context.Context, cfg adapter.ConnectConfig, schema string) ([]adapter.Table, error) {
	mgr, err := b.manager()
	if err != nil {
		return nil, err
	}
	h, err := mgr.Get(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer h.Release()
	return h.Tables(ctx, schema)
}

// Columns 列出指定 Schema/Table 的列。
func (b AdapterBrowser) Columns(ctx context.Context, cfg adapter.ConnectConfig, schema, table string) ([]adapter.Column, error) {
	mgr, err := b.manager()
	if err != nil {
		return nil, err
	}
	h, err := mgr.Get(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer h.Release()
	return h.Columns(ctx, schema, table)
}
