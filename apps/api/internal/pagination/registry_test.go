package pagination

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/queryplan"
)

func testPlan(t *testing.T) queryplan.VerifiedSortPlan {
	t.Helper()
	meta := &queryplan.TableMetadata{
		Schema: "public", Table: "users",
		Columns:    []queryplan.Column{{Name: "id", Ordinal: 1, Nullable: false}},
		PrimaryKey: &queryplan.PrimaryKey{Columns: []string{"id"}},
	}
	snap, err := queryplan.NewSchemaSnapshot("conn-1", queryplan.DialectPostgreSQL, 1, meta)
	if err != nil {
		t.Fatal(err)
	}
	shape := &queryplan.QueryShape{BaseSchema: "public", BaseTable: "users", SelectStar: true}
	p, err := queryplan.VerifySortPlan(snap, shape, []queryplan.SortKey{{Column: "id", Direction: queryplan.SortAsc}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testState(t *testing.T, user string) *ContinuationState {
	t.Helper()
	return &ContinuationState{
		UserID:           user,
		WorkspaceID:      "ws-1",
		ConnectionID:     "conn-1",
		PoolGeneration:   1,
		SchemaGeneration: "abc123",
		TableSchema:      "public",
		TableName:        "users",
		PolicyVersion:    5,
		StatementHash:    "hash-1",
		SortPlan:         testPlan(t),
		SQL:              "SELECT id FROM users WHERE id > $1",
		Args:             []any{int64(10)},
		LastSortValues:   []any{int64(10)},
		CumulativeCount:  1,
		PageSize:         100,
		MaxRows:          500,
		TimeoutMs:        5000,
	}
}

func newTestRegistry(t *testing.T, mutate func(*Config)) *Registry {
	t.Helper()
	cfg := DefaultConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	r := New(cfg)
	t.Cleanup(r.Close)
	return r
}

func TestCreateReturnsOpaqueHandle(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	h, err := r.Create(testState(t, "u1"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if len(h) != 64 { // 32 bytes hex
		t.Fatalf("handle length = %d, want 64", len(h))
	}
	if h == "" {
		t.Fatal("empty handle")
	}
}

func TestRegistryKeyIsDigestNotHandle(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	h, err := r.Create(testState(t, "u1"))
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[h]; ok {
		t.Fatal("registry stores raw bearer handle as key; must store digest")
	}
	if _, ok := r.entries[digestOf(h)]; !ok {
		t.Fatal("registry key is not the SHA-256 digest")
	}
}

func TestClaimAndRotateAndComplete(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	h, err := r.Create(testState(t, "u1"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.Claim(h)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if c.State().SQL != "SELECT id FROM users WHERE id > $1" {
		t.Fatalf("claim state wrong")
	}
	st := testState(t, "u1")
	st.CumulativeCount = 101
	st.LastSortValues = []any{int64(110)}
	h2, err := c.Rotate(st)
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if _, err := r.Claim(h); err == nil {
		t.Fatal("old handle still claimable after rotate")
	}
	c2, err := r.Claim(h2)
	if err != nil {
		t.Fatalf("Claim(h2) error = %v", err)
	}
	if c2.State().CumulativeCount != 101 {
		t.Fatalf("rotated state count = %d, want 101", c2.State().CumulativeCount)
	}
	if err := c2.Complete(); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if _, err := r.Claim(h2); err == nil {
		t.Fatal("completed token still claimable")
	}
}

func TestClaimReplayRejected(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	h, _ := r.Create(testState(t, "u1"))
	c, err := r.Claim(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Claim(h); err == nil {
		t.Fatal("second claim succeeded; must reject")
	}
	c.Abort()
}

func TestTamperedHandleRejected(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	h, _ := r.Create(testState(t, "u1"))
	b := []byte(h)
	if b[63] == 'f' {
		b[63] = 'e'
	} else {
		b[63]++
	}
	tampered := string(b)
	if _, err := r.Claim(tampered); err == nil {
		t.Fatal("tampered handle accepted")
	}
}

func TestClaimExpiredToken(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	r := newTestRegistry(t, func(c *Config) {
		c.Clock = func() time.Time { return now }
		c.TTL = time.Minute
	})
	h, _ := r.Create(testState(t, "u1"))
	r.mu.Lock()
	r.cfg.Clock = func() time.Time { return now.Add(2 * time.Minute) }
	r.mu.Unlock()
	if _, err := r.Claim(h); err == nil {
		t.Fatal("expired token claim succeeded")
	}
	r.mu.Lock()
	if len(r.entries) != 0 {
		t.Fatalf("expired entry not removed: %d", len(r.entries))
	}
	r.mu.Unlock()
}

func TestRotateFailureDeletesOldAndNotRestored(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	h, _ := r.Create(testState(t, "u1"))
	c, err := r.Claim(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Rotate(nil); err == nil {
		t.Fatal("Rotate(nil) succeeded")
	}
	if _, err := r.Claim(h); err == nil {
		t.Fatal("old token restored after rotate failure")
	}
	if err := c.Abort(); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if err := c.Abort(); err != nil {
		t.Fatalf("Abort() twice error = %v", err)
	}
}

func TestDelayedFinalizerDoesNotDeleteNewToken(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	h, _ := r.Create(testState(t, "u1"))
	c1, err := r.Claim(h)
	if err != nil {
		t.Fatal(err)
	}
	st := testState(t, "u1")
	st.CumulativeCount = 200
	h2, err := c1.Rotate(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := c1.Abort(); err != nil {
		t.Fatalf("stale Abort() error = %v", err)
	}
	if err := c1.Complete(); err != nil {
		t.Fatalf("stale Complete() error = %v", err)
	}
	if _, err := r.Claim(h2); err != nil {
		t.Fatalf("new token was deleted by stale finalizer: %v", err)
	}
}

func TestQuotaExactlyFullRotateStillWorks(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, func(c *Config) {
		c.MaxUser = 1
		c.MaxWS = 1
		c.MaxConn = 1
		c.MaxGlobal = 1
	})
	h, err := r.Create(testState(t, "u1"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.Claim(h)
	if err != nil {
		t.Fatal(err)
	}
	st := testState(t, "u1")
	st.CumulativeCount = 1
	if _, err := r.Create(st); err == nil {
		t.Fatal("create with in-flight-only and full quota must fail")
	}
	st2 := testState(t, "u1")
	st2.CumulativeCount = 101
	h2, err := c.Rotate(st2)
	if err != nil {
		t.Fatalf("rotate at full quota failed: %v", err)
	}
	if h2 == "" {
		t.Fatal("empty new handle")
	}
	c2, err := r.Claim(h2)
	if err != nil {
		t.Fatalf("new token not claimable: %v", err)
	}
	c2.Complete()
}

func TestByteQuotaPerState(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, func(c *Config) {
		c.MaxStateBytes = 100
	})
	st := testState(t, "u1")
	st.SQL = strings.Repeat("x", 200)
	if _, err := r.Create(st); err == nil {
		t.Fatal("create exceeding per-state byte quota succeeded")
	}
}

func TestByteQuotaDimensionEvictsReady(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, func(c *Config) {
		c.MaxUserBytes = 100 // 只够容纳一个状态
	})
	h1, err := r.Create(testState(t, "u1"))
	if err != nil {
		t.Fatal(err)
	}
	st2 := testState(t, "u1")
	h2, err := r.Create(st2)
	if err != nil {
		t.Fatalf("second create should evict ready and succeed: %v", err)
	}
	if h1 == h2 {
		t.Fatal("handles must differ")
	}
	if _, err := r.Claim(h1); err == nil {
		t.Fatal("evicted token still claimable")
	}
	if _, err := r.Claim(h2); err != nil {
		t.Fatalf("new token not claimable: %v", err)
	}
}

func TestExpiryCleanupRemovesReadyAndInFlight(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	r := newTestRegistry(t, func(c *Config) {
		c.Clock = func() time.Time { return now }
		c.TTL = time.Minute
	})
	h1, _ := r.Create(testState(t, "u1"))
	h2, _ := r.Create(testState(t, "u2"))
	c2, _ := r.Claim(h2)
	r.mu.Lock()
	r.cfg.Clock = func() time.Time { return now.Add(2 * time.Minute) }
	r.mu.Unlock()
	r.CleanupExpired()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) != 0 {
		t.Fatalf("expired entries not cleaned: %d", len(r.entries))
	}
	if r.globalCount != 0 || r.globalBytes != 0 {
		t.Fatalf("quota not released: count=%d bytes=%d", r.globalCount, r.globalBytes)
	}
	_ = h1
	_ = c2
}

func TestConcurrentClaimOnlyOneSucceeds(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	h, _ := r.Create(testState(t, "u1"))
	var wg sync.WaitGroup
	success := make(chan bool, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Claim(h); err == nil {
				success <- true
			}
		}()
	}
	wg.Wait()
	close(success)
	n := 0
	for range success {
		n++
	}
	if n != 1 {
		t.Fatalf("concurrent claims succeeded = %d, want 1", n)
	}
}

func TestConcurrentRotateOnlyOneSucceeds(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	h, _ := r.Create(testState(t, "u1"))
	c, err := r.Claim(h)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ok := make(chan bool, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := testState(t, "u1")
			st.CumulativeCount = 101
			if _, err := c.Rotate(st); err == nil {
				ok <- true
			}
		}()
	}
	wg.Wait()
	close(ok)
	n := 0
	for range ok {
		n++
	}
	if n != 1 {
		t.Fatalf("concurrent rotates succeeded = %d, want 1", n)
	}
	if r.Stats().ActiveTokens != 1 {
		t.Fatalf("active tokens after rotate = %d, want 1", r.Stats().ActiveTokens)
	}
}

func TestCompleteIdempotentNoNegativeCounts(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	h, _ := r.Create(testState(t, "u1"))
	c, _ := r.Claim(h)
	for i := 0; i < 5; i++ {
		if err := c.Complete(); err != nil {
			t.Fatalf("Complete() #%d error = %v", i, err)
		}
	}
	s := r.Stats()
	if s.ActiveTokens != 0 || s.GlobalBytes != 0 {
		t.Fatalf("counts corrupted: %+v", s)
	}
}

func TestCreateDeepCopiesNonStringKeyMapArgs(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	st := testState(t, "u1")
	m := map[int]any{1: "a", 2: []string{"x", "y"}}
	st.Args = []any{m}
	h, err := r.Create(st)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// 篡改原始 map（键值与非 string 键 map 的嵌套 slice）
	m[1] = "mutated"
	m[2].([]string)[0] = "z"
	c, err := r.Claim(h)
	if err != nil {
		t.Fatal(err)
	}
	args := c.State().Args
	inner, ok := args[0].(map[int]any)
	if !ok {
		t.Fatalf("args[0] type = %T, want map[int]any", args[0])
	}
	if inner[1] != "a" {
		t.Fatalf("map[int]any key value mutated: %v", inner[1])
	}
	if inner[2].([]string)[0] != "x" {
		t.Fatalf("nested slice in non-string-key map mutated: %v", inner[2])
	}
}

func TestClaimStateReturnsCopy(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	st := testState(t, "u1")
	st.Args = []any{"original"}
	h, err := r.Create(st)
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.Claim(h)
	if err != nil {
		t.Fatal(err)
	}
	state := c.State()
	// 篡改返回的 state 不得影响 registry 内部状态
	state.Args = []any{"mutated"}
	state.CumulativeCount = 999
	r.mu.Lock()
	entry := r.entries[digestOf(h)]
	r.mu.Unlock()
	if entry.state.Args[0] != "original" {
		t.Fatalf("internal Args mutated via Claim.State(): %v", entry.state.Args)
	}
	if entry.state.CumulativeCount == 999 {
		t.Fatal("internal CumulativeCount mutated via Claim.State()")
	}
}

func TestInvalidStateRejected(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t, nil)
	st := testState(t, "u1")
	st.SortPlan = nil
	if _, err := r.Create(st); err == nil {
		t.Fatal("nil SortPlan accepted")
	}
	st2 := testState(t, "u1")
	st2.StatementHash = ""
	if _, err := r.Create(st2); err == nil {
		t.Fatal("empty statement hash accepted")
	}
}
