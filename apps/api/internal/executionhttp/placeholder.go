package executionhttp

import "github.com/fujiabao89/webdb/internal/sqlpolicy"

// detectUnboundPlaceholder 委托 sqlpolicy.HasUnboundPlaceholder 做方言感知
// token 级占位符判定（P0-06A §8.1）。真实 SQL 安全判定在服务端 sqlpolicy 完成；
// 本层仅作为 HTTP 校验辅助，最终由 execution Pipeline 在引擎已知后执行。
func detectUnboundPlaceholder(dialect string, sql string) bool {
	return sqlpolicy.HasUnboundPlaceholder(sqlpolicy.Dialect(dialect), sql)
}
