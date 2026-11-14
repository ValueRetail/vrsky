package sdk

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/lib/pq"
)

// Retrying failed starts (#725, after #503 in the Front Systems consumer).
//
// A start that fails for a reason that passes (the database blinking during
// boot restore, a partner that does not answer) would otherwise leave the
// connection doing nothing while its row says "running", until someone
// stops and starts it. A consumer that routes its starts through
// StartGuard.StartRetrying has such a start retried in the background:
//
//	func (s *X) startConnection(ctx context.Context, id, tenant string) {
//		s.starts.StartRetrying(ctx, sdk.RetryOptions{Mu: &s.mu, DB: s.db, Logger: s.logger}, tenant, id,
//			func(ctx context.Context) error { return s.tryStart(ctx, id, tenant) })
//	}
//
// tryStart is the start as before, returning an error where it used to log
// and return. It reserves with ReserveOrRetryContext(ctx, …), passing the ctx
// it was given, so a stop that ends a retry also refuses that retry's
// reserve. The consumer decides what is worth retrying: only an error marked
// with TransientStart is retried (TransientIfDBDown marks the database not
// answering). Any other error, bad config, a refused address, credentials
// refused, a secret that cannot be read, is permanent and fails the start
// once, as before. nil covers every start that did not fail: activated,
// refused because the connection is active or starting, not this consumer's.
//
// The retry waits RetryPolicy.Delay (5 s doubling to 5 min, with jitter) and
// gives up 6 h after the first failure. Before each attempt it reads the row
// (ConnectionRunning) when RetryOptions.DB is set: a stop command is core
// NATS and can be missed, and the row is the record. It ends on a stop for
// the connection's tenant (StopLocked, StopForTenantLocked), on StopAllLocked,
// when the caller's ctx ends, when some start activates the connection, when
// the row no longer says "running", on a permanent failure, and on giving up.
// Each attempt is an ordinary start through the guard, so two never hold the
// claim at once and a start command arriving meanwhile runs at once.
//
// While a retry waits the row keeps saying "running", as it did before when
// a start failed: nothing is written until an attempt activates and writes
// "running" through RunningStatus. Giving up and a permanent failure write
// nothing either; a consumer that writes "error" for those does so itself.

// Default retry timing, the same as the Front Systems consumer's since #503.
const (
	DefaultStartRetryBase   = 5 * time.Second
	DefaultStartRetryMax    = 5 * time.Minute
	DefaultStartRetryGiveUp = 6 * time.Hour

	// rowCheckTimeout bounds the row read before each retry.
	rowCheckTimeout = 10 * time.Second
)

// RetryPolicy is the timing of start retries. The zero value is the
// defaults above.
type RetryPolicy struct {
	Base   time.Duration // wait before the first retry; 0 means DefaultStartRetryBase
	Max    time.Duration // longest wait; 0 means DefaultStartRetryMax
	GiveUp time.Duration // stop retrying this long after the first failure; 0 means DefaultStartRetryGiveUp, negative never
}

// Delay is the wait before retry n (from 1): Base doubled per retry up to
// Max, then a random point between half of that and all of it, so
// connections that failed together don't retry together.
func (p RetryPolicy) Delay(n int) time.Duration {
	base, maxDelay := p.Base, p.Max
	if base <= 0 {
		base = DefaultStartRetryBase
	}
	if maxDelay <= 0 {
		maxDelay = DefaultStartRetryMax
	}
	d := base
	for i := 1; i < n && d < maxDelay; i++ {
		d *= 2
	}
	if d > maxDelay {
		d = maxDelay
	}
	half := d / 2
	return half + rand.N(d-half+1)
}

// GiveUpAfter is how long after the first failure retries stop; ok is false
// when they never do.
func (p RetryPolicy) GiveUpAfter() (d time.Duration, ok bool) {
	switch {
	case p.GiveUp < 0:
		return 0, false
	case p.GiveUp == 0:
		return DefaultStartRetryGiveUp, true
	default:
		return p.GiveUp, true
	}
}

// givenUp reports whether retries that first failed at first are past the
// give-up time.
func (p RetryPolicy) givenUp(first time.Time) bool {
	d, ok := p.GiveUpAfter()
	return ok && time.Since(first) >= d
}

