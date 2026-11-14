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
}

// stopWatch counts the stops for a connection while starts through
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
// One retry loop runs per connection. A transient failure while one is
// pending for the same tenant leaves it to that loop; one for another tenant
// is logged and not retried. A permanent failure ends a pending retry for the
// same tenant.
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

	mu.Lock()
	w := g.watchLocked(connectionID)
	stops := w.stops
	mu.Unlock()

	err := start(ctx)

	mu.Lock()
	stopped := w.stops != stops
	g.unwatchLocked(connectionID, w)
	if err == nil {
		mu.Unlock()
		return
	}
	if !IsTransientStart(err) {
		if e := g.retries[connectionID]; e != nil && e.tenantID == tenantID {
			g.endRetryLocked(connectionID, e)
		}
		mu.Unlock()
		// Debug: the consumer logged the failure itself, and every consumer
		// sees every start command, so "not mine" failures land here too.
		logger.Debug("Start failed for a reason a retry does not fix; not retried", "error", err)
		return
	}
	var why string
	switch e := g.retries[connectionID]; {
	case g.retryClosed:
		why = "shutting down"
	case stopped:
		why = "a stop arrived while it ran"
	case ctx.Err() != nil:
		why = "its context ended"
	case e != nil && e.tenantID == tenantID:
		e.again = true
		mu.Unlock()
		logger.Warn("Start failed; a retry is already pending", "error", err)
		return
	case e != nil:
		why = "a retry for another tenant is pending"
	}
	if why != "" {
		mu.Unlock()
		logger.Warn("Start failed; not retried: "+why, "error", err)
		return
	}
	rctx, cancel := context.WithCancel(ctx)
	e := &pendingRetry{tenantID: tenantID, ctx: rctx, cancel: cancel, first: time.Now()}
	if g.retries == nil {
		g.retries = make(map[string]*pendingRetry)
	}
	g.retries[connectionID] = e
	g.retryWG.Add(1)
	mu.Unlock()
	logger.Warn("Start failed for a reason that may pass; retrying in the background", "error", err)
	go g.retryLoop(e, connectionID, opts, mu, logger, start)
}

// retryLoop retries the connection's start while e is its pending retry. It
// runs on its own goroutine, counted by retryWG, and ends with e: everything
// that ends e cancels its ctx.
func (g *StartGuard) retryLoop(e *pendingRetry, connectionID string, opts RetryOptions, mu sync.Locker, logger *slog.Logger, start func(context.Context) error) {
	defer g.retryWG.Done()
	defer func() {
		mu.Lock()
		if g.retries[connectionID] == e {
			delete(g.retries, connectionID)
		}
		mu.Unlock()
		e.cancel()
	}()
	for {
		mu.Lock()
		e.attempts++
		n := e.attempts
		mu.Unlock()
		t := time.NewTimer(opts.Policy.Delay(n))
		select {
		case <-e.ctx.Done():
			t.Stop()
			logger.Info("Start retries ended: stopped, started, or shutting down")
			return
		case <-t.C:
		}
		if g.BeforeRetryAttempt != nil {
			g.BeforeRetryAttempt(connectionID)
		}

		var err error
		if opts.DB != nil {
			running, rerr := ConnectionRunning(e.ctx, opts.DB, connectionID, e.tenantID)
			switch {
			case e.ctx.Err() != nil:
				return
			case rerr != nil && IsTransientDBError(rerr):
				err = TransientStart(fmt.Errorf("check connection status: %w", rerr))
			case rerr != nil:
				logger.Warn("Start retries end: the connection's row could not be read", "error", rerr)
				return
			case !running:
				logger.Info("Start retries end: the connection is no longer running")
				return
			}
		}
		if err == nil {
			mu.Lock()
			e.again = false
			e.attempting = true
			mu.Unlock()
			logger.Info("Retrying start", "attempt", n)
			err = start(e.ctx)
			mu.Lock()
			e.attempting = false
			mu.Unlock()
		}

		mu.Lock()
		current := g.retries[connectionID] == e && e.ctx.Err() == nil
		again := e.again
		mu.Unlock()
		switch {
		case !current:
			return
		case err == nil && !again:
			// Activated (Activate ended e, so not current), or nothing to do:
			// active already, or not this consumer's.
			return
		case err != nil && !IsTransientStart(err):
			logger.Warn("Start retry failed for a reason a retry does not fix; retries end", "error", err)
			return
		}
		if opts.Policy.givenUp(e.first) {
			logger.Error("Start still failing; giving up. Stop and start the connection once the cause is fixed",
				"error", err, "since", e.first)
			return
		}
		if err != nil {
			logger.Warn("Start retry failed; trying again", "attempt", n, "error", err)
		}
	}
}

// watchLocked registers a start in progress for connectionID and returns
// its stop counter. The caller holds the owner's lock.
func (g *StartGuard) watchLocked(connectionID string) *stopWatch {
	if g.watches == nil {
		g.watches = make(map[string]*stopWatch)
	}
	w := g.watches[connectionID]
	if w == nil {
		w = &stopWatch{}
		g.watches[connectionID] = w
	}
	w.users++
	return w
}

// unwatchLocked drops a start's use of w, and w once unused. The caller
// holds the owner's lock.
func (g *StartGuard) unwatchLocked(connectionID string, w *stopWatch) {
	w.users--
	if w.users == 0 && g.watches[connectionID] == w {
		delete(g.watches, connectionID)
	}
}

// endRetryLocked ends e and removes it if it is still the connection's
// pending retry. The caller holds the owner's lock.
func (g *StartGuard) endRetryLocked(connectionID string, e *pendingRetry) {
	if g.retries[connectionID] == e {
		delete(g.retries, connectionID)
	}
	e.cancel()
}

// stopRetryLocked is a stop's part in retries: a pending retry ends and a
// start in progress through StartRetrying will not be retried. The caller
// holds the owner's lock.
func (g *StartGuard) stopRetryLocked(connectionID string) {
	if e := g.retries[connectionID]; e != nil {
		g.endRetryLocked(connectionID, e)
	}
	if w := g.watches[connectionID]; w != nil {
		w.stops++
	}
}

// activatedLocked ends the connection's pending retry once a start has
// activated it. A retry whose own attempt activated is removed but not
// cancelled here: its context is still the attempt's, and the loop cancels
// it once the attempt returns. The caller holds the owner's lock.
func (g *StartGuard) activatedLocked(connectionID string) {
	e := g.retries[connectionID]
	if e == nil {
		return
	}
	delete(g.retries, connectionID)
	if !e.attempting {
		e.cancel()
	}
}
