package adapter

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fujiabao89/webdb/internal/queryplan"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultMaxOpen     = 10
	defaultMaxIdle     = 2
	connAcquireTimeout = 5 * time.Second
	maxConnLTMin       = 27 * time.Minute
	maxConnLTMax       = 30 * time.Minute

	// resolveQualifiedTablePG 沿完整 search_path 解析未限定表名到实际 schema：
	// to_regclass 使用与 PG 系统一致的名字解析（表位于后置 search_path 条目时也能
	// 解析到正确 schema，而非 current_schema() 返回的首项）。表不可见时返回空行。
	resolveQualifiedTablePG = `SELECT n.nspname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.oid = to_regclass($1)`
)

type poolEntry struct {
	cfg        ConnectConfig
	generation int64
	pgPool     *pgxpool.Pool
	sqlDB      *sql.DB
	draining   int32
	closed     int32
	createdAt  time.Time
	manager    *AdapterManager
}

func (e *poolEntry) isClosed() bool { return atomic.LoadInt32(&e.closed) == 1 }
func (e *poolEntry) drain()         { atomic.StoreInt32(&e.draining, 1) }
func (e *poolEntry) close() {
	atomic.StoreInt32(&e.closed, 1)
	if e.pgPool != nil {
		e.pgPool.Close()
	}
	if e.sqlDB != nil {
		e.sqlDB.Close()
	}
}

type AdapterManager struct {
	mu            sync.Mutex
	pools         map[string]*poolEntry
	ac            *AdmissionController
	opts          ManagerOptions
	currentRevs   map[string]int64
	currentCfgs   map[string]ConnectConfig
	closed        bool
	genCounter    int64
	cleanupCancel context.CancelFunc
	creating      sync.Map // per-cid singleflight: map[string]chan struct{}
}

func NewAdapterManager(opts ManagerOptions) *AdapterManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &AdapterManager{
		pools: make(map[string]*poolEntry), ac: newAdmissionController(),
		opts:          opts,
		currentRevs:   make(map[string]int64),
		currentCfgs:   make(map[string]ConnectConfig),
		creating:      sync.Map{},
		cleanupCancel: cancel,
	}
	// continuation registry 已迁移至服务层（ADR-015），Adapter 不再持有 token。
	go m.cleanupPoolLoop(ctx)
	return m
}

func (m *AdapterManager) cleanupPoolLoop(ctx context.Context) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// 连接池清理由 poolEntry.MaxConnLifetime / pgxpool 内部管理；
			// 本 ticker 仅保留以维持生命周期可扩展性。
		}
	}
}

func (m *AdapterManager) Get(ctx context.Context, cfg ConnectConfig) (*PoolHandle, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, newError(ErrPoolClosed, "manager closed", nil)
	}
	cfg.TLS = normalizeTLSMode(cfg.TLS)
	cid := cfg.ConnectionID
	cr := m.currentRevs[cid]
	if cfg.ConfigRevision < cr {
		m.mu.Unlock()
		return nil, newError(ErrStaleConfig, "stale config", nil)
	}
	if cfg.ConfigRevision == cr {
		if ex, ok := m.pools[cid]; ok && !ex.isClosed() {
			if cfg.compareConfig(m.currentCfgs[cid]) {
				m.mu.Unlock()
				return &PoolHandle{entry: ex, gen: ex.generation}, nil
			}
			m.mu.Unlock()
			return nil, newError(ErrConfigConflict, "config conflict", nil)
		}
	}
	// Singleflight: 若已有其他 goroutine 在创建同 cid 的池，等待其完成
	if ch, loaded := m.creating.LoadOrStore(cid, make(chan struct{})); loaded {
		m.mu.Unlock()
		select {
		case <-ch.(chan struct{}):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		// 重试：池应已存在
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, newError(ErrPoolClosed, "manager closed", nil)
		}
		cr = m.currentRevs[cid]
		if cfg.ConfigRevision < cr {
			m.mu.Unlock()
			return nil, newError(ErrStaleConfig, "stale config", nil)
		}
		// 调用方有更新的 ConfigRevision，需跳出重试循环重新创建池
		if cfg.ConfigRevision > cr {
			goto createPool
		}
		if ex, ok := m.pools[cid]; ok && !ex.isClosed() {
			if cfg.compareConfig(m.currentCfgs[cid]) {
				m.mu.Unlock()
				return &PoolHandle{entry: ex, gen: ex.generation}, nil
			}
		}
		m.mu.Unlock()
		return nil, newError(ErrConfigConflict, "config conflict after singleflight", nil)
	}
