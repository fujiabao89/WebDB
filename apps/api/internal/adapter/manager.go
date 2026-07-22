package adapter

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultMaxOpen     = 10
	defaultMaxIdle     = 2
	connAcquireTimeout = 5 * time.Second
	maxConnLTMin       = 27 * time.Minute
	maxConnLTMax       = 30 * time.Minute
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
	registry      *ContinuationRegistry
	opts          ManagerOptions
	currentRevs   map[string]int64
	currentCfgs   map[string]ConnectConfig
	closed        bool
	genCounter    int64
	cleanupCancel context.CancelFunc
}

func NewAdapterManager(opts ManagerOptions) *AdapterManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &AdapterManager{
		pools: make(map[string]*poolEntry), ac: newAdmissionController(),
		registry: newContinuationRegistry(), opts: opts,
		currentRevs: make(map[string]int64), currentCfgs: make(map[string]ConnectConfig),
		cleanupCancel: cancel,
	}
	go m.cleanupLoop(ctx)
	return m
}

func (m *AdapterManager) cleanupLoop(ctx context.Context) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.registry.cleanup()
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
	m.mu.Unlock()
	entry, err := m.createPool(ctx, cfg)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
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
	m.registry.close()
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
func (h *PoolHandle) Ping(ctx context.Context) error {
	if err := h.check(); err != nil {
		return err
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
func (h *PoolHandle) Schemas(ctx context.Context) ([]Schema, error) {
	if err := h.check(); err != nil {
		return nil, err
	}
	switch h.entry.cfg.Engine {
	case EnginePostgreSQL:
		return pgSchemas(ctx, h.entry.pgPool)
	case EngineMySQL:
		return mysqlSchemas(ctx, h.entry.sqlDB)
	default:
		return nil, newError(ErrUnsupportedEngine, "", nil)
	}
}
func (h *PoolHandle) Tables(ctx context.Context, schema string) ([]Table, error) {
	if err := h.check(); err != nil {
		return nil, err
	}
	switch h.entry.cfg.Engine {
	case EnginePostgreSQL:
		return pgTables(ctx, h.entry.pgPool, schema)
	case EngineMySQL:
		return mysqlTables(ctx, h.entry.sqlDB, schema)
	default:
		return nil, newError(ErrUnsupportedEngine, "", nil)
	}
}
func (h *PoolHandle) Columns(ctx context.Context, schema, table string) ([]Column, error) {
	if err := h.check(); err != nil {
		return nil, err
	}
	switch h.entry.cfg.Engine {
	case EnginePostgreSQL:
		return pgColumns(ctx, h.entry.pgPool, schema, table)
	case EngineMySQL:
		return mysqlColumns(ctx, h.entry.sqlDB, schema, table)
	default:
		return nil, newError(ErrUnsupportedEngine, "", nil)
	}
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
		req.MaxRows = req.PageSize
	}
	specs, err := buildSortSpecs(req.SortKeys)
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
	if result.NextToken != nil && result.TotalReturned < req.MaxRows {
		pv := extractLastValues(result.Rows, specs)
		plan := &PagePlan{SQL: req.SQL, Args: req.Args, SortKeys: req.SortKeys, LastSortValues: pv,
			PageSize: req.PageSize, MaxRows: req.MaxRows, CumulativeCount: result.TotalReturned,
			Scope: req.Scope, ConnectionID: h.entry.cfg.ConnectionID, Generation: h.gen}
		tok, err := h.entry.manager.registry.create(plan)
		if err != nil {
			return nil, err
		}
		result.NextToken = &tok
	}
	return result, nil
}
func (h *PoolHandle) NextPage(ctx context.Context, scope UserWorkspaceScope, token string) (*QueryResult, error) {
	if err := h.check(); err != nil {
		return nil, err
	}
	permit, err := h.entry.manager.ac.TryAcquire(scope.UserID, scope.WorkspaceID, h.entry.cfg.ConnectionID)
	if err != nil {
		return nil, err
	}
	defer permit.Release()
	plan, err := h.entry.manager.registry.claim(token)
	if err != nil {
		return nil, err
	}
	if plan.Scope.UserID != scope.UserID || plan.Scope.WorkspaceID != scope.WorkspaceID {
		h.entry.manager.registry.restore(token, plan)
		return nil, newError(ErrInvalidPageToken, "scope mismatch", nil)
	}
	if plan.ConnectionID != h.entry.cfg.ConnectionID || plan.Generation != h.gen {
		h.entry.manager.registry.restore(token, plan)
		return nil, newError(ErrInvalidPageToken, "token not for this pool", nil)
	}
	if plan.CumulativeCount >= plan.MaxRows {
		h.entry.manager.registry.expire(token)
		return &QueryResult{Rows: [][]any{}, TotalReturned: plan.CumulativeCount}, nil
	}
	specs, err := buildSortSpecs(plan.SortKeys)
	if err != nil {
		h.entry.manager.registry.restore(token, plan)
		return nil, err
	}
	limit := plan.PageSize
	rem := plan.MaxRows - plan.CumulativeCount
	if rem < limit {
		limit = rem
	}
	limit++
	sql, args, err := buildWrappedSQL(plan.SQL, specs, h.entry.cfg.Engine, plan.LastSortValues, plan.Args, limit)
	if err != nil {
		h.entry.manager.registry.restore(token, plan)
		return nil, err
	}
	result, err := h.execQuery(ctx, sql, args, limit, plan.PageSize, plan.CumulativeCount, plan.MaxRows)
	if err != nil {
		h.entry.manager.registry.restore(token, plan)
		return nil, err
	}
	plan.CumulativeCount = result.TotalReturned
	if result.NextToken == nil {
		h.entry.manager.registry.expire(token)
	} else {
		plan.LastSortValues = extractLastValues(result.Rows, specs)
		plan.inUse = false // 清除 claim 设置的 inUse，确保下一页可 claim
		newTok, _ := genToken()
		h.entry.manager.registry.replace(token, newTok, plan)
		result.NextToken = &newTok
	}
	return result, nil
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
	for i, n := range cn {
		dt := ""
		if i < len(cts) {
			dt = cts[i].DatabaseTypeName()
		}
		colInfos[i] = ColumnInfo{Name: n, DataType: dt}
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
func finalizeResult(colInfos []ColumnInfo, data [][]any, rc, effPage, cumCount, maxRows int) *QueryResult {
	hasMore := rc > effPage
	if hasMore {
		data = data[:effPage]
		rc = effPage
	}
	total := cumCount + rc
	result := &QueryResult{Columns: colInfos, Rows: data, ReturnedRows: rc, TotalReturned: total}
	if hasMore && total < maxRows {
		result.NextToken = &[]string{"_"}[0]
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
			if len(val) > maxCell {
				return nil, 0, newError(ErrResultTooLarge, "cell byte limit exceeded", nil)
			}
			b := make([]byte, len(val))
			copy(b, val)
			dst[i] = b
			total += len(b)
		case string:
			if len(val) > maxCell {
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
				if size > maxCell {
					return nil, 0, newError(ErrResultTooLarge, "cell byte limit exceeded", nil)
				}
				dst[i] = val
				total += size * 8
			default:
				dst[i] = val
				total += 64
			}
		}
	}
	return dst, total, nil
}
func extractLastValues(rows [][]any, specs []sortSpec) []any {
	if len(rows) == 0 {
		return nil
	}
	last := rows[len(rows)-1]
	vals := make([]any, len(specs)*2)
	for i := range specs {
		vals[i*2] = last[i] == nil
		vals[i*2+1] = last[i]
	}
	return vals
}
