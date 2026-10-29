package main

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
	"net/http"
	"syscall"
	"time"

	"github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Retrying failed starts (#503, decided by the owner before merge).
//
// Since #503 the webhook route publishes only for a connection this process
// has started. A start that fails for a reason that passes (the database
// blinking during boot restore, Front Systems down or answering 5xx, 429 or a
// timeout) would otherwise leave the connection refusing events while its row
// says "running", until someone stops and starts it.
//
// So a transient failure is retried in the background, after 5 s doubling to
// 5 min, with jitter, for as long as the row says "running", up to
// retryGiveUp. While a retry waits, the route answers 503 rather than 410.
// Retries end on a stop command for the connection's tenant, when the row is
// gone or no longer "running", when some start activates the connection, and
// on Stop. Every attempt goes through the StartGuard like any other start, so
// two never run at once.
//
// A permanent failure (Front Systems refusing the registration with a 4xx
// other than 408 or 429, a bad base URL, a secret that can't be read) is not
// retried: the row is set to "error" with the reason in last_error, so the UI
// shows it and the connection can be started again once it's fixed. Giving up
// after retryGiveUp does the same.

const (
	defaultRetryBase   = 5 * time.Second
	defaultRetryMax    = 5 * time.Minute
	defaultRetryGiveUp = 6 * time.Hour

	// statusWriteTimeout bounds the row check and the "error" write.
	statusWriteTimeout = 10 * time.Second
)

// startOutcome is how one start attempt ended.
type startOutcome int

const (
	startRefused   startOutcome = iota // the connection is active, or another start holds the claim
	startActivated                     // the connection is active now
	startAbandoned                     // a stop took the claim back mid-start
	startNotOurs                       // no such connection for this tenant, or no Front Systems consumer node in it
	startTransient                     // failed for a reason a retry may fix
	startPermanent                     // failed for a reason only a change to the connection fixes
)

// startResult is one attempt's outcome. p is the pending start it ran for
// (nil when it ran untracked: the consumer is closed, or a start for another
// tenant is pending). reason is the last_error text for a permanent failure:
// no response bodies, no secrets. updatedAt is the row's updated_at as the
// attempt read it (zero if it didn't get that far), the version an "error"
// write is allowed to replace. claim is the attempt's claim, still held: the
// caller releases it after afterAttempt, so a start queued behind it runs
// only once this attempt's outcome is recorded (#503 review).
type startResult struct {
	outcome   startOutcome
	p         *pendingStart
	err       error
	reason    string
	updatedAt time.Time
	claim     *sdk.StartClaim
}

// startError is a start failure that knows which outcome it is.
type startError struct {
	kind   startOutcome
	err    error
	reason string
}

func (e *startError) Error() string { return e.err.Error() }
func (e *startError) Unwrap() error { return e.err }

// failedStart is the result for err. A *startError in err decides the
// outcome; otherwise kind and reason do.
func failedStart(p *pendingStart, err error, kind startOutcome, reason string) startResult {
	var se *startError
	if errors.As(err, &se) {
		kind, reason = se.kind, se.reason
	}
	return startResult{outcome: kind, p: p, err: err, reason: reason}
}

// pendingStart is a start that is wanted and has not activated yet.
type pendingStart struct {
	tenantID string
	// ctx ends when the start is no longer wanted: a stop for its tenant, an
	// activation, a permanent failure, giving up, or Stop. The retry loop
	// waits on it.
	ctx    context.Context
	cancel context.CancelFunc
	// Under the consumer's mu.
	looping      bool      // a retry loop runs for it
	firstFailure time.Time // when the first transient failure was seen
	attempts     int       // retries made so far
	updatedAt    time.Time // the row's updated_at as last read, zero if never
}

// attachPendingLocked returns the connection's pending start for tenantID,
// making one if there is none. created reports a new one. It returns nil
// when the consumer is closed or a start for another tenant is pending (that
// start keeps its place; this one runs untracked). The caller holds mu.
func (c *frontSystemsConsumer) attachPendingLocked(connectionID, tenantID string) (p *pendingStart, created bool) {
	if c.closed {
		return nil, false
	}
	if p, ok := c.pending[connectionID]; ok {
		if p.tenantID == tenantID {
			return p, false
		}
		return nil, false
	}
	ctx, cancel := context.WithCancel(c.lifecycleCtxLocked())
	p = &pendingStart{tenantID: tenantID, ctx: ctx, cancel: cancel}
	if c.pending == nil {
		c.pending = make(map[string]*pendingStart)
	}
	c.pending[connectionID] = p
	return p, true
}

