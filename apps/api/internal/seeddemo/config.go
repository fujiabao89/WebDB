package seeddemo

import (
	"fmt"
	"strconv"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- 演示模式开关 -------------------------------------------------------------

// demoSwitchEnv 演示 seed 开关环境变量名。仅严格接受显式 "true"。
const demoSwitchEnv = "WEBDB_DEMO_SEED"

// validateDemoSwitch 演示模式开关门控：仅严格接受显式 "true"。
// 缺失、false 或任何非法值均拒绝（fail-closed）；不根据 hostname、数据库名或网络推断演示模式。
// 生产部署默认不存在该开关，因此 seed 命令永不自动执行。
func validateDemoSwitch(v string) error {
	if v != "true" {
		return fmt.Errorf("%w: %s 必须严格等于 true（当前缺失、false 或非法值）", ErrDemoSeedRefused, demoSwitchEnv)
	}
	return nil
}

// ---- 演示连接安全默认 ----------------------------------------------------------
//
// Owner 已批准的安全默认（P0-06A §6/§7）：单页最大 500 行、执行超时 5s；
// 不启用 DML/DDL/export（AllowWrite/AllowExport 留空 → DB 默认 false）。
// 禁止自行发明新的 connection/policy 字段或默认值。

const (
	policyStatementTimeoutMs = 5000
	policyMaxRows            = 500
)

// ---- 固定合成数据常量 -----------------------------------------------------------

const (
	// demoWorkspaceName 演示工作区合成名称（非真实）。
	demoWorkspaceName = "Demo Workspace"
	// demoUserEmail 演示 user 合成邮箱（example.local 保留域，非真实）。
	demoUserEmail = "demo@example.local"
	// demoPasswordHash 演示 user 的合成占位 hash。
	// P0 无登录、password_hash 无人校验，使用明确哨兵字符串而非 bcrypt，
	// 避免被误认为真实凭据；不得用于任何真实认证路径。
	demoPasswordHash = "!demo-placeholder-password-hash-do-not-use"
	// demoPGReaderRole / demoMySQLReaderRole 演示目标库固定只读角色。
	// 由 deploy/compose init 脚本创建；seed 必须绑定该角色，拒绝高权限账号漂移
	// （CodeRabbit P0-06A 回归项：MySQL 演示账号不能改为 root/任意用户）。
	demoPGReaderRole    = "demo_reader"
	demoMySQLReaderRole = "demo_reader"
)

// ---- 配置结构 -------------------------------------------------------------------

// ConnectionSpec 演示连接规格（全部为合成演示数据，来自部署环境）。
type ConnectionSpec struct {
	ID                 uuid.UUID
	Name               string
	Engine             metadata.Engine
	Host               string
	Port               int
	Database           string
	Environment        metadata.Environment
	CredentialUser     string // 目标库只读账号（PG 固定 demo_reader；MySQL 来自 DEMO_MYSQL_USER）
	CredentialPassword string // 目标库只读密码（仅内存；来自 env，绝不落盘/日志/错误）
}

// Config 由 LoadConfig 解析并校验的 seed 配置。
type Config struct {
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	Connections []ConnectionSpec
}

// LoadConfig 从部署环境解析并校验 seed 配置。
// 缺失/非法/与权威固定 UUID 不一致时 fail-closed，且错误信息不泄露变量值。
func LoadConfig(env func(string) string) (Config, error) {
	wsID, err := requiredPrincipalID(env, "DEMO_PRINCIPAL_WORKSPACE_ID", demoWorkspaceID)
	if err != nil {
		return Config{}, err
	}
	userID, err := requiredPrincipalID(env, "DEMO_PRINCIPAL_USER_ID", demoUserID)
	if err != nil {
		return Config{}, err
	}

	pg, err := loadPostgresSpec(env)
	if err != nil {
		return Config{}, err
	}
	my, err := loadMySQLSpec(env)
	if err != nil {
		return Config{}, err
	}

	return Config{
		WorkspaceID: wsID,
		UserID:      userID,
		Connections: []ConnectionSpec{pg, my},
	}, nil
}

// requiredPrincipalID 校验环境变量的固定合成 UUID 与权威来源（ids.go）完全一致。
func requiredPrincipalID(env func(string) string, name string, want uuid.UUID) (uuid.UUID, error) {
	v := env(name)
	if v == "" {
		return uuid.Nil, fmt.Errorf("%w: 环境变量 %s 缺失", ErrDemoSeedRefused, name)
	}
	got, err := uuid.Parse(v)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: 环境变量 %s 不是合法 UUID", ErrDemoSeedRefused, name)
	}
	if got != want {
		return uuid.Nil, fmt.Errorf("%w: 环境变量 %s 与 seed 权威固定 UUID 不一致（防漂移）", ErrDemoSeedRefused, name)
	}
	return got, nil
}

