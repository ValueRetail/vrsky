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
// Two additions close the gaps that shape leaves (#423, #424). A consumer
// that writes status uses both:
//
//	claim, ok := s.starts.ReserveOrRetry(&s.mu, id, isActive, func() { s.start(ctx, id, tenant) })
//	...
//	if !claim.Activate(...) { ... }
//	claim.RunningStatus().Write(func() { s.setStatus(id, tenant, "running") })
//
// and in the stop handler
//
//	s.mu.Lock()
//	... cancel and delete ...
//	starting := s.starts.StopLocked(id)
//	var stopped sdk.StatusWrite
//	if wasActive { stopped = s.starts.StopStatusLocked(id) }
//	s.mu.Unlock()
//	stopped.Write(func() { s.setStatus(id, tenant, "stopped") })
//
// ReserveOrRetry keeps a start refused while another is in flight and runs it
// if that one gives up (#424). StatusWrite puts the start's "running" and the
// stop's "stopped" in the order of the changes that caused them, so a stop
// landing just after Activate does not leave the database saying "running"
// (#423).
//
// The zero value is ready to use. A StartGuard must not be copied after first
// use.
type StartGuard struct {
	// AfterReserve, when set, runs once a start holds its claim, after Reserve
	// has released the lock and before it returns. Tests use it to hold one
	// start inside its slow part while another arrives. Nil in production.
	AfterReserve func(connectionID string)

	// AfterActivate, when set, runs once Activate has put a start in the
	// owner's active map, after it has released the lock and before it
	// returns true. Tests use it to land a stop between Activate and the
	// start's "running" write (#423). Nil in production.
	AfterActivate func(connectionID string)

	// BeforeStatusWrite, when set, runs at the start of every StatusWrite.Write
	// that has a guard, before it waits for the connection's lane. Tests use it
	// to hold one write (a poller's final status, say) while a newer start
	// writes "running". Nil in production.
	BeforeStatusWrite func(connectionID string)

	// starting holds the claim of every start in progress, by connection ID.
	// Guarded by the owner's lock.
	starting map[string]*StartClaim

	// seq numbers each connection's status-changing events (an Activate, a
	// stop's StopStatusLocked) in the order they happen. Guarded by the
	// owner's lock.
	seq map[string]uint64

	// statusMu guards written and lanes. written holds, per connection, the
	// number of the latest status write that ran; a write older than that is
	// skipped. lanes serialise the writes of one connection, so a slow write
	// holds up only its own connection's; a lane is dropped when no write uses
	// it. seq and written are never pruned: one number per connection this
	// consumer has started or stopped, which stays small.
	statusMu sync.Mutex
	written  map[string]uint64
	lanes    map[string]*statusLane
}

// statusLane is held by one connection's status write while it runs.
type statusLane struct {
	mu    sync.Mutex
	users int // writes holding or waiting for mu; under the guard's statusMu
}

// StatusWrite is one status write's place in the order of a connection's
// starts and stops (#423). It is taken inside the critical section that makes
// the change (Activate, or the stop handler's), and the write runs later,
// outside the owner's lock, through Write. The zero value writes nothing.
type StatusWrite struct {
	guard        *StartGuard
	connectionID string
	seq          uint64
}

// Write runs write unless a write taken later for the same connection has
// already run: the state it would record is then out of date, and it is
// skipped. It reports whether write ran. A write counts as done even if the
// UPDATE inside it fails: the change it records did happen, so an older write
// must still not land after it. Writes for one connection run one at
// a time, under a lock of that connection's own (never the owner's lock), so
// a slow write holds up no other connection. write must not call Write for
// the same connection.
func (w StatusWrite) Write(write func()) bool {
	if w.guard == nil {
		return false
	}
	g := w.guard
	if g.BeforeStatusWrite != nil {
		g.BeforeStatusWrite(w.connectionID)
	}
	lane := g.enterLane(w.connectionID)
	defer g.leaveLane(w.connectionID, lane)

	lane.mu.Lock()
	defer lane.mu.Unlock()
	g.statusMu.Lock()
	stale := w.seq <= g.written[w.connectionID]
	if !stale {
		if g.written == nil {
			g.written = make(map[string]uint64)
		}
		g.written[w.connectionID] = w.seq
	}
	g.statusMu.Unlock()
	if stale {
		return false
	}
	write()
	return true
}

// enterLane returns connectionID's lane, creating it if no write holds one,
// and counts the caller as a user.
func (g *StartGuard) enterLane(connectionID string) *statusLane {
	g.statusMu.Lock()
	defer g.statusMu.Unlock()
	if g.lanes == nil {
		g.lanes = make(map[string]*statusLane)
	}
	l := g.lanes[connectionID]
	if l == nil {
		l = &statusLane{}
		g.lanes[connectionID] = l
	}
	l.users++
	return l
}