createPool:
	m.mu.Unlock()
	entry, err := m.createPool(ctx, cfg)
	if err != nil {
		m.mu.Lock()
		if ch, ok := m.creating.Load(cid); ok {
			close(ch.(chan struct{}))
			m.creating.Delete(cid)
		}
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Lock()
	if ch, ok := m.creating.Load(cid); ok {
		close(ch.(chan struct{}))
		m.creating.Delete(cid)
	}
	defer m.mu.Unlock()
	if m.closed {
		entry.close()
		return nil, newError(ErrPoolClosed, "manager closed", nil)
	}
	cr = m.currentRevs[cid]
	if cfg.ConfigRevision < cr {
		entry.close()
		return nil, newError(ErrStaleConfig, "stale config", nil)
	}
	if cfg.ConfigRevision == cr {
		if ex, ok := m.pools[cid]; ok && !ex.isClosed() {
			if cfg.compareConfig(m.currentCfgs[cid]) {
				entry.close()
				return &PoolHandle{entry: ex, gen: ex.generation}, nil
			}
			entry.close()
			return nil, newError(ErrConfigConflict, "config conflict", nil)
		}
		// 池尚不存在（零 Revision 首次注册）：继续注册新池
	}
	if old, ok := m.pools[cid]; ok && !old.isClosed() {
		old.drain()
		go old.close()
	}
	m.pools[cid] = entry
	m.currentRevs[cid] = cfg.ConfigRevision
	sanitized := cfg
	sanitized.Password = ""
	m.currentCfgs[cid] = sanitized
	return &PoolHandle{entry: entry, gen: entry.generation}, nil
}

func (m *AdapterManager) createPool(ctx context.Context, cfg ConnectConfig) (*poolEntry, error) {
	if cfg.Engine != EnginePostgreSQL && cfg.Engine != EngineMySQL {
		return nil, newError(ErrUnsupportedEngine, "unsupported engine", nil)
	}
	if cfg.TLS != TLSRequire && cfg.TLS != TLSPrefer && cfg.TLS != TLSDisable {
		return nil, newError(ErrInvalidConfig, "unknown TLS mode: "+string(cfg.TLS), nil)
	}
	if cfg.TLS == TLSPrefer {
		return nil, newError(ErrUnsupportedCapability, "TLS prefer not supported", nil)
	}
	if cfg.TLS == TLSDisable {
		if !m.opts.AllowInsecureLocalDemo {
			return nil, newError(ErrInvalidConfig, "TLS disable denied", nil)
		}
		if !isLocalHost(cfg.Host) {
			return nil, newError(ErrInvalidConfig, "TLS disable localhost only", nil)
		}
	}
	gen := atomic.AddInt64(&m.genCounter, 1)
	entry := &poolEntry{cfg: cfg, generation: gen, createdAt: time.Now(), manager: m}
	switch cfg.Engine {
	case EnginePostgreSQL:
		return m.createPG(ctx, cfg, entry)
	case EngineMySQL:
		return m.createMySQL(ctx, cfg, entry)
	default:
		return nil, newError(ErrUnsupportedEngine, "unsupported engine", nil)
	}
}

func isLocalHost(h string) bool {
	return h == "localhost" || h == "127.0.0.1" || h == "demo-pg" || h == "demo-mysql"
}

