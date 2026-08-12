package adapter

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 数据库强制只读边界（P0-06A §8.3/§14 R5，WEB-35 交付）。
//
// 每次目标查询都在显式只读事务/会话中执行，并验证只读已生效。fail-closed：
//   - 只读设置失败
//   - 连接已存在任何未确认只读的事务（含已开启的可写事务）
//   - 池复用连接无法确认只读
//
// 以上任一情况都拒绝执行目标查询（错误折叠为公共 connection_unavailable）。
// 查询结束显式 ROLLBACK，确保连接归还后不残留事务或 session 状态。

// beginReadOnlyPG 开启并验证 PG 只读事务。
// 用 pgx 客户端跟踪的 TxStatus 检测连接是否已在事务块中（含 failed/unknown）→
// 拒绝，避免把调用方事务回滚掉（fail-closed）。TxStatus 由 pgx 根据服务器协议
// 维护，无需额外查询（txid 懒分配 / pg_stat_activity 自查询均不可靠）。
// 再 BEGIN TRANSACTION READ ONLY 并验证 transaction_read_only=on。
// 任一失败返回 ErrConnectionFailed（折叠 503）。
func beginReadOnlyPG(ctx context.Context, conn *pgxpool.Conn) error {
	// TxStatus 'I' = idle（服务器 ReadyForQuery 报告）。其他值（T/E/H）表示已有事务。
	if conn.Conn().PgConn().TxStatus() != 'I' {
		return newError(ErrConnectionFailed, "connection already in a transaction", nil)
	}
	if _, err := conn.Exec(ctx, "BEGIN TRANSACTION READ ONLY"); err != nil {
		return wrapReadOnly("begin read-only transaction", err)
	}
	var ro string
	if err := conn.QueryRow(ctx, "SHOW transaction_read_only").Scan(&ro); err != nil {
		return wrapReadOnly("verify read-only transaction", err)
	}
	if !strings.EqualFold(strings.TrimSpace(ro), "on") {
		return newError(ErrConnectionFailed, "read-only transaction not confirmed", nil)
	}
	return nil
}

// endReadOnlyPG 以 ROLLBACK 结束只读事务（只读事务不提交，ROLLBACK 释放）。
func endReadOnlyPG(ctx context.Context, conn *pgxpool.Conn) error {
	_, err := conn.Exec(ctx, "ROLLBACK")
	return err
}

// beginReadOnlyMySQL 开启并验证 MySQL 只读事务。
// 检测 @@autocommit：非 1（连接被设为手动事务模式，可能有未提交事务）→
// fail-closed 拒绝，避免 START TRANSACTION 隐式提交调用方事务。
// database/sql 连接池在归还时清理未完成事务，正常池连接 autocommit=1。
// START TRANSACTION READ ONLY 建立只读事务：MySQL 8.4 无事务级只读变量查询
// （@@transaction_read_only 只反映会话默认值，实测 START READ ONLY 后仍为 0），
// 语句成功即确认只读语义（MySQL 保证 READ ONLY 事务拒绝数据修改语句）。
func beginReadOnlyMySQL(ctx context.Context, conn *sql.Conn) error {
	// 同时检查 @@session.in_transaction（Codex P1）：@@autocommit=1 不能证明无活动
	// 事务——MySQL 8.4 显式 START TRANSACTION 后 autocommit 仍为 1 而 in_transaction=1，
	// 若放行，下方 START TRANSACTION READ ONLY 会隐式提交该可写事务（CT-22 fail-closed
	// 失效，可能提交泄漏写入）。
	var autocommit, inTx int
	if err := conn.QueryRowContext(ctx, "SELECT @@autocommit, @@session.in_transaction").Scan(&autocommit, &inTx); err != nil {
		return wrapReadOnly("check transaction state", err)
	}
	if autocommit != 1 || inTx != 0 {
		return newError(ErrConnectionFailed, "connection has active transaction", nil)
	}
	if _, err := conn.ExecContext(ctx, "START TRANSACTION READ ONLY"); err != nil {
		return wrapReadOnly("begin read-only transaction", err)
	}
	return nil
}

// endReadOnlyMySQL 以 ROLLBACK 结束只读事务。
func endReadOnlyMySQL(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "ROLLBACK")
	return err
}

// discardConn 销毁连接，确保其不被归还连接池复用（Owner P1-1 决策）：
// 事务建立/回滚/清理异常或取消/超时/panic 后，连接可能携带未清理的事务状态。
// database/sql 无法感知自定义 START TRANSACTION 的状态，必须显式关闭底层驱动
// 连接并返回 driver.ErrBadConn，使 database/sql 标记该连接坏而销毁。
func discardConn(conn *sql.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Raw(func(dc any) error {
		if c, ok := dc.(driver.Conn); ok {
			_ = c.Close()
		}
		return driver.ErrBadConn
	})
}

// discardPGConn 销毁 pgxpool 连接，确保其不被归还连接池复用（与 discardConn 对
// MySQL 的语义一致，CodeRabbit #7）。只读事务结束/回滚失败后连接状态未知，必须
// 关闭底层 PgConn；pgxpool 在 Release 时检测到已关闭连接会销毁而非复用。
func discardPGConn(ctx context.Context, conn *pgxpool.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Conn().Close(ctx)
}

// wrapReadOnly 包装只读设置失败，message 固定（不含驱动原始错误，防止敏感信息
// 进入日志/响应）；cause 保留在错误链供 errors.Is 判定。
func wrapReadOnly(stage string, err error) error {
	return newError(ErrConnectionFailed, fmt.Sprintf("read-only %s failed", stage), err)
}
