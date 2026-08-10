package pagination

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fujiabao89/webdb/internal/queryplan"
)

// ErrorCode 是 registry 稳定错误码（由服务层映射为 execution.StableErrorCode）。
type ErrorCode string

const (
	ErrInvalidPageToken   ErrorCode = "invalid_page_token"
	ErrPaginationCapacity ErrorCode = "pagination_capacity_exhausted"
	ErrRegistryClosed     ErrorCode = "registry_closed"
)

// RegistryError 带稳定错误码的 registry 错误。
type RegistryError struct {
	Code ErrorCode
	Msg  string
}

func (e *RegistryError) Error() string { return fmt.Sprintf("[%s] %s", e.Code, e.Msg) }

func newRegistryError(code ErrorCode, msg string) error {
	return &RegistryError{Code: code, Msg: msg}
}

// IsRegistryErrorCode 判断错误是否带指定 registry 错误码（含包装错误，errors.As 解包）。
func IsRegistryErrorCode(err error, code ErrorCode) bool {
	var re *RegistryError
	if !errors.As(err, &re) {
		return false
	}
	return re.Code == code
}

// Config 是 registry 配置。数量上限沿用 ADR-015；字节上限为 Owner 批准的默认值
// （WEB-38，2026-08-09）：单状态 512 KiB、每用户 16 MiB、每工作区 64 MiB、
// 每连接 16 MiB、全局 512 MiB。TTL 上限强制为 5 分钟。
type Config struct {
	TTL            time.Duration
	MaxGlobal      int
	MaxUser        int
	MaxWS          int
	MaxConn        int
	MaxStateBytes  int64
	MaxUserBytes   int64
	MaxWSBytes     int64
	MaxConnBytes   int64
	MaxGlobalBytes int64
	Clock          func() time.Time
}

// DefaultConfig 返回批准默认值。
func DefaultConfig() Config {
	return Config{
		TTL:            5 * time.Minute,
		MaxGlobal:      10000,
		MaxUser:        100,
		MaxWS:          500,
		MaxConn:        200,
		MaxStateBytes:  512 << 10,
		MaxUserBytes:   16 << 20,
		MaxWSBytes:     64 << 20,
		MaxConnBytes:   16 << 20,
		MaxGlobalBytes: 512 << 20,
	}
}