func (m *AdapterManager) createPG(ctx context.Context, cfg ConnectConfig, entry *poolEntry) (*poolEntry, error) {

	pc, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, wrapError(ErrConnectionFailed, err)
	}
	pc.ConnConfig.Host = cfg.Host
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, newError(ErrInvalidConfig, "port out of range", nil)
	}
	pc.ConnConfig.Port = uint16(cfg.Port)
	pc.ConnConfig.User = cfg.User
	pc.ConnConfig.Password = cfg.Password
	pc.ConnConfig.Database = cfg.Database
	if cfg.TLS == TLSRequire {
		pc.ConnConfig.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.Host}
	} else {
		pc.ConnConfig.TLSConfig = nil
	}
	pc.MaxConns = int32(normInt(cfg.MaxOpen, defaultMaxOpen))
	// pgxpool 无 MaxIdle 等价设置，空闲连接由 MaxConnIdleTime 控制回收
	pc.MinConns = 0
	pc.MaxConnLifetime = maxConnLT(time.Now().UnixNano())
	pc.MaxConnIdleTime = 5 * time.Minute
	pc.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, wrapError(ErrConnectionFailed, err)
	}
	pctx, cancel := context.WithTimeout(ctx, connAcquireTimeout)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, wrapError(ErrConnectionFailed, err)
	}
	entry.pgPool = pool
	return entry, nil
}

func (m *AdapterManager) createMySQL(ctx context.Context, cfg ConnectConfig, entry *poolEntry) (*poolEntry, error) {
	mc := mysql.NewConfig()
	mc.User, mc.Passwd, mc.Net = cfg.User, cfg.Password, "tcp"
	mc.Addr, mc.DBName = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), cfg.Database
	if cfg.TLS == TLSRequire {
		mc.TLSConfig = "true"
	} else {
		mc.AllowFallbackToPlaintext = true
	}
	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		return nil, wrapError(ErrConnectionFailed, err)
	}
	db.SetMaxOpenConns(normInt(cfg.MaxOpen, defaultMaxOpen))
	db.SetMaxIdleConns(normInt(cfg.MaxIdle, defaultMaxIdle))
	db.SetConnMaxLifetime(maxConnLT(time.Now().UnixNano()))
	pctx, cancel := context.WithTimeout(ctx, connAcquireTimeout)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		db.Close()
		return nil, wrapError(ErrConnectionFailed, err)
	}
	entry.sqlDB = db
	return entry, nil
}

func maxConnLT(seed int64) time.Duration {
	e := seed % int64(maxConnLTMax-maxConnLTMin)
	if e < 0 {
		e = -e
	}
	return maxConnLTMin + time.Duration(e)
}

