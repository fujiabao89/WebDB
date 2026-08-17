// Package adapter 定义 PostgreSQL/MySQL 统一数据库 Adapter 接口。
// P0-03 不实现 SQL 安全裁决（P0-04 职责），不公开 HTTP API。
package adapter

import (
	"crypto/subtle"

	"github.com/fujiabao89/webdb/internal/queryplan"
)

// Engine 数据库引擎类型。
type Engine string

const (
	EnginePostgreSQL Engine = "postgresql"
	EngineMySQL      Engine = "mysql"
)

// TLSMode TLS 连接模式。
type TLSMode string

const (
	TLSRequire TLSMode = "require"
	TLSPrefer  TLSMode = "prefer"
	TLSDisable TLSMode = "disable"
)

func normalizeTLSMode(m TLSMode) TLSMode {
	if m == "" {
		return TLSRequire
	}
	return m
}

// UserWorkspaceScope 请求作用域（非安全凭证）。
type UserWorkspaceScope struct {
	UserID      string `json:"user_id"`
	WorkspaceID string `json:"workspace_id"`
}

// FirstPageRequest 首页查询请求。
// SortPlan 为 ADR-014 的 VerifiedSortPlan；nil 表示单页受限请求。
// 客户端无法提交唯一性证明；Adapter 不再信任任何 SortKey.Unique。
type FirstPageRequest struct {
	Scope    UserWorkspaceScope
	SQL      string
	Args     []any
	SortPlan queryplan.VerifiedSortPlan
	PageSize int
	MaxRows  int
}

// QueryResult 查询结果。
// HasMore 表示是否还有后续行（由 Adapter 通过预读 sentinel 判定）；
// token 生成/续页归属服务层（ADR-015），Adapter 不返回 token。
type QueryResult struct {
	Columns       []ColumnInfo `json:"columns"`
	Rows          [][]any      `json:"rows"`
	HasMore       bool         `json:"has_more"`
	ReturnedRows  int          `json:"returned_rows"`
	TotalReturned int          `json:"total_returned"`

	// readAhead 是适配器私有、不可序列化的预读哨兵信号，与公开 HasMore 分离：
	// 单页受限请求据此在读到 effPage+1 哨兵行时返回 result_too_large，
	// 而公开 HasMore 仍受 total < maxRows 约束，避免累计分页到上限时
	// 出现 has_more=true 但无 token 的不一致响应。
	readAhead bool
}

// ColumnInfo 结果列元数据。
type ColumnInfo struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
}

// ConnectConfig 目标数据库连接配置。
type ConnectConfig struct {
	ConnectionID   string
	SecretVersion  int
	ConfigRevision int64
	Engine         Engine
	Host           string
	Port           int
	User           string
	Password       string
	Database       string
	TLS            TLSMode
	MaxOpen        int
	MaxIdle        int
	MaxPageBytes   int
	MaxCellBytes   int
}

func (c ConnectConfig) compareConfig(o ConnectConfig) bool {
	return c.ConnectionID == o.ConnectionID &&
		c.SecretVersion == o.SecretVersion &&
		c.Engine == o.Engine &&
		c.Host == o.Host &&
		c.Port == o.Port &&
		c.User == o.User &&
		c.Database == o.Database &&
		normalizeTLSMode(c.TLS) == normalizeTLSMode(o.TLS) &&
		normInt(c.MaxOpen, 10) == normInt(o.MaxOpen, 10) &&
		normInt(c.MaxIdle, 2) == normInt(o.MaxIdle, 2) &&
		normInt(c.MaxPageBytes, defaultMaxPageBytes) == normInt(o.MaxPageBytes, defaultMaxPageBytes) &&
		normInt(c.MaxCellBytes, defaultMaxCellBytes) == normInt(o.MaxCellBytes, defaultMaxCellBytes)
}

func normInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// ManagerOptions AdapterManager 创建选项。
type ManagerOptions struct {
	AllowInsecureLocalDemo bool
}

const (
	defaultMaxPageBytes = 2 << 20
	defaultMaxCellBytes = 256 << 10
)

type PoolStats struct {
	ActiveConns     int32
	IdleConns       int32
	MaxOpen         int
	AcquireTimeouts int64
}

type ManagerStats struct {
	TotalPools      int32
	RateLimitedUser int64
	RateLimitedWS   int64
	RateLimitedConn int64
	ActiveTokens    int32
}

func constantTimeEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
