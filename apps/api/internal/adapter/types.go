// Package adapter 定义 PostgreSQL/MySQL 统一数据库 Adapter 接口。
// P0-03 不实现 SQL 安全裁决（P0-04 职责），不公开 HTTP API。
package adapter

import (
	"crypto/subtle"
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

// SortOrder 排序方向。
type SortOrder string

const (
	SortAsc  SortOrder = "ASC"
	SortDesc SortOrder = "DESC"
)

// SortKey 排序键定义。
type SortKey struct {
	Column    string    `json:"column"`
	Order     SortOrder `json:"order"`
	NullsLast bool      `json:"nulls_last"`
	Unique    bool      `json:"unique,omitempty"` // 该列是否构成唯一顺序，用于 keyset tie-breaker
}

// UserWorkspaceScope 请求作用域（非安全凭证）。
type UserWorkspaceScope struct {
	UserID      string `json:"user_id"`
	WorkspaceID string `json:"workspace_id"`
}

// FirstPageRequest 首页查询请求。
type FirstPageRequest struct {
	Scope    UserWorkspaceScope
	SQL      string
	Args     []any
	SortKeys []SortKey
	PageSize int
	MaxRows  int
}

// QueryResult 查询结果。
type QueryResult struct {
	Columns       []ColumnInfo `json:"columns"`
	Rows          [][]any      `json:"rows"`
	NextToken     *string      `json:"next_token,omitempty"`
	ReturnedRows  int          `json:"returned_rows"`
	TotalReturned int          `json:"total_returned"`
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