func (m *AdapterManager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	entries := make([]*poolEntry, 0, len(m.pools))
	for _, e := range m.pools {
		entries = append(entries, e)
	}
	m.mu.Unlock()
	m.cleanupCancel()
	m.ac.close()
	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		go func(pe *poolEntry) { defer wg.Done(); pe.drain(); pe.close() }(e)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type PoolHandle struct {
	entry *poolEntry
	gen   int64
}

func (h *PoolHandle) check() error {
	if h.entry.isClosed() || atomic.LoadInt32(&h.entry.draining) == 1 {
		return newError(ErrPoolClosed, "pool closed or draining", nil)
	}
	return nil
}
func (h *PoolHandle) Release() {}

// PoolGeneration 返回连接池 generation（连接重建时递增；续页重新校验用）。
func (h *PoolHandle) PoolGeneration() int64 {
	if h == nil {
		return 0
	}
	return h.gen
}

// ResolveQualifiedTable 返回未限定表名实际解析到的可信 schema。
// PostgreSQL：经 to_regclass 沿完整 search_path 解析实际 relation 的命名空间。
// 仅用 current_schema() 会返回 search_path 首项存在的 schema，而 PG 对未限定表名
// 沿整个 search_path 解析（表可能在后置条目中，Codex P1）；MySQL：连接数据库。
// 带 connAcquireTimeout 超时；查询失败/空由调用方 fail-closed。
func (h *PoolHandle) ResolveQualifiedTable(ctx context.Context, table string) (string, error) {
	if err := h.check(); err != nil {
		return "", err
	}
	switch h.entry.cfg.Engine {
	case EngineMySQL:
		return h.entry.cfg.Database, nil
	case EnginePostgreSQL:
		var s string
		qctx, cancel := context.WithTimeout(ctx, connAcquireTimeout)
		defer cancel()
		// parser 提供的标识符按 PG 引号规则转义后传给 to_regclass：未限定带引号
		// 混合大小写表（如 "Users"）若不引号会被折叠为小写，导致解析失败或解析到
		// 错误的同名表（Codex P1）。
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		if err := h.entry.pgPool.QueryRow(qctx, resolveQualifiedTablePG, quoted).Scan(&s); err != nil {
			return "", mapAcquireError(err)
		}
		return s, nil
	default:
		return "", newError(ErrUnsupportedEngine, "", nil)
	}
}
func (h *PoolHandle) Ping(ctx context.Context) error {
	if err := h.check(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	if h.entry.pgPool != nil {
		err := h.entry.pgPool.Ping(ctx)
		if err != nil {
			return wrapError(ErrConnectionFailed, err)
		}
		return nil
	}
	if h.entry.sqlDB != nil {
		err := h.entry.sqlDB.PingContext(ctx)
		if err != nil {
			return wrapError(ErrConnectionFailed, err)
		}
		return nil
	}
	return newError(ErrPoolClosed, "no connection", nil)
}
func (h *PoolHandle) Schemas(ctx context.Context, scope UserWorkspaceScope, limit int) ([]Schema, error) {
	if err := h.check(); err != nil {
		return nil, err
	}
	// 与 Query/NextPage 一致：元数据浏览同样受用户/工作区/连接级并发准入约束，
	// 超限返回 ErrRateLimited（429），不得绕过并发边界（ADR-016）。
	permit, err := h.entry.manager.ac.TryAcquire(scope.UserID, scope.WorkspaceID, h.entry.cfg.ConnectionID)
	if err != nil {
		return nil, err
	}
	defer permit.Release()
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, connAcquireTimeout)
	defer cancel()
	limit = clampLimit(limit)
	switch h.entry.cfg.Engine {
	case EnginePostgreSQL:
		return pgSchemas(ctx, h.entry.pgPool, limit)
	case EngineMySQL:
		return mysqlSchemas(ctx, h.entry.sqlDB, limit)
	default:
		return nil, newError(ErrUnsupportedEngine, "", nil)
	}
}
func (h *PoolHandle) Tables(ctx context.Context, scope UserWorkspaceScope, schema string, limit int) ([]Table, error) {
	if err := h.check(); err != nil {
		return nil, err
	}
	permit, err := h.entry.manager.ac.TryAcquire(scope.UserID, scope.WorkspaceID, h.entry.cfg.ConnectionID)
	if err != nil {
		return nil, err
	}
	defer permit.Release()
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, connAcquireTimeout)
	defer cancel()
	limit = clampLimit(limit)
	switch h.entry.cfg.Engine {
	case EnginePostgreSQL:
		return pgTables(ctx, h.entry.pgPool, schema, limit)
	case EngineMySQL:
		return mysqlTables(ctx, h.entry.sqlDB, schema, limit)
	default:
		return nil, newError(ErrUnsupportedEngine, "", nil)
	}
}
func (h *PoolHandle) Columns(ctx context.Context, scope UserWorkspaceScope, schema, table string, limit int) ([]Column, error) {
	if err := h.check(); err != nil {
		return nil, err
	}
	permit, err := h.entry.manager.ac.TryAcquire(scope.UserID, scope.WorkspaceID, h.entry.cfg.ConnectionID)
	if err != nil {
		return nil, err
	}
	defer permit.Release()
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, connAcquireTimeout)
	defer cancel()
	limit = clampLimit(limit)
	switch h.entry.cfg.Engine {
	case EnginePostgreSQL:
		return pgColumns(ctx, h.entry.pgPool, schema, table, limit)
	case EngineMySQL:
		return mysqlColumns(ctx, h.entry.sqlDB, schema, table, limit)
	default:
		return nil, newError(ErrUnsupportedEngine, "", nil)
	}
}