type transientStartError struct{ cause error }

func (e *transientStartError) Error() string { return e.cause.Error() }
func (e *transientStartError) Unwrap() error { return e.cause }

// TransientStart marks a failed start as one a retry may fix.
// StartRetrying retries it; any other error is permanent.
func TransientStart(err error) error {
	if err == nil {
		return nil
	}
	return &transientStartError{cause: err}
}

// IsTransientStart reports whether err (or anything it wraps) was marked
// with TransientStart.
func IsTransientStart(err error) bool {
	var te *transientStartError
	return errors.As(err, &te)
}

// TransientIfDBDown marks err with TransientStart when IsTransientDBError
// says it is the database not answering, and returns it unchanged
// otherwise. For the start's config and secret lookups: a connection that is
// not there is a plain "not mine", and a secret that is missing or refused is
// permanent.
func TransientIfDBDown(err error) error {
	if IsTransientDBError(err) {
		return TransientStart(err)
	}
	return err
}

// IsTransientDBError reports whether err looks like a database not
// answering (connection lost or refused, timeout, server starting or
// shutting down, too many connections) rather than a problem with the data
// or the request. sql.ErrNoRows and nil are not.
func IsTransientDBError(err error) bool {
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, sql.ErrConnDone) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var pe *pq.Error
	if errors.As(err, &pe) {
		switch pe.Code.Class() {
		case "08", "53", "57", "40": // connection, resources, operator intervention, rollback
			return true
		}
	}
	return false
}