// NormalizedConfig 收敛安全默认值并强制 TTL ≤ 5 分钟。
func NormalizedConfig(cfg Config) Config {
	if cfg.TTL <= 0 || cfg.TTL > 5*time.Minute {
		cfg.TTL = 5 * time.Minute
	}
	if cfg.MaxGlobal <= 0 {
		cfg.MaxGlobal = 10000
	}
	if cfg.MaxUser <= 0 {
		cfg.MaxUser = 100
	}
	if cfg.MaxWS <= 0 {
		cfg.MaxWS = 500
	}
	if cfg.MaxConn <= 0 {
		cfg.MaxConn = 200
	}
	if cfg.MaxStateBytes <= 0 {
		cfg.MaxStateBytes = 512 << 10
	}
	if cfg.MaxUserBytes <= 0 {
		cfg.MaxUserBytes = 16 << 20
	}
	if cfg.MaxWSBytes <= 0 {
		cfg.MaxWSBytes = 64 << 20
	}
	if cfg.MaxConnBytes <= 0 {
		cfg.MaxConnBytes = 16 << 20
	}
	if cfg.MaxGlobalBytes <= 0 {
		cfg.MaxGlobalBytes = 512 << 20
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	return cfg
}

type entryStatus int

const (
	statusReady entryStatus = iota
	statusInFlight
)

// entry 是 registry 内部条目；key 为 opaque handle 的 SHA-256 digest。
type entry struct {
	state     *ContinuationState
	status    entryStatus
	version   uint64 // claim ownership/version，防延迟 finalizer 误删新 token
	bytes     int64
	expiresAt time.Time
	createdAt time.Time
}

// Registry 是 Service-owned 的内存 continuation registry。
// 单互斥锁保证所有状态转换原子；只驱逐 ready；ready 与 in-flight 都计入配额。
type Registry struct {
	mu          sync.Mutex
	cfg         Config
	entries     map[string]*entry
	userCount   map[string]int
	wsCount     map[string]int
	connCount   map[string]int
	globalCount int
	userBytes   map[string]int64
	wsBytes     map[string]int64
	connBytes   map[string]int64
	globalBytes int64
	seq         uint64
	closed      bool
	cancel      context.CancelFunc
}

// New 创建 registry 并启动过期清理 goroutine。
func New(cfg Config) *Registry {
	cfg = NormalizedConfig(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r := &Registry{
		cfg:       cfg,
		entries:   make(map[string]*entry),
		userCount: make(map[string]int),
		wsCount:   make(map[string]int),
		connCount: make(map[string]int),
		userBytes: make(map[string]int64),
		wsBytes:   make(map[string]int64),
		connBytes: make(map[string]int64),
		cancel:    cancel,
	}
	go r.cleanupLoop(ctx)
	return r
}

// Close 停止清理 goroutine 并使 registry 拒绝新操作。
func (r *Registry) Close() {
	r.cancel()
	r.mu.Lock()
	r.closed = true
	r.entries = nil
	r.mu.Unlock()
}

func (r *Registry) now() time.Time { return r.cfg.Clock() }

func (r *Registry) nextVersion() uint64 {
	r.seq++
	return r.seq
}

// Stats 是 registry 观测快照。
type Stats struct {
	ActiveTokens    int
	InFlightTokens  int
	GlobalBytes     int64
	UserCount       int
	WorkspaceCount  int
	ConnectionCount int
}

// Stats 返回当前状态快照（供指标/测试使用，不含任何 token 或敏感字段）。
func (r *Registry) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Stats{ActiveTokens: len(r.entries), GlobalBytes: r.globalBytes}
	for _, e := range r.entries {
		if e.status == statusInFlight {
			s.InFlightTokens++
		}
	}
	s.UserCount = len(r.userCount)
	s.WorkspaceCount = len(r.wsCount)
	s.ConnectionCount = len(r.connCount)
	return s
}

// Create 深拷贝 state、校验并创建 ready token，返回唯一一次的 opaque handle。
func (r *Registry) Create(state *ContinuationState) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", newRegistryError(ErrRegistryClosed, "registry closed")
	}
	if state == nil || !validState(state) {
		return "", newRegistryError(ErrInvalidPageToken, "invalid continuation state")
	}
	cp := state.deepCopy()
	cp.ExpiresAt = r.now().Add(r.cfg.TTL)
	n := stateBytes(cp)
	if n > r.cfg.MaxStateBytes {
		return "", newRegistryError(ErrPaginationCapacity, "state exceeds per-state byte quota")
	}
	if !r.evictForQuota(cp, n) {
		return "", newRegistryError(ErrPaginationCapacity, "pagination capacity exhausted")
	}
	handle, err := genHandle()
	if err != nil {
		return "", err
	}
	d := digestOf(handle)
	r.entries[d] = &entry{
		state: cp, status: statusReady, version: r.nextVersion(),
		bytes: n, expiresAt: cp.ExpiresAt, createdAt: r.now(),
	}
	r.addQuota(cp, n)
	return handle, nil
}

// Claim 原子执行 ready → in-flight；过期则删除。并发 claim 同一 token 仅第一个成功。
func (r *Registry) Claim(handle string) (*Claim, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, newRegistryError(ErrRegistryClosed, "registry closed")
	}
	d := digestOf(handle)
	e, ok := r.entries[d]
	if !ok {
		return nil, newRegistryError(ErrInvalidPageToken, "token not found")
	}
	if e.status != statusReady {
		return nil, newRegistryError(ErrInvalidPageToken, "token already in flight")
	}
	// now >= expiresAt 即拒绝（相等边界同样视为过期，fail-closed，Codex 审查）。
	if !r.now().Before(e.expiresAt) {
		r.deleteEntry(d)
		return nil, newRegistryError(ErrInvalidPageToken, "token expired")
	}
	e.status = statusInFlight
	return &Claim{reg: r, digest: d, version: e.version, state: e.state}, nil
}