// clampLimit 钳制元数据浏览 LIMIT：limit<=0 时强制为 1（F4）。
// PG 中 LIMIT -1 等价无限制、LIMIT 0 返回空，均会破坏集合有界性；
// 调用方（browse.Service）传 MaxEntries+1 sentinel，此处为纵深防御。
func clampLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	return limit
}
func (h *PoolHandle) Query(ctx context.Context, req FirstPageRequest) (*QueryResult, error) {
	if err := h.check(); err != nil {
		return nil, err
	}
	permit, err := h.entry.manager.ac.TryAcquire(req.Scope.UserID, req.Scope.WorkspaceID, h.entry.cfg.ConnectionID)
	if err != nil {
		return nil, err
	}
	defer permit.Release()
	if req.PageSize <= 0 {
		req.PageSize = 100
	}
	if req.PageSize > 500 {
		req.PageSize = 500
	}
	if req.MaxRows <= 0 {
		req.MaxRows = 500 // 默认最大行数，大于 PageSize 以允许续页
	}
	specs, singlePage, err := prepareFirstPageSort(req)
	if err != nil {
		return nil, err
	}
	limit := req.PageSize
	if limit > req.MaxRows {
		limit = req.MaxRows
	}
	limit++
	sql, args, err := buildWrappedSQL(req.SQL, specs, h.entry.cfg.Engine, nil, req.Args, limit)
	if err != nil {
		return nil, err
	}
	result, err := h.execQuery(ctx, sql, args, limit, req.PageSize, 0, req.MaxRows)
	if err != nil {
		return nil, err
	}
	if singlePage {
		result.HasMore = false
	}
	// ADR-015：Adapter 不再生成/保存 continuation token；HasMore 交由服务层决定是否续页。
	return result, nil
}

func prepareFirstPageSort(req FirstPageRequest) ([]sortSpec, bool, error) {
	if req.SortPlan == nil {
		if req.MaxRows > req.PageSize {
			return nil, false, newError(
				ErrUnsupportedQuery,
				"verified sort plan required when max rows exceed page size",
				nil,
			)
		}
		return nil, true, nil
	}
	specs, err := sortSpecsFromPlan(req.SortPlan)
	if err != nil {
		return nil, false, err
	}
	return specs, false, nil
}

// NextPage 使用不可伪造的 VerifiedNextPagePlan 执行续页（ADR-015）。
// SQL/Args/SortSpecs/last values 均来自服务层恢复的 ContinuationState，
// 不接受客户端重新提交；Adapter 不生成/解析/保存 token。
func (h *PoolHandle) NextPage(ctx context.Context, scope UserWorkspaceScope, plan queryplan.VerifiedNextPagePlan) (*QueryResult, error) {
	if err := h.check(); err != nil {
		return nil, err
	}
	if !queryplan.IsValidVerifiedNextPagePlan(plan) {
		return nil, newError(ErrInvalidPageToken, "invalid verified next page plan", nil)
	}
	permit, err := h.entry.manager.ac.TryAcquire(scope.UserID, scope.WorkspaceID, h.entry.cfg.ConnectionID)
	if err != nil {
		return nil, err
	}
	defer permit.Release()
	specs, err := sortSpecsFromSpecs(plan.SortSpecs())
	if err != nil {
		return nil, err
	}
	limit := plan.PageSize()
	rem := plan.MaxRows() - plan.CumulativeCount()
	if rem < limit {
		limit = rem
	}
	limit++
	sql, args, err := buildWrappedSQL(plan.SQL(), specs, h.entry.cfg.Engine, plan.LastSortValues(), plan.Args(), limit)
	if err != nil {
		return nil, err
	}
	return h.execQuery(ctx, sql, args, limit, plan.PageSize(), plan.CumulativeCount(), plan.MaxRows())
}
func (h *PoolHandle) Stats() PoolStats {
	if h.entry.pgPool != nil {
		s := h.entry.pgPool.Stat()
		return PoolStats{ActiveConns: s.AcquiredConns(), IdleConns: s.IdleConns(), MaxOpen: int(s.MaxConns())}
	}
	if h.entry.sqlDB != nil {
		st := h.entry.sqlDB.Stats()
		return PoolStats{ActiveConns: int32(st.InUse), IdleConns: int32(st.Idle), MaxOpen: st.MaxOpenConnections}
	}
	return PoolStats{}
}

