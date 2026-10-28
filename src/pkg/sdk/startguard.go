package sdk

import "sync"

// StartGuard keeps a command-driven consumer from starting a connection twice
// (#375, #397). RestoreRunning and the start-command subscription call the same
// start function on different goroutines; without a claim taken before the
// slow part of a start (database lookup, secrets, opening a client), two starts
// for one connection can both pass an "already active" check and both start a
// poller, and every record is then published twice.
//
// The guard has no lock of its own. It works under the lock the consumer
// already uses for its map of active connections, passed to Reserve, so
// "active" and "starting" are checked and changed in one critical section. The
// consumer's map stays the record of what is active.
//
// A start goes:
//
//	claim, ok := s.starts.Reserve(&s.mu, id, func() bool { _, ok := s.active[id]; return ok })
//	if !ok { return } // already active or starting
//	defer claim.Release()
//	... slow work; return early on any failure ...
//	if !claim.Activate(func() { s.active[id] = cancel }) {
//		cancel() // a stop arrived meanwhile
//		return
//	}
//
// and the stop handler, inside its existing critical section on s.mu, calls
// s.starts.StopLocked(id); the consumer's Stop calls StopAllLocked.
//
// The zero value is ready to use. A StartGuard must not be copied after first
// use.
type StartGuard struct {
	// AfterReserve, when set, runs once a start holds its claim, after Reserve
	// has released the lock and before it returns. Tests use it to hold one
	// start inside its slow part while another arrives. Nil in production.
	AfterReserve func(connectionID string)

	// starting holds the claim of every start in progress, by connection ID.
	// Guarded by the owner's lock.
	starting map[string]*StartClaim
}

// StartClaim is one start's hold on a connection, from Reserve until Activate
// or Release.
type StartClaim struct {
	guard        *StartGuard
	mu           sync.Locker
	connectionID string
	stopped      bool // set by StopLocked/StopAllLocked, under mu
}

// Reserve claims connectionID for a start. mu is the lock that guards the
// owner's active map; Reserve takes it. active reports, with mu held, whether
// the connection is already active, and must not lock mu itself.
//
// It returns false when the connection is active or another start holds the
// claim; the caller then returns without starting anything. On true the caller
// must defer claim.Release().
func (g *StartGuard) Reserve(mu sync.Locker, connectionID string, active func() bool) (*StartClaim, bool) {
	mu.Lock()
	if active() {
		mu.Unlock()
		return nil, false
	}
	if _, busy := g.starting[connectionID]; busy {
		mu.Unlock()
		return nil, false
	}
	if g.starting == nil {
		g.starting = make(map[string]*StartClaim)
	}
	c := &StartClaim{guard: g, mu: mu, connectionID: connectionID}
	g.starting[connectionID] = c
	mu.Unlock()

	if g.AfterReserve != nil {
		g.AfterReserve(connectionID)
	}
	return c, true
}

// Activate turns the claim into an active connection in one critical section:
// with the owner's lock held it runs insert, which puts the connection in the
// owner's active map, and drops the claim. insert must not lock the owner's
// lock.
//
// It returns false, without running insert, if a stop took the claim back
// first. The caller then cancels whatever it set up and returns without
// writing status "running" or starting a poller.
func (c *StartClaim) Activate(insert func()) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return false
	}
	insert()
	c.drop()
	return true
}

// Release drops the claim if it is still this start's, so a later start can
// try again after this one gave up. Deferred right after Reserve; a no-op
// after Activate, and after a stop took the claim (a newer start may hold the
// connection by then, and its claim is left alone). Safe on a nil claim.
func (c *StartClaim) Release() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drop()
}

// drop removes the claim from the guard if it is still this one. Caller holds
// c.mu.
func (c *StartClaim) drop() {
	if c.guard.starting[c.connectionID] == c {
		delete(c.guard.starting, c.connectionID)
	}
}

// StopLocked tells a start in progress for connectionID to give up, and
// reports whether there was one. The caller holds the owner's lock, the same
// one it passes to Reserve. The claim is removed as well as marked, so a start
// that arrives after the stop is not refused while the abandoned one is still
// working.
func (g *StartGuard) StopLocked(connectionID string) bool {
	c, ok := g.starting[connectionID]
	if !ok {
		return false
	}
	c.stopped = true
	delete(g.starting, connectionID)
	return true
}

// StopAllLocked does what StopLocked does for every start in progress, for a
// consumer shutting down. The caller holds the owner's lock.
func (g *StartGuard) StopAllLocked() {
	for _, c := range g.starting {
		c.stopped = true
	}
	g.starting = nil
}
