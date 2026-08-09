package browse

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- 窄接口依赖（测试替身 / 生产仓储均可注入） --------------------------------

// MemberReader 仅暴露授权所需的工作区成员资格检查能力。
type MemberReader interface {
	MemberByWorkspaceAndUser(ctx context.Context, wsID, userID uuid.UUID) (*metadata.WorkspaceMember, error)
}

// ConnectionReader 仅暴露授权所需的连接读取能力。
type ConnectionReader interface {
	ConnectionByID(ctx context.Context, wsID, id uuid.UUID) (*metadata.Connection, error)
	// ListConnectionsAllowed 列出工作区内 AllowRead=true 的连接，limit 保证集合有界
	// （生产实现 PGStore.ListConnectionsAllowed：JOIN connection_policies + LIMIT）。
	ListConnectionsAllowed(ctx context.Context, wsID uuid.UUID, limit int) ([]metadata.Connection, error)
}

// PolicyReader 仅暴露授权所需的连接策略读取能力。
type PolicyReader interface {
	PolicyByConnection(ctx context.Context, wsID, connID uuid.UUID) (*metadata.ConnectionPolicy, error)
}

// MetadataBrowser 授权通过后访问目标库元数据的最小契约。
// 生产实现包装 adapter.AdapterManager.Get + PoolHandle.Schemas/Tables/Columns，
// 并保证 handle.Release 归还连接。limit 为服务端有界上限（MaxEntries+1 sentinel），
// 必须下传查询层参数化 LIMIT 或在迭代到 sentinel 行时停止，不能先累积完整结果再拒绝
// （WEB-36 P1：元数据条目上限不得在无界检索之后才执行）。
type MetadataBrowser interface {
	Schemas(ctx context.Context, cfg adapter.ConnectConfig, limit int) ([]adapter.Schema, error)
	Tables(ctx context.Context, cfg adapter.ConnectConfig, schema string, limit int) ([]adapter.Table, error)
	Columns(ctx context.Context, cfg adapter.ConnectConfig, schema, table string, limit int) ([]adapter.Column, error)
}

// Limits 浏览响应硬上限（P0-06A §6/§7，D06c 已批准：连接 200、浏览层级 1000）。
type Limits struct {
	MaxConnections int
	MaxEntries     int
}

// DefaultLimits 返回 Owner 批准的默认上限。
func DefaultLimits() Limits {
	return Limits{MaxConnections: 200, MaxEntries: 1000}
}

// boundedSentinel 返回 max+1 作为 sentinel，使查询层能区分"恰好等于上限"
// 与"超过上限"：LIMIT max+1 在恰好 max 条时返回 max 条（正常），在多于
// max 条时返回 max+1 条（超限）。防御整型溢出：max<0 或已为 int 最大值时
// 原样返回，避免 +1 回绕成负值传入 LIMIT。MaxEntries 在 NewService 已归一
// 化为正配置，此处为纵深防御。
func boundedSentinel(max int) int {
	if max < 0 || max == int(^uint(0)>>1) {
		return max
	}
	return max + 1
}

// MaxResponseBytes 响应体字节上限（P0-06A §6/§7，D06b 已批准）。
const MaxResponseBytes = 8 << 20

// checkResponseBudget 将响应 DTO 序列化并检查字节预算（F2，方案 A）。
// 序列化后超过 8 MiB 返回 result_too_large（不静默截断）；序列化失败视为内部错误。
func checkResponseBudget(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w", ErrInternalError)
	}
	if len(b) > MaxResponseBytes {
		return fmt.Errorf("%w", ErrResultTooLarge)
	}
	return nil
}

// Service 授权读取服务（P0-06D）。零 net/http 依赖；HTTP 层由 browsehttp 提供。
type Service struct {
	members  MemberReader
	conns    ConnectionReader
	policies PolicyReader
	resolver credentials.CredentialResolver
	browser  MetadataBrowser
	limits   Limits
	tlsMode  adapter.TLSMode // 服务端派生（非客户端输入），默认 TLSRequire
	logger   *slog.Logger
}

// NewService 创建授权读取服务。
func NewService(
	members MemberReader,
	conns ConnectionReader,
	policies PolicyReader,
	resolver credentials.CredentialResolver,
	browser MetadataBrowser,
	limits Limits,
) *Service {
	if limits.MaxConnections <= 0 {
		limits.MaxConnections = 200
	}
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = 1000
	}
	return &Service{
		members:  members,
		conns:    conns,
		policies: policies,
		resolver: resolver,
		browser:  browser,
		limits:   limits,
		tlsMode:  adapter.TLSRequire,
		logger:   slog.Default(),
	}
}