func (h *PoolHandle) execQuery(ctx context.Context, sql string, args []any, limit int, pageSize int, cumCount int, maxRows int) (*QueryResult, error) {
	mpb := normInt(h.entry.cfg.MaxPageBytes, defaultMaxPageBytes)
	if mpb > 16<<20 {
		mpb = 16 << 20
	}
	mcb := normInt(h.entry.cfg.MaxCellBytes, defaultMaxCellBytes)
	if mcb > 2<<20 {
		mcb = 2 << 20
	}
	effPage := pageSize
	rem := maxRows - cumCount
	if rem < effPage {
		effPage = rem
	}
	mf := effPage + 1
	if h.entry.pgPool != nil {
		return h.execPG(ctx, sql, args, mf, effPage, cumCount, mpb, mcb, maxRows)
	}
	if h.entry.sqlDB != nil {
		return h.execMySQL(ctx, sql, args, mf, effPage, cumCount, mpb, mcb, maxRows)
	}
	return nil, newError(ErrPoolClosed, "no connection", nil)
}
func mapExecError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return newError(ErrQueryTimeout, "query exceeded deadline", err)
	}
	if errors.Is(err, context.Canceled) {
		return newError(ErrQueryCanceled, "query cancelled", err)
	}
	return wrapError(ErrDatabaseError, err)
}

// mapAcquireError 映射连接获取错误：DeadlineExceeded 视为池耗尽而非查询超时。
func mapAcquireError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return newError(ErrConnPoolExhausted, "pool exhausted", err)
	}
	if errors.Is(err, context.Canceled) {
		return newError(ErrQueryCanceled, "acquire cancelled", err)
	}
	return wrapError(ErrDatabaseError, err)
}

