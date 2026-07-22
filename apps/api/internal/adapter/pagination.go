package adapter

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type PagePlan struct {
	SQL             string
	Args            []any
	SortKeys        []SortKey
	LastSortValues  []any
	PageSize        int
	MaxRows         int
	CumulativeCount int
	Scope           UserWorkspaceScope
	ConnectionID    string
	Generation      int64
	ExpiresAt       time.Time
	inUse           bool
}

type ContinuationRegistry struct {
	mu        sync.Mutex
	tokens    map[string]*PagePlan
	maxGlobal int
	maxUser   int
	maxWS     int
	maxConn   int
	closed    bool
}

func newContinuationRegistry() *ContinuationRegistry {
	return &ContinuationRegistry{
		tokens:    make(map[string]*PagePlan),
		maxGlobal: 10000, maxUser: 100, maxWS: 500, maxConn: 200,
	}
}

func (r *ContinuationRegistry) create(plan *PagePlan) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", newError(ErrPoolClosed, "registry closed", nil)
	}

	tok, err := genToken()
	if err != nil {
		return "", err
	}

	connKey := plan.ConnectionID
	userKey := plan.Scope.UserID
	wsKey := plan.Scope.WorkspaceID

	connCount, userCount, wsCount, globalCount := 0, 0, 0, 0
	for _, p := range r.tokens {
		if p.ConnectionID == connKey {
			connCount++
		}
		if p.Scope.UserID == userKey {
			userCount++
		}
		if p.Scope.WorkspaceID == wsKey {
			wsCount++
		}
		globalCount++
	}

	if globalCount >= r.maxGlobal || connCount >= r.maxConn || userCount >= r.maxUser || wsCount >= r.maxWS {
		if !r.evict(connKey, userKey, wsKey, connCount, userCount, wsCount, globalCount) {
			return "", newError(ErrPaginationCapacity, "token capacity exhausted", nil)
		}
	}

	plan.ExpiresAt = time.Now().Add(5 * time.Minute)
	r.tokens[tok] = plan
	return tok, nil
}

func (r *ContinuationRegistry) evict(connKey, userKey, wsKey string, connC, userC, wsC, globalC int) bool {
	type candidate struct {
		tok string
		t   time.Time
	}
	var candidates []candidate
	for t, p := range r.tokens {
		if p.inUse {
			continue
		}
		switch {
		case p.ConnectionID == connKey && connC >= r.maxConn:
			candidates = append(candidates, candidate{t, p.ExpiresAt})
		case p.Scope.UserID == userKey && userC >= r.maxUser:
			candidates = append(candidates, candidate{t, p.ExpiresAt})
		case p.Scope.WorkspaceID == wsKey && wsC >= r.maxWS:
			candidates = append(candidates, candidate{t, p.ExpiresAt})
		case globalC >= r.maxGlobal:
			candidates = append(candidates, candidate{t, p.ExpiresAt})
		}
	}
	if len(candidates) == 0 {
		return false
	}
	oldest := candidates[0]
	for _, c := range candidates[1:] {
		if c.t.Before(oldest.t) {
			oldest = c
		}
	}
	delete(r.tokens, oldest.tok)
	return true
}

func (r *ContinuationRegistry) claim(token string) (*PagePlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, newError(ErrPoolClosed, "registry closed", nil)
	}
	plan, ok := r.tokens[token]
	if !ok {
		return nil, newError(ErrInvalidPageToken, "token not found", nil)
	}
	if plan.inUse {
		return nil, newError(ErrInvalidPageToken, "token in use", nil)
	}
	if time.Now().After(plan.ExpiresAt) {
		delete(r.tokens, token)
		return nil, newError(ErrInvalidPageToken, "token expired", nil)
	}
	plan.inUse = true
	return plan, nil
}

func (r *ContinuationRegistry) replace(oldToken, newToken string, plan *PagePlan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return newError(ErrPoolClosed, "registry closed", nil)
	}
	delete(r.tokens, oldToken)
	r.tokens[newToken] = plan
	return nil
}

func (r *ContinuationRegistry) restore(token string, plan *PagePlan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	plan.inUse = false
}

func (r *ContinuationRegistry) expire(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tokens, token)
}

func (r *ContinuationRegistry) invalidateByPool(connID string, generation int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for t, p := range r.tokens {
		if p.ConnectionID == connID && p.Generation <= generation {
			delete(r.tokens, t)
		}
	}
}

func (r *ContinuationRegistry) cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for t, p := range r.tokens {
		if !p.inUse && now.After(p.ExpiresAt) {
			delete(r.tokens, t)
		}
	}
}

func (r *ContinuationRegistry) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.tokens = nil
}

func (r *ContinuationRegistry) stats() (active int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	active = len(r.tokens)
	return
}

func genToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("token generation: %w", err)
	}
	return hex.EncodeToString(b), nil
}