// dropPendingLocked ends p if it is still the connection's pending start. A
// nil p, or one already replaced, is left alone. The caller holds mu.
func (c *frontSystemsConsumer) dropPendingLocked(connectionID string, p *pendingStart) {
	if p == nil || c.pending[connectionID] != p {
		return
	}
	p.cancel()
	delete(c.pending, connectionID)
}

// afterAttempt acts on a start attempt's outcome: a transient failure starts
// the retry loop (or leaves it to the one running), a permanent one or giving
// up marks the connection "error", and anything else ends the pending start.
func (c *frontSystemsConsumer) afterAttempt(connectionID, tenantID string, res startResult) {
	logger := c.logger.With("connection_id", connectionID, "tenant_id", tenantID)
	p := res.p
	if p != nil && !res.updatedAt.IsZero() {
		c.mu.Lock()
		p.updatedAt = res.updatedAt
		c.mu.Unlock()
	}
	switch res.outcome {
	case startRefused, startActivated:
		// Refused: whoever holds the claim, or the active connection, owns the
		// pending start. Activated: Activate ended it.
		return
	case startTransient:
		c.mu.Lock()
		if p == nil || c.pending[connectionID] != p || c.closed {
			c.mu.Unlock()
			logger.Warn("Front Systems start failed; not retried (stopped, shutting down, or another tenant's start is pending)", "error", res.err)
			return
		}
		now := time.Now()
		if p.firstFailure.IsZero() {
			p.firstFailure = now
		}
		if since := p.firstFailure; now.Sub(since) >= c.giveUpAfter() {
			version := p.updatedAt
			c.dropPendingLocked(connectionID, p)
			c.mu.Unlock()
			logger.Error("Front Systems start still failing; giving up and marking the connection error",
				"error", res.err, "since", since)
			c.markError(connectionID, tenantID,
				fmt.Sprintf("Front Systems: could not start for %s; the last attempt failed: %s", c.giveUpAfter(), failureClass(res.err)),
				version, logger)
			return
		}
		if !p.looping {
			p.looping = true
			c.bg.Add(1)
			go c.retryLoop(connectionID, p)
		}
		c.mu.Unlock()
		logger.Warn("Front Systems start failed; retrying", "error", res.err)
	case startPermanent:
		c.mu.Lock()
		current := p != nil && c.pending[connectionID] == p
		var version time.Time
		if p != nil {
			version = p.updatedAt
		}
		c.dropPendingLocked(connectionID, p)
		// Starts queued behind this one (boot restore and a start command, say)
		// were asked for before it failed for good; they'd fail the same way.
		if c.permanentFailures == nil {
			c.permanentFailures = make(map[string]uint64)
		}
		c.permanentFailures[connectionID]++
		c.mu.Unlock()
		logger.Error("Front Systems start failed and will not be retried", "error", res.err)
		if current {
			c.markError(connectionID, tenantID, res.reason, version, logger)
		}
	default: // startNotOurs, startAbandoned
		if res.outcome == startNotOurs {
			logger.Debug("Not a Front Systems consumer for this connection", "error", res.err)
		}
		c.mu.Lock()
		c.dropPendingLocked(connectionID, p)
		c.mu.Unlock()
	}
}

// retryLoop retries the connection's start while p is its pending start. It
// runs on its own goroutine, counted by bg, and ends with p: everything that
// ends p cancels its ctx.
func (c *frontSystemsConsumer) retryLoop(connectionID string, p *pendingStart) {
	defer c.bg.Done()
	logger := c.logger.With("connection_id", connectionID, "tenant_id", p.tenantID)
	defer func() {
		c.mu.Lock()
		c.dropPendingLocked(connectionID, p)
		c.mu.Unlock()
	}()
	for {
		c.mu.Lock()
		p.attempts++
		delay := c.retryDelay(p.attempts)
		c.mu.Unlock()
		t := time.NewTimer(delay)
		select {
		case <-p.ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}

		// A stop command can be missed (core NATS); the row is the record.
		running, updatedAt, err := c.rowRunning(p.ctx, connectionID, p.tenantID)
		var res startResult
		switch {
		case p.ctx.Err() != nil:
			return
		case err != nil:
			res = startResult{outcome: startTransient, p: p,
				err: &startError{kind: startTransient, err: fmt.Errorf("check connection status: %w", err)}}
		case !running:
			logger.Info("Front Systems start not retried any more: the connection is no longer running")
			return
		default:
			c.mu.Lock()
			p.updatedAt = updatedAt
			c.mu.Unlock()
			logger.Info("Retrying Front Systems start", "attempt", p.attempts)
			res = c.attemptStart(connectionID, p.tenantID, p, nil)
		}
		// Once p has ended (activated, stopped, given up), its ctx is done and
		// the wait above returns at once.
		c.afterAttempt(connectionID, p.tenantID, res)
		res.claim.Release()
	}
}