func (h *PoolHandle) execPG(ctx context.Context, sql string, args []any, maxFetch, effPage, cumCount, maxPage, maxCell, maxRows int) (*QueryResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithTimeout(ctx, 30*time.Second)
		defer deadlineCancel()
	}
	aCtx, cancel := context.WithTimeout(ctx, connAcquireTimeout)
	defer cancel()
	conn, err := h.entry.pgPool.Acquire(aCtx)
	if err != nil {
		return nil, mapAcquireError(err)
	}
	defer conn.Release()
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapExecError(err)
	}
	defer rows.Close()
	cols := rows.FieldDescriptions()
	colInfos := make([]ColumnInfo, len(cols))
	for i, c := range cols {
		colInfos[i] = ColumnInfo{Name: string(c.Name), DataType: fmt.Sprintf("%d", c.DataTypeOID)}
	}
	var data [][]any
	rc := 0
	pb := 0
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, mapExecError(err)
		}
		// 预读行：只计数不拷贝，finalizeResult 会丢弃该行
		if rc >= effPage {
			rc++
			break
		}
		dd, cb, err := copyAndMeasure(vals, maxCell)
		if err != nil {
			return nil, err
		}
		pb += cb
		if pb > maxPage {
			return nil, newError(ErrResultTooLarge, "page byte limit exceeded", nil)
		}
		data = append(data, dd)
		rc++
		if rc >= maxFetch {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, mapExecError(err)
	}
	return finalizeResult(colInfos, data, rc, effPage, cumCount, maxRows), nil
}
func (h *PoolHandle) execMySQL(ctx context.Context, sql string, args []any, maxFetch, effPage, cumCount, maxPage, maxCell, maxRows int) (*QueryResult, error) {
	aCtx, cancel := context.WithTimeout(ctx, connAcquireTimeout)
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithTimeout(ctx, 30*time.Second)
		defer deadlineCancel()
	}
	conn, err := h.entry.sqlDB.Conn(aCtx)
	if err != nil {
		return nil, mapAcquireError(err)
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, mapExecError(err)
	}
	defer rows.Close()
	cn, err := rows.Columns()
	if err != nil {
		return nil, mapExecError(err)
	}
	cts, _ := rows.ColumnTypes()
	colInfos := make([]ColumnInfo, len(cn))
	// textCols 依据驱动列元数据 DatabaseTypeName() 判定文本列，
	// 不使用运行时值是否为 []byte 猜测文本/二进制。
	textCols := make([]bool, len(cn))
	for i, n := range cn {
		dt := ""
		if i < len(cts) {
			dt = cts[i].DatabaseTypeName()
		}
		colInfos[i] = ColumnInfo{Name: n, DataType: dt}
		textCols[i] = isMySQLTextColumn(dt)
	}
	var data [][]any
	rc := 0
	pb := 0
	for rows.Next() {
		vals := make([]any, len(cn))
		ptrs := make([]any, len(cn))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, mapExecError(err)
		}
		// stepRow 先判定预读行（rc >= effPage）再做文本规范化：
		// 预读哨兵行只计数不复制，finalizeResult 会丢弃该行，
		// 避免超大文本在丢弃前被完整复制；仅对进入 data 的行规范化。
		step, err := stepRow(vals, textCols, rc, effPage, maxCell)
		if err != nil {
			return nil, err
		}
		rc = step.nextRC
		if step.readAhead {
			break
		}
		pb += step.added
		if pb > maxPage {
			return nil, newError(ErrResultTooLarge, "page byte limit exceeded", nil)
		}
		data = append(data, step.dataRow)
		if rc >= maxFetch {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, mapExecError(err)
	}
	return finalizeResult(colInfos, data, rc, effPage, cumCount, maxRows), nil
}
func finalizeResult(colInfos []ColumnInfo, data [][]any, rc, effPage, cumCount, maxRows int) *QueryResult {
	hasMore := rc > effPage
	if hasMore {
		data = data[:effPage]
		rc = effPage
	}
	total := cumCount + rc
	result := &QueryResult{Columns: colInfos, Rows: data, ReturnedRows: rc, TotalReturned: total}
	if hasMore && total < maxRows {
		result.HasMore = true
	}
	return result
}

func copyAndMeasure(vals []any, maxCell int) ([]any, int, error) {
	dst := make([]any, len(vals))
	total := 0
	for i, v := range vals {
		if v == nil {
			dst[i] = nil
			continue
		}
		switch val := v.(type) {
		case []byte:
			if maxCell > 0 && len(val) > maxCell {
				return nil, 0, newError(ErrResultTooLarge, "cell byte limit exceeded", nil)
			}
			b := make([]byte, len(val))
			copy(b, val)
			dst[i] = b
			total += len(b)
		case string:
			if maxCell > 0 && len(val) > maxCell {
				return nil, 0, newError(ErrResultTooLarge, "cell byte limit exceeded", nil)
			}
			dst[i] = val
			total += len(val)
		case float64:
			dst[i] = val
			total += 8
		case int64:
			dst[i] = val
			total += 8
		case bool:
			dst[i] = val
			total += 1
		case time.Time:
			dst[i] = val
			total += 32
		case int32:
			dst[i] = val
			total += 4
		default:
			rv := reflect.ValueOf(v)
			switch rv.Kind() {
			case reflect.Slice, reflect.Array:
				size := rv.Len()
				dst[i] = val
				var byteSize int
				if rv.Type().Elem().Kind() == reflect.String {
					for j := 0; j < size; j++ {
						elem := rv.Index(j)
						if elem.Kind() == reflect.String {
							s := elem.String()
							byteSize += len(s)
							if maxCell > 0 && len(s) > maxCell {
								return nil, 0, newError(ErrResultTooLarge, "cell byte limit exceeded", nil)
							}
						} else {
							byteSize += int(elem.Type().Size())
							if maxCell > 0 && byteSize > maxCell {
								return nil, 0, newError(ErrResultTooLarge, "cell byte limit exceeded", nil)
							}
						}
					}
				} else {
					elemSize := int(rv.Type().Elem().Size())
					if elemSize < 8 {
						elemSize = 8
					}
					byteSize = size * elemSize
				}
				if maxCell > 0 && byteSize > maxCell {
					return nil, 0, newError(ErrResultTooLarge, "cell byte limit exceeded", nil)
				}
				total += byteSize
			default:
				dst[i] = val
				total += 64
			}
		}
	}
	return dst, total, nil
}

