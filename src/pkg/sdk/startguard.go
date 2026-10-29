package sdk

import (
	"fmt"
	"strings"
	"sync"
)

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
//	claim, ok := s.starts.ReserveOrRetry(&s.mu, tenant, id, isActive, func() { s.start(ctx, id, tenant) })
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
//	if wasActive { stopped = s.starts.StopStatusLocked(tenant, id) }
//	s.mu.Unlock()
//	stopped.Write(func() { s.setStatus(id, tenant, "stopped") })
//
// A stop handler checks the command's tenant first (#498), and calls this in
// place of StopLocked:
//
//	s.mu.Lock()
//	starting, err := s.starts.StopForTenantLocked(msg.Subject, cmd.TenantID, id)
//	if err != nil {
//		s.mu.Unlock()
//		logger.Warn("Ignoring stop command", "connection_id", id, "error", err)
//		return
//	}
//	... cancel and delete, StopStatusLocked as above ...
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

	// owners holds, by connection ID, the tenant the latest Activate was for:
	// the tenant of the owner's active entry while it has one, since every
	// entry goes in through Activate. StopForTenantLocked checks a stop
	// against it (#498). Guarded by the owner's lock. Like seq it is never
	// pruned: an entry the owner dropped leaves its tenant behind, a stop
	// naming that tenant passes and finds nothing to stop, and a connection
	// belongs to one tenant for life. An empty tenant (a start through
	// Reserve) is not checked.
	owners map[string]string

	// seq numbers each connection's status-changing events (an Activate, a
	// stop's StopStatusLocked) in the order they happen. Guarded by the
	// owner's lock. It, written and lanes are keyed by tenant and connection,
	// so a command carrying the wrong tenant for a connection cannot move
	// another tenant's order or skip its writes.
	seq map[statusKey]uint64

	// statusMu guards written and lanes. written holds, per connection, the
	// number of the latest status write that ran; a write older than that is
	// skipped. lanes serialise the writes of one connection, so a slow write
	// holds up only its own connection's; a lane is dropped when no write uses
	// it. seq and written are never pruned: one number per connection this
	// consumer has started or stopped, which stays small.
	statusMu sync.Mutex
	written  map[statusKey]uint64
	lanes    map[statusKey]*statusLane
}

// statusKey is one tenant's connection, the unit status writes are ordered in.
type statusKey struct{ tenantID, connectionID string }

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
	guard *StartGuard
	key   statusKey
	seq   uint64
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
		g.BeforeStatusWrite(w.key.connectionID)
	}
	lane := g.enterLane(w.key)
	defer g.leaveLane(w.key, lane)

	lane.mu.Lock()
	defer lane.mu.Unlock()
	g.statusMu.Lock()
	stale := w.seq <= g.written[w.key]
	if !stale {
		if g.written == nil {
			g.written = make(map[statusKey]uint64)
		}
		g.written[w.key] = w.seq
	}
	g.statusMu.Unlock()
	if stale {
		return false
	}
	write()
	return true
}

// enterLane returns key's lane, creating it if no write holds one, and counts
// the caller as a user.
func (g *StartGuard) enterLane(key statusKey) *statusLane {
	g.statusMu.Lock()
	defer g.statusMu.Unlock()
	if g.lanes == nil {
		g.lanes = make(map[statusKey]*statusLane)
	}
	l := g.lanes[key]
	if l == nil {
		l = &statusLane{}
		g.lanes[key] = l
	}
	l.users++
	return l
}

// leaveLane drops the caller's use of the lane, and the lane once unused.
func (g *StartGuard) leaveLane(key statusKey, l *statusLane) {
	g.statusMu.Lock()
	defer g.statusMu.Unlock()
	l.users--
	if l.users == 0 {
		delete(g.lanes, key)
	}
}