// Revoke 原子删除一个尚未被 claim 的 ready token（如审计持久化失败后的撤销），
// 释放计数与字节配额。token 不存在、已过期或已被 claim（in-flight）时 no-op；
// in-flight token 只能通过其 Claim 句柄 Complete/Abort 结束，避免误删并发持有者。
func (r *Registry) Revoke(handle string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	d := digestOf(handle)
	e, ok := r.entries[d]
	if !ok || e.status != statusReady {
		return
	}
	r.deleteEntry(d)
}

// CleanupExpired 删除所有过期条目（ready 与 in-flight），并释放配额。
func (r *Registry) CleanupExpired() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for d, e := range r.entries {
		// now >= expiresAt 即清理（与 Claim/Rotate 的相等边界语义一致）。
		if !now.Before(e.expiresAt) {
			r.deleteEntry(d)
		}
	}
}

func (r *Registry) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.CleanupExpired()
		}
	}
}

// deleteEntry 删除条目并释放全部计数/字节配额。幂等。
func (r *Registry) deleteEntry(d string) {
	e, ok := r.entries[d]
	if !ok {
		return
	}
	delete(r.entries, d)
	r.removeQuota(e.state, e.bytes)
}

func (r *Registry) addQuota(s *ContinuationState, n int64) {
	r.userCount[s.UserID]++
	r.wsCount[s.WorkspaceID]++
	r.connCount[s.ConnectionID]++
	r.globalCount++
	r.userBytes[s.UserID] += n
	r.wsBytes[s.WorkspaceID] += n
	r.connBytes[s.ConnectionID] += n
	r.globalBytes += n
}

func (r *Registry) removeQuota(s *ContinuationState, n int64) {
	if s == nil {
		return
	}
	// 计数/字节归零时删除 map 键，避免零值键无界累积。
	if v := r.userCount[s.UserID]; v > 1 {
		r.userCount[s.UserID] = v - 1
	} else {
		delete(r.userCount, s.UserID)
	}
	if v := r.wsCount[s.WorkspaceID]; v > 1 {
		r.wsCount[s.WorkspaceID] = v - 1
	} else {
		delete(r.wsCount, s.WorkspaceID)
	}
	if v := r.connCount[s.ConnectionID]; v > 1 {
		r.connCount[s.ConnectionID] = v - 1
	} else {
		delete(r.connCount, s.ConnectionID)
	}
	if r.globalCount > 0 {
		r.globalCount--
	}
	if v := r.userBytes[s.UserID]; v > n {
		r.userBytes[s.UserID] = v - n
	} else {
		delete(r.userBytes, s.UserID)
	}
	if v := r.wsBytes[s.WorkspaceID]; v > n {
		r.wsBytes[s.WorkspaceID] = v - n
	} else {
		delete(r.wsBytes, s.WorkspaceID)
	}
	if v := r.connBytes[s.ConnectionID]; v > n {
		r.connBytes[s.ConnectionID] = v - n
	} else {
		delete(r.connBytes, s.ConnectionID)
	}
	if r.globalBytes >= n {
		r.globalBytes -= n
	}
}

// evictForQuota 在插入新条目（bytes=n）前逐出 ready 条目直到所有配额满足。
// 只允许驱逐 ready（in-flight 永不因配额被驱逐）。
func (r *Registry) evictForQuota(s *ContinuationState, n int64) bool {
	for {
		if r.globalCount < r.cfg.MaxGlobal &&
			r.userCount[s.UserID] < r.cfg.MaxUser &&
			r.wsCount[s.WorkspaceID] < r.cfg.MaxWS &&
			r.connCount[s.ConnectionID] < r.cfg.MaxConn &&
			r.globalBytes+n <= r.cfg.MaxGlobalBytes &&
			r.userBytes[s.UserID]+n <= r.cfg.MaxUserBytes &&
			r.wsBytes[s.WorkspaceID]+n <= r.cfg.MaxWSBytes &&
			r.connBytes[s.ConnectionID]+n <= r.cfg.MaxConnBytes {
			return true
		}
		d := r.pickEvictable(s, n)
		if d == "" {
			return false
		}
		r.deleteEntry(d)
	}
}

