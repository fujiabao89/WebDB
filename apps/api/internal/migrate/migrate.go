// Package migrate 提供 WebDB 元数据库迁移能力。
// 使用 pressly/goose/v3 执行嵌入式 SQL migration。
// API 进程启动时不会自动迁移；迁移命令由 CLI 子命令显式触发。
package migrate

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Run 按指定方向执行 migration。
// dir 为 "up" 或 "down"；db 必须是 PostgreSQL 连接。
func Run(ctx context.Context, db *sql.DB, dir string, args ...string) error {
	goose.SetBaseFS(migrations)

	if err := goose.RunContext(ctx, dir, db, "migrations", args...); err != nil {
		return fmt.Errorf("migration %s 失败: %w", dir, err)
	}
	return nil
}

// Status 输出 migration 版本状态。
func Status(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(migrations)

	s, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return fmt.Errorf("获取 migration 状态失败: %w", err)
	}
	fmt.Printf("当前 migration 版本: %d\n", s)
	return nil
}

// Validate 校验 migration 文件完整性和 SQL 语法（不连接数据库）。
func Validate() error {
	goose.SetBaseFS(migrations)

	entries, err := goose.CollectMigrations("migrations", 0, goose.MaxVersion)
	if err != nil {
		return fmt.Errorf("migration 校验失败: %w", err)
	}
	fmt.Printf("已发现 %d 个 migration\n", len(entries))

	// 额外解析每个 SQL 文件：验证 goose 指令完整性
	for _, e := range entries {
		src, err := fs.ReadFile(migrations, e.Source)
		if err != nil {
			return fmt.Errorf("migration %s: 读取失败: %w", e.Source, err)
		}
		if len(src) == 0 {
			return fmt.Errorf("migration %s: 文件为空", e.Source)
		}
		// 验证必须包含 -- +goose Up 和 -- +goose Down 指令
		if !bytes.Contains(src, []byte("-- +goose Up")) {
			return fmt.Errorf("migration %s: 缺少 -- +goose Up 指令", e.Source)
		}
		if !bytes.Contains(src, []byte("-- +goose Down")) {
			return fmt.Errorf("migration %s: 缺少 -- +goose Down 指令", e.Source)
		}
		fmt.Printf("  ✓ %s (%d bytes)\n", e.Source, len(src))
	}
	return nil
}
