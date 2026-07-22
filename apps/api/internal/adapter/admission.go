package adapter

import "sync"
import "time"

type limiter struct {
	mu         sync.Mutex
	count      int
	max        int
	lastAccess time.Time
}

func (l *limiter) tryInc(max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count >= max {
		return false
	}
	l.count++
	l.lastAccess = time.Now()
	return true
}
func (l *limiter) dec() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count > 0 {
		l.count--
	}
	l.lastAccess = time.Now()
}
func (l *limiter) idle(d time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count == 0 && time.Since(l.lastAccess) > d
}

type Permit struct {
	userID, workspaceID, connID string
	userLim, wsLim, connLim     *limiter
	ac                          *AdmissionController
	released                    bool
	mu                          sync.Mutex
}

func (p *Permit) Release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.released {
		return
	}
	p.released = true
	if p.connLim != nil {
		p.connLim.dec()
	}
	if p.wsLim != nil {
		p.wsLim.dec()
	}
	if p.userLim != nil {
		p.userLim.dec()
	}
}

type AdmissionController struct {
	mu           sync.Mutex
	users        map[string]*limiter
	workspaces   map[string]*limiter
	connections  map[string]*limiter
	maxUser      int
	maxWorkspace int
	maxConn      int
	closed       bool
}

func newAdmissionController() *AdmissionController {
	return &AdmissionController{
		users: make(map[string]*limiter), workspaces: make(map[string]*limiter),
		connections: make(map[string]*limiter), maxUser: 2, maxWorkspace: 10, maxConn: 5,
	}
}
func (ac *AdmissionController) TryAcquire(userID, workspaceID, connectionID string) (*Permit, error) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if ac.closed {
		return nil, newError(ErrPoolClosed, "admission closed", nil)
	}
	ul := ac.ensure(&ac.users, userID)
	wl := ac.ensure(&ac.workspaces, workspaceID)
	cl := ac.ensure(&ac.connections, connectionID)
	if !ul.tryInc(ac.maxUser) {
		return nil, newError(ErrRateLimited, "user rate limited", nil)
	}
	if !wl.tryInc(ac.maxWorkspace) {
		ul.dec()
		return nil, newError(ErrRateLimited, "workspace rate limited", nil)
	}
	if !cl.tryInc(ac.maxConn) {
		wl.dec()
		ul.dec()
		return nil, newError(ErrRateLimited, "connection rate limited", nil)
	}
	return &Permit{userID: userID, workspaceID: workspaceID, connID: connectionID, userLim: ul, wsLim: wl, connLim: cl, ac: ac}, nil
}
func (ac *AdmissionController) ensure(m *map[string]*limiter, key string) *limiter {
	if l, ok := (*m)[key]; ok {
		return l
	}
	l := &limiter{lastAccess: time.Now()}
	(*m)[key] = l
	return l
}
func (ac *AdmissionController) close() { ac.mu.Lock(); defer ac.mu.Unlock(); ac.closed = true }