// nextLocked numbers the next status-changing event for the tenant's
// connection. The caller holds the owner's lock.
func (g *StartGuard) nextLocked(tenantID, connectionID string) StatusWrite {
	if g.seq == nil {
		g.seq = make(map[statusKey]uint64)
	}
	k := statusKey{tenantID: tenantID, connectionID: connectionID}
	g.seq[k]++
	return StatusWrite{guard: g, key: k, seq: g.seq[k]}
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
//
// tenantID is the tenant the stop names, the one its UPDATE filters on. A stop
// handler that checks the tenant first with StopForTenantLocked (#498) only
// gets here for the start's own tenant. Should a stop naming another tenant
// get here anyway, it takes its place in that tenant's order, not the start's,
// so it can neither skip the start's writes nor be skipped by them.
func (g *StartGuard) StopStatusLocked(tenantID, connectionID string) StatusWrite {
	return g.nextLocked(tenantID, connectionID)
}

// StartClaim is one start's hold on a connection, from Reserve until Activate
// or Release.
type StartClaim struct {
	guard        *StartGuard
	mu           sync.Locker
	connectionID string
	tenantID     string            // the tenant whose order RunningStatus joins; "" through Reserve
	stopped      bool              // set by StopLocked/StopAllLocked, under mu
	retry        map[string]func() // starts refused while this one was in flight, by tenant, under mu
	running      StatusWrite       // this start's "running" write, set by Activate under mu
}

// Reserve claims connectionID for a start. mu is the lock that guards the
// owner's active map; Reserve takes it. active reports, with mu held, whether
// the connection is already active, and must not lock mu itself.
//
// It returns false when the connection is active or another start holds the
// claim; the caller then returns without starting anything. On true the caller
// must defer claim.Release().
func (g *StartGuard) Reserve(mu sync.Locker, connectionID string, active func() bool) (*StartClaim, bool) {
	return g.ReserveOrRetry(mu, "", connectionID, active, nil)
}

// ReserveOrRetry is Reserve for a start that must not be lost when it is
// refused because another start for the connection is in flight (#424).
// retry, normally the same start again, is kept on that start's claim and run
// by its Release if it gives up without activating. It is dropped if that
// start activates (the connection is up), if a stop arrives first (the stop
// is newer and wins), or on StopAllLocked. Of several starts refused by one
// claim, the latest one's retry is kept for each tenant they name, so a start
// naming the wrong tenant cannot displace the right one's (its own retry
// fails the tenant-scoped lookup). A start refused because the connection is
// already active keeps nothing. retry may be nil.
//
// tenantID is the tenant the start was given, the one its "running" UPDATE
// filters on; RunningStatus takes its place in that tenant's order for the
// connection.
func (g *StartGuard) ReserveOrRetry(mu sync.Locker, tenantID, connectionID string, active func() bool, retry func()) (*StartClaim, bool) {
	mu.Lock()
	if active() {
		mu.Unlock()
		return nil, false
	}
	if busy, ok := g.starting[connectionID]; ok {
		if retry != nil {
			if busy.retry == nil {
				busy.retry = make(map[string]func())
			}
			busy.retry[tenantID] = retry
		}
		mu.Unlock()
		return nil, false
	}
	if g.starting == nil {
		g.starting = make(map[string]*StartClaim)
	}
	c := &StartClaim{guard: g, mu: mu, connectionID: connectionID, tenantID: tenantID}
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
	if c.guard.owners == nil {
		c.guard.owners = make(map[string]string)
	}
	c.guard.owners[c.connectionID] = c.tenantID
	c.running = c.guard.nextLocked(c.tenantID, c.connectionID)
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
	var retries map[string]func()
	if c.guard.starting[c.connectionID] == c {
		retries = c.retry
	}
	c.retry = nil
	c.drop()
	c.mu.Unlock()
	// One per tenant named; normally just the one. The first that activates
	// makes the others find the connection active.
	for _, retry := range retries {
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

// StopForTenantLocked is StopLocked for a stop command, checked against the
// tenant the command names (#498). subject is the subject the command arrived
// on, vrsky.commands.{tenant}.connection.stop; tenantID is the tenant in its
// payload. The caller holds the owner's lock and calls it before it touches
// its active map.
//
// It refuses the stop with a *StopTenantMismatch when the subject's tenant is
// not tenantID, when a start in progress for the connection is for another
// tenant, or when the latest Activate for it was for another tenant. The
// caller then changes nothing (no cancel, no delete, no status write), logs
// the error as a warning and returns. The one thing a refused stop does is
// drop a start for its own tenant that ReserveOrRetry queued on another
// tenant's claim: the stop is newer, as in StopLocked.
//
// Otherwise it does what StopLocked does and returns its result. A connection
// this owner neither started nor is starting passes, so a stop meant for
// another consumer finds nothing here, as before. A start through Reserve
// records no tenant, and stops for it are checked against the subject only.
func (g *StartGuard) StopForTenantLocked(subject, tenantID, connectionID string) (bool, error) {
	if st, ok := CommandSubjectTenant(subject); !ok || st != tenantID {
		return false, &StopTenantMismatch{ConnectionID: connectionID, TenantID: tenantID, Other: st, Subject: true}
	}
	if c, ok := g.starting[connectionID]; ok {
		if c.tenantID != "" && c.tenantID != tenantID {
			delete(c.retry, tenantID)
			return false, &StopTenantMismatch{ConnectionID: connectionID, TenantID: tenantID, Other: c.tenantID, Starting: true}
		}
	} else if owner := g.owners[connectionID]; owner != "" && owner != tenantID {
		return false, &StopTenantMismatch{ConnectionID: connectionID, TenantID: tenantID, Other: owner}
	}
	return g.StopLocked(connectionID), nil
}

// StopTenantMismatch is why StopForTenantLocked refused a stop. Its message
// holds the connection ID and the tenant IDs, nothing else from the command.
type StopTenantMismatch struct {
	ConnectionID string
	TenantID     string // the tenant the command's payload names
	Other        string // the subject's tenant, or the tenant the connection is starting or active for here
	Subject      bool   // Other is the subject's tenant ("" if the subject is not a command subject)
	Starting     bool   // Other is the tenant of a start in progress
}

func (e *StopTenantMismatch) Error() string {
	switch {
	case e.Subject && e.Other == "":
		return fmt.Sprintf("stop for connection %s names tenant %q but its subject is not a connection command subject", e.ConnectionID, e.TenantID)
	case e.Subject:
		return fmt.Sprintf("stop for connection %s names tenant %q but its subject names tenant %q", e.ConnectionID, e.TenantID, e.Other)
	case e.Starting:
		return fmt.Sprintf("stop for connection %s names tenant %q but it is starting here for tenant %q", e.ConnectionID, e.TenantID, e.Other)
	default:
		return fmt.Sprintf("stop for connection %s names tenant %q but it is active here for tenant %q", e.ConnectionID, e.TenantID, e.Other)
	}
}

// CommandSubjectTenant returns the tenant token of a connection command
// subject, vrsky.commands.{tenant}.connection.{verb}. ok is false when subject
// is not one.
func CommandSubjectTenant(subject string) (string, bool) {
	p := strings.Split(subject, ".")
	if len(p) != 5 || p[0] != "vrsky" || p[1] != "commands" || p[2] == "" || p[3] != "connection" || p[4] == "" {
		return "", false
	}
	return p[2], true
}