// pickEvictable 从超限维度选择最旧的 ready 条目驱逐（n 为待插入条目实际字节）。
// 返回 digest；无候选返回 ""。只驱逐 ready，in-flight 永不因配额被驱逐。
func (r *Registry) pickEvictable(s *ContinuationState, n int64) string {
	var best string
	var bestT time.Time
	pick := func(d string, e *entry) {
		if e.status != statusReady {
			return
		}
		if best == "" || e.expiresAt.Before(bestT) {
			best, bestT = d, e.expiresAt
		}
	}
	for d, e := range r.entries {
		if r.userCount[s.UserID] >= r.cfg.MaxUser || r.userBytes[s.UserID]+n > r.cfg.MaxUserBytes {
			if e.state.UserID == s.UserID {
				pick(d, e)
			}
			continue
		}
		if r.wsCount[s.WorkspaceID] >= r.cfg.MaxWS || r.wsBytes[s.WorkspaceID]+n > r.cfg.MaxWSBytes {
			if e.state.WorkspaceID == s.WorkspaceID {
				pick(d, e)
			}
			continue
		}
		if r.connCount[s.ConnectionID] >= r.cfg.MaxConn || r.connBytes[s.ConnectionID]+n > r.cfg.MaxConnBytes {
			if e.state.ConnectionID == s.ConnectionID {
				pick(d, e)
			}
			continue
		}
		pick(d, e) // global 压力：任意 ready
	}
	return best
}

// Claim 是一次成功 claim 的句柄；Rotate/Complete/Abort 必须通过它执行
// （claim ownership/version 校验），延迟 finalizer 无法删除已旋转出的新 token。
type Claim struct {
	reg     *Registry
	digest  string
	version uint64
	state   *ContinuationState
}

// State 返回已 claim 状态的深拷贝，防止调用方篡改 registry 内部状态
// （FINDING 4：不得暴露内部指针）。
func (c *Claim) State() *ContinuationState {
	if c == nil {
		return nil
	}
	return c.state.deepCopy()
}

// Rotate 原子旋转：删除旧 in-flight 条目，在同一容量槽写入新 ready 条目，
// 返回新的 opaque handle。任一条件不满足时旧 token 永久失效、不恢复。
func (c *Claim) Rotate(newState *ContinuationState) (string, error) {
	if c == nil || c.reg == nil {
		return "", newRegistryError(ErrInvalidPageToken, "invalid claim")
	}
	r := c.reg
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[c.digest]
	if !ok || e.status != statusInFlight || e.version != c.version {
		return "", newRegistryError(ErrInvalidPageToken, "claim ownership mismatch")
	}
	// now >= expiresAt 即拒绝（相等边界同样视为过期，不允许已过期 claim 创建后继 token）。
	if !r.now().Before(e.expiresAt) {
		r.deleteEntry(c.digest)
		return "", newRegistryError(ErrInvalidPageToken, "token expired")
	}
	if newState == nil || !validState(newState) {
		r.deleteEntry(c.digest)
		return "", newRegistryError(ErrInvalidPageToken, "invalid new state")
	}
	// 身份一致性：旋转后的状态必须绑定与 claim 前相同的 user/workspace/connection，
	// 防止旋转把 token 迁移到其他主体（Codex 审查）。
	if newState.UserID != e.state.UserID || newState.WorkspaceID != e.state.WorkspaceID ||
		newState.ConnectionID != e.state.ConnectionID {
		r.deleteEntry(c.digest)
		return "", newRegistryError(ErrInvalidPageToken, "rotated state identity mismatch")
	}
	cp := newState.deepCopy()
	// TTL 为绝对过期时间，自 token 创建起算（ADR-015 §6），旋转不得重新计算
	// now+TTL：否则客户端在过期前持续请求下一页可无限延长服务端保存的
	// SQL/参数/游标生命周期。旋转后继承被 claim 条目的原始 expiresAt。
	cp.ExpiresAt = e.expiresAt
	n := stateBytes(cp)
	if n > r.cfg.MaxStateBytes {
		r.deleteEntry(c.digest)
		return "", newRegistryError(ErrPaginationCapacity, "new state exceeds per-state byte quota")
	}
	// 同一容量槽：释放旧字节后再校验各维度字节预算（net = n - old）。
	old := e.bytes
	net := n - old
	if !r.fitsBytes(cp, net) {
		r.deleteEntry(c.digest)
		return "", newRegistryError(ErrPaginationCapacity, "rotate would exceed byte quota")
	}
	handle, err := genHandle()
	if err != nil {
		r.deleteEntry(c.digest)
		return "", err
	}
	r.deleteEntry(c.digest)
	nd := digestOf(handle)
	r.entries[nd] = &entry{
		state: cp, status: statusReady, version: r.nextVersion(),
		bytes: n, expiresAt: cp.ExpiresAt, createdAt: r.now(),
	}
	r.addQuota(cp, n)
	return handle, nil
}