// rowStep 描述 execMySQL 中单行扫描值的处理结果。
type rowStep struct {
	readAhead bool  // 预读哨兵行：只计数、不加入 data
	added     int   // 该行加入 data 的字节数（readAhead 时为 0）
	nextRC    int   // 处理后的行计数
	dataRow   []any // 规范化+防御性复制后的行（readAhead 时为 nil）
}

// stepRow 对单行扫描值执行“预读判定 → 文本规范化 → 字节计数”。
// 预读判定必须先于文本规范化：页面已达 effPage 时，当前行只是探测“还有下一行”
// 的哨兵行，finalizeResult 会丢弃它；若先规范化文本列，超大文本会在丢弃前被
// 完整复制成 string，造成不必要的峰值内存。仅对实际进入 data 的行做
// []byte→string 转换与超限检查。
func stepRow(vals []any, textCols []bool, rc, effPage, maxCell int) (rowStep, error) {
	if rc >= effPage {
		return rowStep{readAhead: true, nextRC: rc + 1}, nil
	}
	if err := normalizeTextCols(vals, textCols, maxCell); err != nil {
		return rowStep{}, err
	}
	dd, cb, err := copyAndMeasure(vals, maxCell)
	if err != nil {
		return rowStep{}, err
	}
	return rowStep{dataRow: dd, added: cb, nextRC: rc + 1}, nil
}

// normalizeTextCols 仅对元数据判定的文本列把 []byte 转 string；
// 二进制/未知列保持 []byte，防御性复制由 copyAndMeasure 完成。
// 转换前先按 maxCell 拒绝超限单元格，避免超大文本先被完整复制、
// 再由 copyAndMeasure 拒绝；MaxCellBytes 语义不变（ErrResultTooLarge）。
func normalizeTextCols(vals []any, textCols []bool, maxCell int) error {
	for i, v := range vals {
		if b, ok := v.([]byte); ok && textCols[i] {
			if maxCell > 0 && len(b) > maxCell {
				return newError(ErrResultTooLarge, "cell byte limit exceeded", nil)
			}
			vals[i] = string(b)
		}
	}
	return nil
}

// ExtractLastValues 从结果末行提取 last sort values（[isNull0, val0, isNull1, val1, ...]）。
// 供服务层在首页/续页后构造 ContinuationState 使用（ADR-015）。
func ExtractLastValues(rows [][]any, colInfos []ColumnInfo, specs []queryplan.SortSpec) ([]any, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	colIdx := make(map[string]int, len(colInfos))
	for i, c := range colInfos {
		if _, exists := colIdx[c.Name]; exists {
			return nil, newError(ErrDatabaseError, "duplicate column name: "+c.Name, nil)
		}
		colIdx[c.Name] = i
	}
	last := rows[len(rows)-1]
	vals := make([]any, len(specs)*2)
	for i, s := range specs {
		pos, ok := colIdx[s.Column]
		if !ok {
			return nil, newError(ErrDatabaseError, "sort column not in result: "+s.Column, nil)
		}
		vals[i*2] = last[pos] == nil
		vals[i*2+1] = last[pos]
	}
	return vals, nil
}