// ConnectionRunning reports whether the tenant's connection row says
// "running". A row that is gone reports false.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func ConnectionRunning(ctx context.Context, db *sql.DB, connectionID, tenantID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, rowCheckTimeout)
	defer cancel()
	var status string
	err := db.QueryRowContext(ctx,
		`SELECT status FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "running", nil
}

// RetryOptions is what StartRetrying needs from the consumer.
type RetryOptions struct {
	// Mu is the owner's lock, the one passed to Reserve and held around the
	// stop methods. Required: without it the start runs once, unretried.
	Mu sync.Locker
	// DB, when set, is read before each retry (ConnectionRunning).
	DB *sql.DB
	// Logger gets the retry's log lines; nil means slog.Default().
	Logger *slog.Logger
	// Policy is the retry timing; the zero value is the defaults.
	Policy RetryPolicy
}

// pendingRetry is a connection's retry loop. Its fields other than ctx,
// cancel, tenantID and first are guarded by the owner's lock.
type pendingRetry struct {
	tenantID string
	ctx      context.Context // ends when the retry is no longer wanted
	cancel   context.CancelFunc
	first    time.Time // when the first failure was seen

	attempts   int  // retries made or waiting
	attempting bool // an attempt is running on the loop's goroutine
	// again is set when another start for the same tenant failed for a
	// reason that passes while this loop ran: the loop then goes on even if
	// its own attempt found nothing to do.
	again bool
	// queued is set when the loop's attempt was refused by another start's
	// claim and left its start queued there (ReserveOrRetryContext). That
	// start runs under e's context once the claim is released, possibly just
	// after the claim is gone from the guard, so the loop makes one more
	// pass rather than end and cancel it.
	queued bool
}

// stopWatch counts the stops for a tenant's connection while starts through
// StartRetrying are running for it, so a start that fails after a stop is
// not retried.
type stopWatch struct {
	users int
	stops uint64
}

// StartRetrying runs start for the tenant's connection and, when it fails
// with an error marked TransientStart, runs it again in the background on
// opts.Policy's schedule until it stops failing that way (see the comment at
// the top of startretry.go). start gets ctx on this call and the retry's own
// context on the retries; it must reserve with ReserveOrRetryContext and that
// context, and keep nothing that outlives its return under it (pollers run
// under a context of their own). It returns once the first attempt is done.
//
// One retry loop runs per tenant and connection: a transient failure while
// one is pending leaves it to that loop. Loops are kept apart by tenant, so a
// start naming the wrong tenant for a connection can neither take the right
// tenant's place nor end its retry. A permanent failure ends the pending
// retry of its own tenant.
func (g *StartGuard) StartRetrying(ctx context.Context, opts RetryOptions, tenantID, connectionID string, start func(ctx context.Context) error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("connection_id", connectionID, "tenant_id", tenantID)
	mu := opts.Mu
	if mu == nil {
		if err := start(ctx); err != nil {
			logger.Warn("Start failed; not retried (no lock given)", "error", err)
		}
		return
	}
	key := statusKey{tenantID: tenantID, connectionID: connectionID}

	mu.Lock()
	w := g.watchLocked(key)
	stops := w.stops
	mu.Unlock()

	err := start(ctx)

	mu.Lock()
	stopped := w.stops != stops
	g.unwatchLocked(key, w)
	if err == nil {
		mu.Unlock()
		return
	}
	if !IsTransientStart(err) {
		if e := g.retries[key]; e != nil {
			g.endRetryLocked(key, e)
		}
		mu.Unlock()
		// Debug: the consumer logged the failure itself, and every consumer
		// sees every start command, so "not mine" failures land here too.
		logger.Debug("Start failed for a reason a retry does not fix; not retried", "error", err)
		return
	}
	var why string
	switch e := g.retries[key]; {
	case g.retryClosed:
		why = "shutting down"
	case stopped:
		why = "a stop arrived while it ran"
	case ctx.Err() != nil:
		why = "its context ended"
	case e != nil:
		e.again = true
		mu.Unlock()
		logger.Warn("Start failed; a retry is already pending", "error", err)
		return
	}
	if why != "" {
		mu.Unlock()
		logger.Warn("Start failed; not retried: "+why, "error", err)
		return
	}
	rctx, cancel := context.WithCancel(ctx)
	e := &pendingRetry{tenantID: tenantID, ctx: rctx, cancel: cancel, first: time.Now()}
	if g.retries == nil {
		g.retries = make(map[statusKey]*pendingRetry)
	}
	g.retries[key] = e
	g.retryWG.Add(1)
	mu.Unlock()
	logger.Warn("Start failed for a reason that may pass; retrying in the background", "error", err)
	go g.retryLoop(e, key, opts, mu, logger, start)
}

// retryLoop retries the connection's start while e is its pending retry. It
// runs on its own goroutine, counted by retryWG, and ends with e: everything
// that ends e cancels its ctx. The loop removes e itself in the critical
// section that decides to end, so a start failing at the same moment either
// finds e and leaves the retry to it (again) or finds none and starts a loop
// of its own.
func (g *StartGuard) retryLoop(e *pendingRetry, key statusKey, opts RetryOptions, mu sync.Locker, logger *slog.Logger, start func(context.Context) error) {
	defer g.retryWG.Done()
	defer e.cancel()
	// end removes e if it is still the pending retry. The caller holds mu.
	end := func() {
		if g.retries[key] == e {
			delete(g.retries, key)
		}
	}
	// stop ends the loop for a reason found outside the lock.
	stop := func() {
		mu.Lock()
		end()
		mu.Unlock()
	}
	for {
		mu.Lock()
		e.attempts++
		n := e.attempts
		mu.Unlock()
		t := time.NewTimer(opts.Policy.Delay(n))
		select {
		case <-e.ctx.Done():
			t.Stop()
			stop()
			logger.Info("Start retries ended: stopped, started, or shutting down")
			return
		case <-t.C:
		}
		if g.BeforeRetryAttempt != nil {
			g.BeforeRetryAttempt(key.connectionID)
		}

		var err error
		if opts.DB != nil {
			running, rerr := ConnectionRunning(e.ctx, opts.DB, key.connectionID, key.tenantID)
			switch {
			case e.ctx.Err() != nil:
				stop()
				return
			case rerr != nil && IsTransientDBError(rerr):
				err = TransientStart(fmt.Errorf("check connection status: %w", rerr))
			case rerr != nil:
				stop()
				logger.Warn("Start retries end: the connection's row could not be read", "error", rerr)
				return
			case !running:
				stop()
				logger.Info("Start retries end: the connection is no longer running")
				return
			}
		}
		if err == nil {
			mu.Lock()
			e.again = false
			e.queued = false
			e.attempting = true
			mu.Unlock()
			logger.Info("Retrying start", "attempt", n)
			err = start(e.ctx)
			mu.Lock()
			e.attempting = false
			mu.Unlock()
		}

		mu.Lock()
		var why string
		switch {
		case g.retries[key] != e || e.ctx.Err() != nil:
			// Ended by a stop, an activation, a permanent failure or Stop.
			why = "-"
		case err == nil && !e.again && !e.queued && g.starting[key.connectionID] == nil:
			// Nothing to do: active already, or not this consumer's. A start
			// still holding the claim may give up (one for another tenant
			// finding the connection is not its own), and the start this
			// attempt queued on it runs under e's context: the loop goes on
			// until that claim is gone.
			why = "-"
		case err != nil && !IsTransientStart(err):
			why = "failed for a reason a retry does not fix"
		case opts.Policy.givenUp(e.first):
			why = "giving up"
		}
		if why != "" {
			end()
		}
		mu.Unlock()
		switch why {
		case "":
		case "-":
			return
		case "giving up":
			logger.Error("Start still failing; giving up. Stop and start the connection once the cause is fixed",
				"error", err, "since", e.first)
			return
		default:
			logger.Warn("Start retry "+why+"; retries end", "error", err)
			return
		}
		if err != nil {
			logger.Warn("Start retry failed; trying again", "attempt", n, "error", err)
		}
	}
}

// watchLocked registers a start in progress for key and returns its stop
// counter. The caller holds the owner's lock.
func (g *StartGuard) watchLocked(key statusKey) *stopWatch {
	if g.watches == nil {
		g.watches = make(map[statusKey]*stopWatch)
	}
	w := g.watches[key]
	if w == nil {
		w = &stopWatch{}
		g.watches[key] = w
	}
	w.users++
	return w
}

// unwatchLocked drops a start's use of w, and w once unused. The caller
// holds the owner's lock.
func (g *StartGuard) unwatchLocked(key statusKey, w *stopWatch) {
	w.users--
	if w.users == 0 && g.watches[key] == w {
		delete(g.watches, key)
	}
}

// endRetryLocked ends e and removes it if it is still key's pending retry.
// The caller holds the owner's lock.
func (g *StartGuard) endRetryLocked(key statusKey, e *pendingRetry) {
	if g.retries[key] == e {
		delete(g.retries, key)
	}
	e.cancel()
}

// stopRetryLocked is a stop's part in retries: the pending retry ends and a
// start in progress through StartRetrying will not be retried. With a
// tenant, only that tenant's; with "" (StopLocked, which names none), every
// tenant's. The caller holds the owner's lock.
func (g *StartGuard) stopRetryLocked(tenantID, connectionID string) {
	for key, e := range g.retries {
		if key.connectionID == connectionID && (tenantID == "" || key.tenantID == tenantID) {
			g.endRetryLocked(key, e)
		}
	}
	for key, w := range g.watches {
		if key.connectionID == connectionID && (tenantID == "" || key.tenantID == tenantID) {
			w.stops++
		}
	}
}

// otherTenantRetryLocked reports the tenant of a retry pending for the
// connection under another tenant than tenantID, when tenantID has none.
// The caller holds the owner's lock.
func (g *StartGuard) otherTenantRetryLocked(tenantID, connectionID string) (string, bool) {
	if _, ok := g.retries[statusKey{tenantID: tenantID, connectionID: connectionID}]; ok {
		return "", false
	}
	for key := range g.retries {
		if key.connectionID == connectionID {
			return key.tenantID, true
		}
	}
	return "", false
}

// activatedLocked ends the connection's pending retries once a start has
// activated it. A retry whose own attempt activated is removed but not
// cancelled here: its context is still the attempt's, and the loop cancels
// it once the attempt returns. The caller holds the owner's lock.
func (g *StartGuard) activatedLocked(connectionID string) {
	for key, e := range g.retries {
		if key.connectionID != connectionID {
			continue
		}
		delete(g.retries, key)
		if !e.attempting {
			e.cancel()
		}
	}
}