// fitsBytes 校验 net 字节变化后各维度字节预算仍满足（count 槽位在 rotate 中不变）。
func (r *Registry) fitsBytes(s *ContinuationState, net int64) bool {
	return r.globalBytes+net <= r.cfg.MaxGlobalBytes &&
		r.userBytes[s.UserID]+net <= r.cfg.MaxUserBytes &&
		r.wsBytes[s.WorkspaceID]+net <= r.cfg.MaxWSBytes &&
		r.connBytes[s.ConnectionID]+net <= r.cfg.MaxConnBytes
}

// Complete 原子删除旧 in-flight 条目（无后续页）。幂等。
func (c *Claim) Complete() error {
	if c == nil || c.reg == nil {
		return nil
	}
	r := c.reg
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[c.digest]
	if !ok || e.version != c.version {
		return nil // 已删除或已被新 token 取代：幂等 no-op
	}
	if e.status != statusInFlight {
		return nil
	}
	r.deleteEntry(c.digest)
	return nil
}

// Abort 原子删除旧 in-flight 条目（失败/取消/超时/panic 路径）。幂等。
// 旧 token 永不恢复。
func (c *Claim) Abort() error {
	if c == nil || c.reg == nil {
		return nil
	}
	r := c.reg
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[c.digest]
	if !ok || e.version != c.version {
		return nil
	}
	if e.status != statusInFlight {
		return nil
	}
	r.deleteEntry(c.digest)
	return nil
}

func validState(s *ContinuationState) bool {
	return s.UserID != "" && s.WorkspaceID != "" && s.ConnectionID != "" &&
		s.SchemaGeneration != "" && s.TableSchema != "" && s.TableName != "" &&
		s.PolicyVersion > 0 && s.StatementHash != "" && s.SortPlan != nil &&
		queryplanIsValid(s.SortPlan) && s.SQL != "" &&
		s.PageSize > 0 && s.MaxRows > 0
}

// queryplanIsValid 委托 queryplan 校验计划（nil/typed-nil/version）。
func queryplanIsValid(p queryplan.VerifiedSortPlan) bool {
	return queryplan.IsValidVerifiedPlan(p)
}

// genHandle 生成 32 字节 CSPRNG opaque handle（hex 编码 64 字符）。
func genHandle() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("continuation handle generation: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// digestOf 计算 opaque handle 的 SHA-256 digest（registry key 保存 digest，不保存 handle）。
func digestOf(handle string) string {
	sum := sha256.Sum256([]byte(handle))
	return hex.EncodeToString(sum[:])
}