// retryDelay is the wait before retry n (from 1): retryBase doubled per
// retry up to retryMax, then between half of that and all of it, so
// connections that failed together don't retry together.
func (c *frontSystemsConsumer) retryDelay(n int) time.Duration {
	base, maxDelay := c.retryBase, c.retryMax
	if base <= 0 {
		base = defaultRetryBase
	}
	if maxDelay <= 0 {
		maxDelay = defaultRetryMax
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

func (c *frontSystemsConsumer) giveUpAfter() time.Duration {
	if c.retryGiveUp > 0 {
		return c.retryGiveUp
	}
	return defaultRetryGiveUp
}

// rowRunning reports whether the connection's row still says "running", and
// its updated_at. A row that is gone reports false.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (c *frontSystemsConsumer) rowRunning(ctx context.Context, connectionID, tenantID string) (bool, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, statusWriteTimeout)
	defer cancel()
	var (
		status    string
		updatedAt time.Time
	)
	err := c.db.QueryRowContext(ctx,
		`SELECT status, updated_at FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&status, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, err
	}
	return status == "running", updatedAt, nil
}

// markError records a start that won't be retried: status "error" and the
// reason in last_error. Only the row version the start read is changed: it
// must still say "running" and have the updated_at the start saw. The
// management API bumps updated_at on every start and stop, so a stop, or a
// stop and a new start, that landed meanwhile keeps its status. Without a
// version (the start never read the row) nothing is written.
// lint:tenant-ok — update is scoped by (id, tenant_id).
func (c *frontSystemsConsumer) markError(connectionID, tenantID, reason string, updatedAt time.Time, logger *slog.Logger) {
	if updatedAt.IsZero() {
		logger.Warn("Not marking the connection error: its row was never read")
		return
	}
	c.mu.Lock()
	ctx := c.lifecycleCtxLocked()
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, statusWriteTimeout)
	defer cancel()
	res, err := c.db.ExecContext(ctx,
		`UPDATE connections SET status = 'error', last_error = $1, updated_at = NOW()
		 WHERE id = $2 AND tenant_id = $3 AND status = 'running' AND updated_at = $4`,
		reason, connectionID, tenantID, updatedAt)
	if err != nil {
		logger.Warn("Could not mark the connection error", "error", err)
		return
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		logger.Info("Not marking the connection error: it was stopped or started again meanwhile")
	}
}

// errBadBaseURL is a base_url that is missing or not a URL: no retry fixes it.
var errBadBaseURL = errors.New("bad front_systems base_url")

// fsStatusError is a Front Systems answer other than 2xx.
type fsStatusError struct {
	status int
	body   string
}

func (e *fsStatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.status, e.body) }

// classifyRegisterError says whether a failed registration is worth
// retrying. 408, 429 and 5xx are, and so is anything that got no answer
// (network error, timeout). Any other status means Front Systems refused the
// request as made (bad keys, no access, wrong base URL), and so does a bad
// base URL; those are permanent.
func classifyRegisterError(err error) (startOutcome, string) {
	if errors.Is(err, errBadBaseURL) {
		return startPermanent, "Front Systems: base_url is missing or not a URL"
	}
	var se *fsStatusError
	if errors.As(err, &se) {
		if se.status == http.StatusRequestTimeout || se.status == http.StatusTooManyRequests || se.status >= 500 {
			return startTransient, ""
		}
		return startPermanent, fmt.Sprintf("Front Systems refused the webhook registration (HTTP %d); check base_url and the two keys", se.status)
	}
	return startTransient, ""
}

// failureClass is a short, safe description of a transient failure for
// last_error.
func failureClass(err error) string {
	var se *fsStatusError
	if errors.As(err, &se) {
		return fmt.Sprintf("Front Systems answered HTTP %d", se.status)
	}
	var ste *startError
	if errors.As(err, &ste) {
		return "the database did not answer"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return "Front Systems could not be reached"
}

// transientDBError reports whether err looks like the database not
// answering (connection lost or refused, timeout, server starting or
// shutting down, too many connections) rather than a problem with the data.
func transientDBError(err error) bool {
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