// leaveLane drops the caller's use of the lane, and the lane once unused.
func (g *StartGuard) leaveLane(connectionID string, l *statusLane) {
	g.statusMu.Lock()
	defer g.statusMu.Unlock()
	l.users--
	if l.users == 0 {
		delete(g.lanes, connectionID)
	}
}

// nextLocked numbers the next status-changing event for connectionID. The
// caller holds the owner's lock.
func (g *StartGuard) nextLocked(connectionID string) StatusWrite {
	if g.seq == nil {
		g.seq = make(map[string]uint64)
	}
	g.seq[connectionID]++
	return StatusWrite{guard: g, connectionID: connectionID, seq: g.seq[connectionID]}
}

// StopStatusLocked takes a stop's place in the connection's status order, for
// the "stopped" write the stop handler makes after unlocking. The caller holds
// the owner's lock, in the same critical section that cancels the connection.
// A start that activated earlier and has not yet written "running" then skips
// its write if this one runs first, and a start that activates after this
// stop is not overwritten by it.
//
// Take it only when the stop will write, that is when it cancelled an active
// connection: every consumer sees every stop command, and the guard keeps one
// counter per connection it has numbered.
func (g *StartGuard) StopStatusLocked(connectionID string) StatusWrite {
	return g.nextLocked(connectionID)
}

// StartClaim is one start's hold on a connection, from Reserve until Activate
// or Release.
type StartClaim struct {
	guard        *StartGuard
	mu           sync.Locker
	connectionID string
	stopped      bool        // set by StopLocked/StopAllLocked, under mu
	retry        func()      // a start refused while this one was in flight, under mu
	running      StatusWrite // this start's "running" write, set by Activate under mu
}

// Reserve claims connectionID for a start. mu is the lock that guards the
// owner's active map; Reserve takes it. active reports, with mu held, whether
// the connection is already active, and must not lock mu itself.
//
// It returns false when the connection is active or another start holds the
// claim; the caller then returns without starting anything. On true the caller
// must defer claim.Release().
func (g *StartGuard) Reserve(mu sync.Locker, connectionID string, active func() bool) (*StartClaim, bool) {
	return g.ReserveOrRetry(mu, connectionID, active, nil)
}

// ReserveOrRetry is Reserve for a start that must not be lost when it is
// refused because another start for the connection is in flight (#424).
// retry, normally the same start again, is kept on that start's claim and run
// by its Release if it gives up without activating. It is dropped if that
// start activates (the connection is up), if a stop arrives first (the stop
// is newer and wins), or on StopAllLocked. Of several starts refused by one
// claim, the latest one's retry is kept. A start refused because the
// connection is already active keeps nothing. retry may be nil.
func (g *StartGuard) ReserveOrRetry(mu sync.Locker, connectionID string, active func() bool, retry func()) (*StartClaim, bool) {
	mu.Lock()
	if active() {
		mu.Unlock()
		return nil, false
	}
	if busy, ok := g.starting[connectionID]; ok {
		if retry != nil {
			busy.retry = retry
		}
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
//
// On true the start's "running" write takes its place in the connection's
// status order; run it with RunningStatus().Write.
func (c *StartClaim) Activate(insert func()) bool {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return false
	}
	insert()
	c.retry = nil
	c.running = c.guard.nextLocked(c.connectionID)
	c.drop()
	c.mu.Unlock()

	if c.guard.AfterActivate != nil {
		c.guard.AfterActivate(c.connectionID)
	}
	return true
}

// RunningStatus is the start's "running" write, placed in the connection's
// status order by Activate. Write it once Activate has returned true: if a
// stop has cancelled the connection and written "stopped" meanwhile, the
// write is skipped. Before Activate, or after it returned false, it writes
// nothing.
func (c *StartClaim) RunningStatus() StatusWrite {
	if c == nil {
		return StatusWrite{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// Release drops the claim if it is still this start's, so a later start can
// try again after this one gave up. Deferred right after Reserve; a no-op
// after Activate, and after a stop took the claim (a newer start may hold the
// connection by then, and its claim is left alone). Safe on a nil claim.
//
// When it does drop the claim and a start was refused meanwhile through
// ReserveOrRetry, it runs that start's retry after unlocking, on this
// goroutine.
func (c *StartClaim) Release() {
	if c == nil {
		return
	}
	c.mu.Lock()
	var retry func()
	if c.guard.starting[c.connectionID] == c {
		retry = c.retry
	}
	c.retry = nil
	c.drop()
	c.mu.Unlock()
	if retry != nil {
		retry()
	}
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
	c.retry = nil
	delete(g.starting, connectionID)
	return true
}

// StopAllLocked does what StopLocked does for every start in progress, for a
// consumer shutting down. The caller holds the owner's lock.
func (g *StartGuard) StopAllLocked() {
	for _, c := range g.starting {
		c.stopped = true
		c.retry = nil
	}
	g.starting = nil
}
