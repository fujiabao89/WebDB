// Package migrate 提供 WebDB 元数据库迁移能力。
// 使用 pressly/goose/v3 执行嵌入式 SQL migration。
// API 进程启动时不会自动迁移；迁移命令由 CLI 子命令显式触发。
package migrate

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Run 按指定方向执行 migration。
// dir 为 "up" 或 "down"；db 必须是 PostgreSQL 连接。
func Run(ctx context.Context, db *sql.DB, dir string) error {
	goose.SetBaseFS(migrations)

	if err := goose.RunContext(ctx, dir, db, "migrations"); err != nil {
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

// Validate 校验 migration 文件完整性（不连接数据库）。
func Validate() error {
	goose.SetBaseFS(migrations)

	entries, err := goose.CollectMigrations("migrations", 0, goose.MaxVersion)
	if err != nil {
		return fmt.Errorf("migration 校验失败: %w", err)
	}
	fmt.Printf("已发现 %d 个 migration\n", len(entries))
	return nil
}