// ListConnections 返回已授权连接的安全 DTO 列表（P0-06A §6）。
// 列表访问要求成员资格（任意可读角色）；仅 AllowRead=true 的连接出现在列表
// （WEB-36：缺失或拒绝策略的连接不出现）。过滤下沉到 SQL（ListConnectionsAllowed，
// JOIN connection_policies WHERE allow_read=true），LIMIT MaxConnections+1 保证集合有界；
// 超过上限返回 result_too_large（不静默截断）。不写 AuditEvent（D05b）。
func (s *Service) ListConnections(ctx context.Context, p Principal) ([]ConnectionDTO, error) {
	if _, err := s.authorizeMember(ctx, p); err != nil {
		return nil, err
	}
	if s.conns == nil {
		return nil, fmt.Errorf("%w", ErrInternalError)
	}
	conns, err := s.conns.ListConnectionsAllowed(ctx, p.WorkspaceID, s.limits.MaxConnections+1)
	if err != nil {
		code := mapStoreError(err)
		s.logStorageFailure("list_connections", p.WorkspaceID, uuid.Nil, err)
		return nil, fmt.Errorf("%w: list connections failed", code)
	}
	if len(conns) > s.limits.MaxConnections {
		return nil, fmt.Errorf("%w", ErrResultTooLarge)
	}
	out := make([]ConnectionDTO, 0, len(conns))
	for _, c := range conns {
		out = append(out, ConnectionDTO{
			ID:          c.ID,
			Name:        c.Name,
			Engine:      string(c.Engine),
			Environment: string(c.Environment),
			Database:    c.Database,
		})
	}
	if err := checkResponseBudget(out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListSchemas 列出已授权连接的 Schema（P0-06A §7）。catalog 由连接 database 派生。
func (s *Service) ListSchemas(ctx context.Context, p Principal, connID uuid.UUID) ([]SchemaDTO, error) {
	conn, err := s.authorizeRead(ctx, p, connID)
	if err != nil {
		return nil, err
	}
	cfg, err := s.resolveCfg(ctx, conn)
	if err != nil {
		return nil, err
	}
	if s.browser == nil {
		return nil, fmt.Errorf("%w", ErrInternalError)
	}
	schemas, err := s.browser.Schemas(ctx, cfg, boundedSentinel(s.limits.MaxEntries))
	if err != nil {
		return nil, fmt.Errorf("%w", mapAdapterError(err))
	}
	if len(schemas) > s.limits.MaxEntries {
		return nil, fmt.Errorf("%w", ErrResultTooLarge)
	}
	out := make([]SchemaDTO, 0, len(schemas))
	for _, sc := range schemas {
		out = append(out, SchemaDTO{Name: sc.Name, Catalog: conn.Database})
	}
	if err := checkResponseBudget(out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListTables 列出指定 Schema 下的表/视图（P0-06A §7）。重新授权，不信任前一步状态。
func (s *Service) ListTables(ctx context.Context, p Principal, connID uuid.UUID, schema string) ([]TableDTO, error) {
	if !ValidIdent(schema) {
		return nil, fmt.Errorf("%w", ErrInvalidScope)
	}
	conn, err := s.authorizeRead(ctx, p, connID)
	if err != nil {
		return nil, err
	}
	cfg, err := s.resolveCfg(ctx, conn)
	if err != nil {
		return nil, err
	}
	if s.browser == nil {
		return nil, fmt.Errorf("%w", ErrInternalError)
	}
	tables, err := s.browser.Tables(ctx, cfg, schema, boundedSentinel(s.limits.MaxEntries))
	if err != nil {
		return nil, fmt.Errorf("%w", mapAdapterError(err))
	}
	if len(tables) > s.limits.MaxEntries {
		return nil, fmt.Errorf("%w", ErrResultTooLarge)
	}
	out := make([]TableDTO, 0, len(tables))
	for _, tb := range tables {
		out = append(out, TableDTO{Schema: tb.Schema, Name: tb.Name, Type: string(tb.Type)})
	}
	if err := checkResponseBudget(out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListColumns 列出指定 Schema/Table 的列（P0-06A §7）。重新授权，不信任前一步状态。
func (s *Service) ListColumns(ctx context.Context, p Principal, connID uuid.UUID, schema, table string) ([]ColumnDTO, error) {
	if !ValidIdent(schema) || !ValidIdent(table) {
		return nil, fmt.Errorf("%w", ErrInvalidScope)
	}
	conn, err := s.authorizeRead(ctx, p, connID)
	if err != nil {
		return nil, err
	}
	cfg, err := s.resolveCfg(ctx, conn)
	if err != nil {
		return nil, err
	}
	if s.browser == nil {
		return nil, fmt.Errorf("%w", ErrInternalError)
	}
	cols, err := s.browser.Columns(ctx, cfg, schema, table, boundedSentinel(s.limits.MaxEntries))
	if err != nil {
		return nil, fmt.Errorf("%w", mapAdapterError(err))
	}
	if len(cols) > s.limits.MaxEntries {
		return nil, fmt.Errorf("%w", ErrResultTooLarge)
	}
	out := make([]ColumnDTO, 0, len(cols))
	for _, c := range cols {
		out = append(out, ColumnDTO{Name: c.Name, Ordinal: c.Ordinal, NativeType: c.NativeType, Nullable: c.Nullable, HasDefault: c.HasDefault})
	}
	if err := checkResponseBudget(out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- 授权编排（顺序固定：Principal → 成员 → 连接归属 → 策略） -----------------

// authorizeMember 校验可信 Principal 与 active 成员/角色（可读角色）。
// 存储故障与真正的权限拒绝区分：根因仅进服务端日志（脱敏），返回稳定错误码。
func (s *Service) authorizeMember(ctx context.Context, p Principal) (*metadata.WorkspaceMember, error) {
	if !p.valid() {
		return nil, fmt.Errorf("%w", ErrUnauthorized)
	}
	if s.members == nil {
		return nil, fmt.Errorf("%w", ErrInternalError)
	}
	member, err := s.members.MemberByWorkspaceAndUser(ctx, p.WorkspaceID, p.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w", ErrForbidden) // inactive 或非成员
		}
		code := mapStoreError(err)
		s.logStorageFailure("authorize.member", p.WorkspaceID, uuid.Nil, err)
		return nil, fmt.Errorf("%w", code)
	}
	if member == nil || !member.Role.CanRead() {
		return nil, fmt.Errorf("%w", ErrForbidden) // 未知/空角色拒绝
	}
	return member, nil
}

// authorizeRead 校验成员、连接归属与 ConnectionPolicy.AllowRead。
// 门禁全部通过后才允许调用方解析凭证/访问目标库。
func (s *Service) authorizeRead(ctx context.Context, p Principal, connID uuid.UUID) (*metadata.Connection, error) {
	if _, err := s.authorizeMember(ctx, p); err != nil {
		return nil, err
	}
	if s.conns == nil || s.policies == nil {
		return nil, fmt.Errorf("%w", ErrInternalError)
	}
	conn, err := s.conns.ConnectionByID(ctx, p.WorkspaceID, connID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w", ErrConnectionNotFound) // 不存在/跨工作区/不可见统一
		}
		code := mapStoreError(err)
		s.logStorageFailure("authorize.connection", p.WorkspaceID, connID, err)
		return nil, fmt.Errorf("%w", code)
	}
	policy, err := s.policies.PolicyByConnection(ctx, p.WorkspaceID, connID)
	if err != nil {
		code := mapStoreError(err)
		s.logStorageFailure("authorize.policy", p.WorkspaceID, connID, err)
		return nil, fmt.Errorf("%w", code)
	}
	// 生产仓储对缺失策略返回 (nil, nil)（PGStore.PolicyByConnection），
	// 显式判空映射 policy_not_configured（404 防枚举），不允许回退到宽松默认。
	if policy == nil {
		return nil, fmt.Errorf("%w", ErrPolicyNotConfigured)
	}
	if !allowRead(policy) {
		return nil, fmt.Errorf("%w", ErrReadNotAllowed)
	}
	return conn, nil
}

// resolveCfg 在授权通过后解析凭证并构造 Adapter ConnectConfig（阶段 C'）。
// 凭证/KEK/pool/config 内部故障一律折叠为 ErrConnectionUnavailable（D15a），不外泄内部码。
func (s *Service) resolveCfg(ctx context.Context, conn *metadata.Connection) (adapter.ConnectConfig, error) {
	if s.resolver == nil {
		return adapter.ConnectConfig{}, fmt.Errorf("%w", ErrInternalError)
	}
	payload, err := s.resolver.ResolveCredential(ctx, conn.WorkspaceID, conn.SecretRef, conn.SecretVersion)
	if err != nil {
		s.logStorageFailure("resolve_credential", conn.WorkspaceID, conn.ID, err)
		return adapter.ConnectConfig{}, fmt.Errorf("%w", ErrConnectionUnavailable)
	}
	revision, err := connectionConfigRevision(conn)
	if err != nil {
		s.logStorageFailure("resolve_config_revision", conn.WorkspaceID, conn.ID, err)
		return adapter.ConnectConfig{}, fmt.Errorf("%w", ErrConnectionUnavailable)
	}
	return adapter.ConnectConfig{
		ConnectionID:   conn.ID.String(),
		SecretVersion:  conn.SecretVersion,
		ConfigRevision: revision,
		Engine:         adapter.Engine(conn.Engine),
		Host:           conn.Host,
		Port:           conn.Port,
		User:           payload.User,
		Password:       payload.Password,
		Database:       conn.Database,
		TLS:            s.tlsMode,
	}, nil
}

// connectionConfigRevision 派生 AdapterManager 的配置修订号（与 execution 对齐，
// P0-06A §15：复用 execution 的 ConfigRevision 语义，避免与执行管线共享池时
// ConfigRevision=0 被判定 stale 而恒 503）。updated_at 缺失/非法时 fail-closed。
func connectionConfigRevision(conn *metadata.Connection) (int64, error) {
	if conn == nil || conn.UpdatedAt.IsZero() {
		return 0, fmt.Errorf("connection updated_at is required")
	}
	revision := conn.UpdatedAt.UnixMicro()
	if revision <= 0 {
		return 0, fmt.Errorf("connection updated_at must be after unix epoch")
	}
	return revision, nil
}

// allowRead 判定策略允许读：策略非空且 AllowRead 显式为 true。
// nil AllowRead 默认拒绝（不信任 DB 默认值回退）。
func allowRead(p *metadata.ConnectionPolicy) bool {
	return p != nil && p.AllowRead != nil && *p.AllowRead
}

// logStorageFailure 记录底层故障根因（服务端日志），对错误消息统一脱敏。
func (s *Service) logStorageFailure(op string, wsID, connID uuid.UUID, err error) {
	s.logger.Error("browse service failure",
		"op", op,
		"workspace_id", wsID.String(),
		"connection_id", connID.String(),
		"error", metadata.RedactSensitive(err.Error()))
}

// ---- 错误映射 ---------------------------------------------------------------

// mapStoreError 映射元数据库存储错误：超时/取消保持原语义，其余折叠 internal_error。
func mapStoreError(err error) StableErrorCode {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrQueryTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrQueryCancelled
	}
	return ErrInternalError
}

// mapAdapterError 映射 Adapter/目标库错误到稳定码（P0-06A §12）。
// 先按 Adapter 稳定码映射，再退化为 context 语义，避免带 cause 的错误链
// （如池耗尽包装 deadline）被 context 判定抢先掩盖真实语义（P2-4）；
// 凭证/pool/config 类内部故障折叠为 connection_unavailable；不泄露原始错误。
func mapAdapterError(err error) StableErrorCode {
	var ae *adapter.AdapterError
	if errors.As(err, &ae) {
		switch ae.Code {
		case adapter.ErrRateLimited:
			return ErrRateLimited
		case adapter.ErrConnPoolExhausted:
			return ErrConnectionBusy
		case adapter.ErrQueryTimeout:
			return ErrQueryTimeout
		case adapter.ErrQueryCanceled:
			return ErrQueryCancelled
		case adapter.ErrResultTooLarge:
			return ErrResultTooLarge
		case adapter.ErrConnectionFailed, adapter.ErrStaleConfig,
			adapter.ErrConfigConflict, adapter.ErrPoolClosed,
			adapter.ErrInvalidConfig, adapter.ErrUnsupportedEngine:
			return ErrConnectionUnavailable
		case adapter.ErrDatabaseError:
			// 目标库 rows.Err()/rows.Scan 会把取消/超时包装进 cause（WrapDatabaseError
			// 保留 cause 链）。先识别 context 语义，再退化为通用 database_error，
			// 否则流式读取中的取消会被误报为 500（WEB-36 P1）。connection_busy
			// 等其余 Adapter 码仍保持先于 context 判定（P2-4）。
			if errors.Is(err, context.DeadlineExceeded) {
				return ErrQueryTimeout
			}
			if errors.Is(err, context.Canceled) {
				return ErrQueryCancelled
			}
			return ErrDatabaseError
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrQueryTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrQueryCancelled
	}
	return ErrInternalError
}