// loadPostgresSpec 解析演示 PostgreSQL 连接规格。
func loadPostgresSpec(env func(string) string) (ConnectionSpec, error) {
	pw := env("DEMO_PG_READER_PASSWORD")
	if pw == "" {
		return ConnectionSpec{}, fmt.Errorf("%w: 环境变量 DEMO_PG_READER_PASSWORD 缺失（演示 PostgreSQL 只读密码）", ErrDemoSeedRefused)
	}
	host, err := requiredEnv(env, "DEMO_PG_HOST")
	if err != nil {
		return ConnectionSpec{}, err
	}
	port, err := requiredPort(env, "DEMO_PG_PORT")
	if err != nil {
		return ConnectionSpec{}, err
	}
	name, err := requiredEnv(env, "DEMO_PG_NAME")
	if err != nil {
		return ConnectionSpec{}, err
	}
	return ConnectionSpec{
		ID:                 demoPGConnID,
		Name:               "demo_reader (PostgreSQL)",
		Engine:             metadata.EnginePostgreSQL,
		Host:               host,
		Port:               port,
		Database:           name,
		Environment:        metadata.EnvDevelopment,
		CredentialUser:     demoPGReaderRole, // init 脚本固定只读角色名
		CredentialPassword: pw,
	}, nil
}

// loadMySQLSpec 解析演示 MySQL 连接规格。
func loadMySQLSpec(env func(string) string) (ConnectionSpec, error) {
	pw := env("DEMO_MYSQL_READER_PASSWORD")
	if pw == "" {
		return ConnectionSpec{}, fmt.Errorf("%w: 环境变量 DEMO_MYSQL_READER_PASSWORD 缺失（演示 MySQL 只读密码）", ErrDemoSeedRefused)
	}
	host, err := requiredEnv(env, "DEMO_MYSQL_HOST")
	if err != nil {
		return ConnectionSpec{}, err
	}
	port, err := requiredPort(env, "DEMO_MYSQL_PORT")
	if err != nil {
		return ConnectionSpec{}, err
	}
	name, err := requiredEnv(env, "DEMO_MYSQL_NAME")
	if err != nil {
		return ConnectionSpec{}, err
	}
	user := env("DEMO_MYSQL_USER")
	if user == "" {
		user = demoMySQLReaderRole // 与 init 脚本默认一致
	}
	if user != demoMySQLReaderRole {
		// 与 PG 分支对称：演示连接必须使用 init 脚本创建的固定只读角色，
		// 拒绝部署方把 DEMO_MYSQL_USER 改为 root 等高权限账号（最小权限边界）。
		return ConnectionSpec{}, fmt.Errorf("%w: 环境变量 DEMO_MYSQL_USER 必须是固定只读角色 %s",
			ErrDemoSeedRefused, demoMySQLReaderRole)
	}
	return ConnectionSpec{
		ID:                 demoMySQLConnID,
		Name:               "demo_reader (MySQL)",
		Engine:             metadata.EngineMySQL,
		Host:               host,
		Port:               port,
		Database:           name,
		Environment:        metadata.EnvDevelopment,
		CredentialUser:     user,
		CredentialPassword: pw,
	}, nil
}

func requiredEnv(env func(string) string, name string) (string, error) {
	v := env(name)
	if v == "" {
		return "", fmt.Errorf("%w: 环境变量 %s 缺失", ErrDemoSeedRefused, name)
	}
	return v, nil
}

func requiredPort(env func(string) string, name string) (int, error) {
	v := env(name)
	if v == "" {
		return 0, fmt.Errorf("%w: 环境变量 %s 缺失", ErrDemoSeedRefused, name)
	}
	p, err := strconv.Atoi(v)
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("%w: 环境变量 %s 必须是 1-65535 的端口号", ErrDemoSeedRefused, name)
	}
	return p, nil
}
