package pagination

import (
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/queryplan"
)

func fuzzState(seed string) *ContinuationState {
	meta := &queryplan.TableMetadata{
		Schema: "public", Table: "users",
		Columns:    []queryplan.Column{{Name: "id", Ordinal: 1, Nullable: false}},
		PrimaryKey: &queryplan.PrimaryKey{Columns: []string{"id"}},
	}
	snap, err := queryplan.NewSchemaSnapshot("conn-1", queryplan.DialectPostgreSQL, 1, meta)
	if err != nil {
		panic(err)
	}
	plan, err := queryplan.VerifySortPlan(snap,
		&queryplan.QueryShape{BaseSchema: "public", BaseTable: "users", SelectStar: true},
		[]queryplan.SortKey{{Column: "id", Direction: queryplan.SortAsc}})
	if err != nil {
		panic(err)
	}
	n := min(len(seed), 8)
	return &ContinuationState{
		UserID:           "u-" + seed[:n],
		WorkspaceID:      "ws-1",
		ConnectionID:     "conn-1",
		PoolGeneration:   1,
		SchemaGeneration: "abc123",
		TableSchema:      "public",
		TableName:        "users",
		PolicyVersion:    5,
		StatementHash:    "hash-" + seed[:n],
		SortPlan:         plan,
		SQL:              "SELECT id FROM users WHERE id > $1" + seed[:n],
		Args:             []any{int64(len(seed))},
		LastSortValues:   []any{int64(len(seed))},
		CumulativeCount:  len(seed),
		PageSize:         100,
		MaxRows:          500,
		TimeoutMs:        5000,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// invariantsHeld 校验 registry 计数/字节配额与 entries 一致（含锁）。
func invariantsHeld(r *Registry) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) != r.globalCount {
		return false
	}
	var totalBytes int64
	userC, wsC, connC := map[string]int{}, map[string]int{}, map[string]int{}
	userB, wsB, connB := map[string]int64{}, map[string]int64{}, map[string]int64{}
	for _, e := range r.entries {
		totalBytes += e.bytes
		userC[e.state.UserID]++
		wsC[e.state.WorkspaceID]++
		connC[e.state.ConnectionID]++
		userB[e.state.UserID] += e.bytes
		wsB[e.state.WorkspaceID] += e.bytes
		connB[e.state.ConnectionID] += e.bytes
	}
	if totalBytes != r.globalBytes {
		return false
	}
	if len(userC) != len(r.userCount) {
		return false
	}
	for k, v := range userC {
		if r.userCount[k] != v {
			return false
		}
	}
	for k, v := range userB {
		if r.userBytes[k] != v {
			return false
		}
	}
	for k, v := range wsC {
		if r.wsCount[k] != v {
			return false
		}
	}
	for k, v := range connC {
		if r.connCount[k] != v {
			return false
		}
	}
	return true
}

func FuzzRegistryInvariants(f *testing.F) {
	f.Add("SELECT * FROM t")
	f.Add("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	f.Add("")
	f.Add("1234567890123456789012345678901234567890123456789012345678901234")

	f.Fuzz(func(t *testing.T, seed string) {
		cfg := DefaultConfig()
		cfg.MaxGlobal = 8
		cfg.MaxUser = 4
		cfg.MaxWS = 8
		cfg.MaxConn = 4
		cfg.MaxStateBytes = 512
		cfg.MaxUserBytes = 2048
		cfg.MaxWSBytes = 4096
		cfg.MaxConnBytes = 2048
		cfg.MaxGlobalBytes = 8192
		cfg.Clock = func() time.Time { return time.Unix(1_700_000_000, 0) }
		r := New(cfg)
		defer r.Close()

		var handles []string
		var claims []*Claim
		op := 0
		if len(seed) > 0 {
			op = int(seed[0])
		}
		for i := 0; i < 100; i++ {
			switch (op + i) % 5 {
			case 0:
				if h, err := r.Create(fuzzState(seed)); err == nil {
					handles = append(handles, h)
				}
			case 1:
				if len(handles) > 0 {
					if c, err := r.Claim(handles[len(handles)-1]); err == nil {
						claims = append(claims, c)
					}
				}
			case 2:
				if len(claims) > 0 {
					c := claims[len(claims)-1]
					if h, err := c.Rotate(fuzzState(seed + "r")); err == nil {
						handles = append(handles, h)
					}
				}
			case 3:
				if len(claims) > 0 {
					c := claims[len(claims)-1]
					if i%2 == 0 {
						c.Complete()
					} else {
						c.Abort()
					}
					claims = claims[:len(claims)-1]
				}
			case 4:
				r.CleanupExpired()
			}
			if !invariantsHeld(r) {
				t.Fatalf("invariants broken at op %d seed=%q", i, seed)
			}
		}
	})
}
