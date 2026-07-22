package adapter

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
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
		if cfg.compareConfig(m.currentCfgs[cid]) {
			entry.close()
			return &PoolHandle{entry: m.pools[cid], gen: m.pools[cid].generation}, nil
		}
		entry.close()
		return nil, newError(ErrConfigConflict, "config conflict", nil)
	}
	if old, ok := m.pools[cid]; ok && !old.isClosed() {
		old.drain()
	}
	m.pools[cid] = entry
	m.currentRevs[cid] = cfg.ConfigRevision
	m.currentCfgs[cid] = cfg
	return &PoolHandle{entry: entry, gen: entry.generation}, nil
}

func (m *AdapterManager) createPool(ctx context.Context, cfg ConnectConfig) (*poolEntry, error) {
	if cfg.Engine != EnginePostgreSQL && cfg.Engine != EngineMySQL {
		return nil, newError(ErrUnsupportedEngine, "unsupported engine", nil)
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
	entry := &poolEntry{cfg: cfg, generation: gen, createdAt: time.Now()}
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
	cs := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)
	pc, err := pgxpool.ParseConfig(cs)
	if err != nil {
		return nil, wrapError(ErrConnectionFailed, err)
	}
	pc.MaxConns = int32(normInt(cfg.MaxOpen, defaultMaxOpen))
	pc.MinConns = int32(normInt(cfg.MaxIdle, defaultMaxIdle))
	pc.MaxConnLifetime = maxConnLT(time.Now().UnixNano())
	if cfg.TLS == TLSRequire {
		pc.ConnConfig.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
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
	if cfg.TLS != TLSRequire {
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
	wg.Wait()
	return nil
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
		return h.entry.pgPool.Ping(ctx)
	}
	if h.entry.sqlDB != nil {
		return h.entry.sqlDB.PingContext(ctx)
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
	return nil, fmt.Errorf("not implemented")
}
func (h *PoolHandle) NextPage(ctx context.Context, scope UserWorkspaceScope, token string) (*QueryResult, error) {
	return nil, fmt.Errorf("not implemented")
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
