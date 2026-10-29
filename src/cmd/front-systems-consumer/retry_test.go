package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for retrying failed starts (#503, the owner's decision before merge):
// a start that fails for a reason that passes is retried with backoff until
// the connection is active; the webhook route answers 503 meanwhile; a stop,
// a row that is no longer running, giving up or shutdown end the retries; a
// permanent failure is not retried and marks the row "error"; and every
// attempt goes through the StartGuard, so two never run at once.
//
// They reuse the fake Front Systems and the direct fixture from
// unregister_test.go. Retry timing is shortened per test. Where the order
// matters it is forced through the fake's onPost hook or a backoff of an
// hour, never by sleeping; the sleeps below only give a wrong
// implementation the chance to do something it must not.

const (
	statusCheckSQL = `SELECT status, updated_at FROM connections WHERE id = \$1 AND tenant_id = \$2`
	markErrorSQL   = `UPDATE connections SET status = 'error', last_error = \$1, updated_at = NOW\(\)\s+WHERE id = \$2 AND tenant_id = \$3 AND status = 'running' AND updated_at = \$4`
)

// newRetryFixture is the direct fixture with retries every base to 4*base.
func newRetryFixture(t *testing.T, base time.Duration) *urFixture {
	t.Helper()
	f := newURFixture(t)
	f.c.retryBase, f.c.retryMax = base, 4*base
	return f
}

// expectStatusChecks scripts n status checks of u's row: status "" answers
// no row, err fails the query.
func expectStatusChecks(mock sqlmock.Sqlmock, u urConn, n int, status string, err error) {
	for i := 0; i < n; i++ {
		q := mock.ExpectQuery(statusCheckSQL).WithArgs(u.id, u.tenant)
		switch {
		case err != nil:
			q.WillReturnError(err)
		case status == "":
			q.WillReturnRows(sqlmock.NewRows([]string{"status", "updated_at"}))
		default:
			q.WillReturnRows(sqlmock.NewRows([]string{"status", "updated_at"}).AddRow(status, cfgVersion))
		}
	}
}

// expectLookupErrors scripts n config lookups of u that fail with err.
func expectLookupErrors(mock sqlmock.Sqlmock, u urConn, n int, err error) {
	for i := 0; i < n; i++ {
		mock.ExpectQuery(configLookupSQL).
			WithArgs(u.id, u.tenant).WillReturnError(err)
	}
}

// reasonArg matches a last_error that contains want and none of the fake's
// response body.
type reasonArg struct{ want string }

func (a reasonArg) Match(v driver.Value) bool {
	s, ok := v.(string)
	return ok && strings.Contains(s, a.want) && !strings.Contains(s, "body-must-not-reach")
}

func expectMarkError(mock sqlmock.Sqlmock, u urConn, want string) {
	mock.ExpectExec(markErrorSQL).WithArgs(reasonArg{want}, u.id, u.tenant, cfgVersion).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// pendingTenant reports the connection's pending start, if any.
func pendingTenant(c *frontSystemsConsumer, connID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pending[connID]
	if !ok {
		return "", false
	}
	return p.tenantID, true
}

// retryLoopsFor counts the retry loop goroutines running for c. One that
// hasn't been scheduled yet prints no arguments and isn't counted, so a check
// for a loop that was just started waits for it.
func retryLoopsFor(c *frontSystemsConsumer) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	return strings.Count(string(buf), fmt.Sprintf("(*frontSystemsConsumer).retryLoop(%p", c))
}

func (f *urFixture) postCount() int {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	return f.fs.posts
}

// gate holds the n-th create at the fake until release.
type gate struct {
	reached, release chan struct{}
	once             sync.Once
}

func newGate(f *urFixture, n int) *gate {
	g := &gate{reached: make(chan struct{}), release: make(chan struct{})}
	f.fs.set(func(fs *fakeFS) {
		fs.onPost = func(i int, _ string) {
			if i == n {
				close(g.reached)
				waitOrTimeout(g.release)
			}
		}
	})
	return g
}

func (g *gate) open() { g.once.Do(func() { close(g.release) }) }

// quiet waits long enough for several retries at the fixture's timing.
func (f *urFixture) quiet() { time.Sleep(10 * f.c.retryMax) }

// A start that fails for a reason that passes is retried until it goes
// through. The route answers 503 while the start is pending, between
// attempts and during one, and 202 once the connection is active; the
// event is published once; nothing is registered twice; the retries end.
func TestFrontSystemsRetry_TransientFailureRetriesUntilActive(t *testing.T) {
	reset := fmt.Errorf("read tcp 10.0.0.1:5432: %w", syscall.ECONNRESET)
	for _, tc := range []struct {
		name     string
		fsStatus int // status of the failing creates; 0 drops the connection
		fsFails  int // creates that fail before the start goes through
		dbFails  int // config lookups that fail first
		rowFails int // status checks that fail (after one failed create)
	}{
		{"Front Systems 500 three times", http.StatusInternalServerError, 3, 0, 0},
		{"Front Systems 502", http.StatusBadGateway, 1, 0, 0},
		{"Front Systems 503 twice", http.StatusServiceUnavailable, 2, 0, 0},
		{"Front Systems 429 twice", http.StatusTooManyRequests, 2, 0, 0},
		{"Front Systems 408", http.StatusRequestTimeout, 1, 0, 0},
		{"connection to Front Systems dropped twice", 0, 2, 0, 0},
		{"database blip on the config lookup twice", 0, 0, 2, 0},
		{"database blip on the status check twice", http.StatusInternalServerError, 1, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRetryFixture(t, 5*time.Millisecond)
			u := urConn{id: "conn-fs-retry", tenant: urTenantA}
			f.owners.set(u.id, u.tenant)
			expectLookupErrors(f.mock, u, tc.dbFails, reset)
			expectConfig(f.mock, f.fs, u, tc.fsFails+1)
			expectStatusChecks(f.mock, u, tc.rowFails, "", reset)
			expectStatusChecks(f.mock, u, tc.fsFails+tc.dbFails, "running", nil)
			f.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = tc.fsStatus, tc.fsFails })
			g := newGate(f, tc.fsFails+1)
			defer g.open()

			fgAwait(t, goDone(func() { f.start(u) }), "the first start")
			if _, ok := activeTenant(f.c, u.id); ok {
				t.Fatal("active although the first start failed")
			}
			if tenant, ok := pendingTenant(f.c, u.id); !ok || tenant != u.tenant {
				t.Fatalf("pending start after the failure = %q, %v; want %q", tenant, ok, u.tenant)
			}
			if code := postEvent(f.c, u.id, `{"event":"SaleCreated"}`); code != http.StatusServiceUnavailable {
				t.Errorf("event while the start waits to retry: status %d, want 503", code)
			}

			fgAwait(t, g.reached, "the retry that goes through to be registering")
			if code := postEvent(f.c, u.id, `{"event":"SaleCreated"}`); code != http.StatusServiceUnavailable {
				t.Errorf("event during the retry: status %d, want 503", code)
			}
			if n := f.publishedFor(u.id); n != 0 {
				t.Errorf("published %d event(s) before the connection was active", n)
			}
			g.open()
			harness.Eventually(t, urWait, "the retried start to activate", func() bool {
				_, ok := activeTenant(f.c, u.id)
				return ok
			})
			if code := postEvent(f.c, u.id, `{"event":"SaleCreated"}`); code != http.StatusAccepted {
				t.Fatalf("event once active: status %d, want 202", code)
			}
			if n := f.publishedFor(u.id); n != 1 {
				t.Errorf("published %d event(s), want 1", n)
			}
			wantOnePerEvent(t, f.fs, u, "after the retried start")
			harness.Eventually(t, urWait, "the retry loop to end", func() bool {
				_, pending := pendingTenant(f.c, u.id)
				return !pending && retryLoopsFor(f.c) == 0
			})
			posts := f.postCount()
			f.quiet()
			if got := f.postCount(); got != posts {
				t.Errorf("creates after the connection was active: %d, want none", got-posts)
			}
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db: %v", err)
			}
		})
	}
}

// A stop ends the retries: while the start waits for its next attempt, and
// while an attempt is in progress. No start goes through afterwards.
func TestFrontSystemsRetry_StopEndsRetries(t *testing.T) {
	t.Run("during the backoff", func(t *testing.T) {
		f := newRetryFixture(t, time.Hour)
		u := f.conn("conn-fs-stop-backoff", urTenantA, 1)
		f.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusServiceUnavailable, 1 })

		fgAwait(t, goDone(func() { f.start(u) }), "the failing start")
		harness.Eventually(t, urWait, "one retry loop after a transient failure", func() bool { return retryLoopsFor(f.c) == 1 })
		f.stop(u)
		harness.Eventually(t, urWait, "the retry loop to end after the stop", func() bool {
			return retryLoopsFor(f.c) == 0
		})
		if _, ok := pendingTenant(f.c, u.id); ok {
			t.Error("start still pending after the stop")
		}
		if code := postEvent(f.c, u.id, `{}`); code != http.StatusGone {
			t.Errorf("event after the stop: status %d, want 410", code)
		}
		if err := f.mock.ExpectationsWereMet(); err != nil {
			t.Errorf("db: %v", err)
		}
	})

	t.Run("nothing starts after the stop", func(t *testing.T) {
		f := newRetryFixture(t, 5*time.Millisecond)
		u := f.conn("conn-fs-stop-quick", urTenantA, 1)
		f.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusServiceUnavailable, 1 })

		fgAwait(t, goDone(func() { f.start(u) }), "the failing start")
		f.stop(u)
		calls := len(f.fs.logCopy())
		f.quiet()
		if got := f.fs.logCopy(); len(got) != calls {
			t.Errorf("Front Systems calls after the stop: %v, want none", got[calls:])
		}
		if _, ok := activeTenant(f.c, u.id); ok {
			t.Error("connection active after the stop")
		}
		if code := postEvent(f.c, u.id, `{}`); code != http.StatusGone {
			t.Errorf("event after the stop: status %d, want 410", code)
		}
		if n := retryLoopsFor(f.c); n != 0 {
			t.Errorf("retry loops after the stop = %d, want 0", n)
		}
	})

	t.Run("during an attempt", func(t *testing.T) {
		f := newRetryFixture(t, 5*time.Millisecond)
		u := f.conn("conn-fs-stop-attempt", urTenantA, 2)
		expectStatusChecks(f.mock, u, 1, "running", nil)
		f.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusInternalServerError, 1 })
		g := newGate(f, 2)
		defer g.open()

		fgAwait(t, goDone(func() { f.start(u) }), "the failing start")
		fgAwait(t, g.reached, "the retry to be registering")
		f.stop(u)
		g.open()
		harness.Eventually(t, urWait, "the abandoned retry to undo its registrations and the loop to end", func() bool {
			return len(f.fs.registered(u.apiKey())) == 0 && retryLoopsFor(f.c) == 0
		})
		if _, ok := activeTenant(f.c, u.id); ok {
			t.Error("connection active although a stop arrived during the retry")
		}
		posts := f.postCount()
		f.quiet()
		if got := f.postCount(); got != posts {
			t.Errorf("creates after the stop: %d, want none", got-posts)
		}
		if err := f.mock.ExpectationsWereMet(); err != nil {
			t.Errorf("db: %v", err)
		}
	})
}

// Shutting down while a start waits to retry ends the retry at once, leaves
// no goroutine behind and writes nothing: the row stays "running" for the
// next process's boot restore.
func TestFrontSystemsRetry_ShutdownDuringBackoff(t *testing.T) {
	f := newRetryFixture(t, time.Hour)
	u := f.conn("conn-fs-shutdown", urTenantA, 1)
	f.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusBadGateway, 1 })

	fgAwait(t, goDone(func() { f.start(u) }), "the failing start")
	harness.Eventually(t, urWait, "one retry loop after a transient failure", func() bool { return retryLoopsFor(f.c) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 3*stopGrace)
	defer cancel()
	began := time.Now()
	if err := f.c.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if took := time.Since(began); took >= time.Second {
		t.Errorf("Stop took %v with a retry waiting; it should end the retry at once", took)
	}
	harness.Eventually(t, time.Second, "no retry loop left", func() bool { return retryLoopsFor(f.c) == 0 })
	if _, ok := pendingTenant(f.c, u.id); ok {
		t.Error("start still pending after Stop")
	}
	if code := postEvent(f.c, u.id, `{}`); code != http.StatusServiceUnavailable {
		t.Errorf("event during shutdown: status %d, want 503", code)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}

// Every attempt goes through the StartGuard: while one start (the retry's or
// a start command's) is registering, other starts and the retry loop's next
// attempts are refused, and the webhooks end up registered once.
func TestFrontSystemsRetry_NoConcurrentStarts(t *testing.T) {
	f := newRetryFixture(t, 5*time.Millisecond)
	u := urConn{id: "conn-fs-concurrent", tenant: urTenantA}
	f.owners.set(u.id, u.tenant)
	expectConfig(f.mock, f.fs, u, 2)
	expectStatusChecks(f.mock, u, 1000, "running", nil) // the loop checks while it is refused
	f.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusInternalServerError, 1 })
	var claims atomic.Int32
	f.c.starts.AfterReserve = func(id string) {
		if id == u.id {
			claims.Add(1)
		}
	}
	g := newGate(f, 2)
	defer g.open()

	fgAwait(t, goDone(func() { f.start(u) }), "the failing start")
	// A start command right away: it or the retry loop gets the claim, and is
	// held at the fake.
	go f.c.handleStartCommand(cmdMsg("start", u.id, u.tenant))
	fgAwait(t, g.reached, "a second start to be registering")
	fgAwait(t, goDone(func() { f.c.handleStartCommand(cmdMsg("start", u.id, u.tenant)) }), "a start command while one is registering")
	fgAwait(t, goDone(func() { f.start(u) }), "a restore start while one is registering")
	f.quiet() // the retry loop tries several times meanwhile
	if n := claims.Load(); n != 2 {
		t.Errorf("starts that claimed the connection = %d, want 2 (the failed one, then the held one)", n)
	}
	g.open()
	harness.Eventually(t, urWait, "the connection to be active and the retries to end", func() bool {
		_, ok := activeTenant(f.c, u.id)
		return ok && retryLoopsFor(f.c) == 0
	})
	if n := claims.Load(); n != 2 {
		t.Errorf("starts that claimed the connection = %d, want 2", n)
	}
	wantOnePerEvent(t, f.fs, u, "after the starts")
}

// A failure no retry fixes is not retried. The row is marked "error" with a
// reason that names the cause and carries no response body, and the route
// answers 410.
func TestFrontSystemsRetry_PermanentFailureMarksError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int  // the create's answer
		noBaseURL  bool // the node has no base_url
		noSecret   bool // the node refers to a secret that isn't there
		wantReason string
	}{
		{"401 bad keys", http.StatusUnauthorized, false, false, "HTTP 401"},
		{"403 no access", http.StatusForbidden, false, false, "HTTP 403"},
		{"400 bad request", http.StatusBadRequest, false, false, "HTTP 400"},
		{"404 wrong base URL", http.StatusNotFound, false, false, "HTTP 404"},
		{"no base_url", 0, true, false, "base_url"},
		{"secret not found", 0, false, true, "secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRetryFixture(t, 5*time.Millisecond)
			u := urConn{id: "conn-fs-permanent", tenant: urTenantA}
			f.owners.set(u.id, u.tenant)
			switch {
			case tc.noBaseURL:
				f.mock.ExpectQuery(configLookupSQL).
					WithArgs(u.id, u.tenant).
					WillReturnRows(configRows(u.nodes("")))
			case tc.noSecret:
				nodes := mustJSON([]any{map[string]any{"id": "n1", "type": "consumer", "config": map[string]any{
					"type": "front_systems",
					"front_systems": map[string]any{
						"base_url": f.fs.srv.URL, "api_key_secret_id": "sec-gone",
						"events": u.evs(), "callback_url": u.callback(),
					},
				}}})
				f.mock.ExpectQuery(configLookupSQL).
					WithArgs(u.id, u.tenant).WillReturnRows(configRows(nodes))
				f.mock.ExpectQuery(`SELECT ciphertext FROM secrets`).WithArgs("sec-gone", u.tenant).
					WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}))
			default:
				expectConfig(f.mock, f.fs, u, 1)
				f.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = tc.status, 1000 })
			}
			expectMarkError(f.mock, u, tc.wantReason)

			fgAwait(t, goDone(func() { f.start(u) }), "the failing start")
			if n := retryLoopsFor(f.c); n != 0 {
				t.Errorf("retry loops after a permanent failure = %d, want 0", n)
			}
			if _, ok := pendingTenant(f.c, u.id); ok {
				t.Error("start still pending after a permanent failure")
			}
			if code := postEvent(f.c, u.id, `{}`); code != http.StatusGone {
				t.Errorf("event after a permanent failure: status %d, want 410", code)
			}
			posts := f.postCount()
			f.quiet()
			if got := f.postCount(); got != posts {
				t.Errorf("creates after a permanent failure: %d, want none", got-posts)
			}
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db: %v", err)
			}
		})
	}
}

// Retries end without a start when the row no longer says "running" (a stop
// command that never arrived, a delete), and nothing is written.
func TestFrontSystemsRetry_RowNotRunningEndsRetries(t *testing.T) {
	for _, status := range []string{"stopped", "error", ""} {
		name := status
		if name == "" {
			name = "row gone"
		}
		t.Run(name, func(t *testing.T) {
			f := newRetryFixture(t, 5*time.Millisecond)
			u := f.conn("conn-fs-rowcheck", urTenantA, 1)
			expectStatusChecks(f.mock, u, 1, status, nil)
			f.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusServiceUnavailable, 1 })

			fgAwait(t, goDone(func() { f.start(u) }), "the failing start")
			harness.Eventually(t, urWait, "the retry loop to end", func() bool { return retryLoopsFor(f.c) == 0 })
			posts := f.postCount()
			f.quiet()
			if got := f.postCount(); got != posts {
				t.Errorf("creates after the row stopped saying running: %d, want none", got-posts)
			}
			if _, ok := activeTenant(f.c, u.id); ok {
				t.Error("connection active")
			}
			if code := postEvent(f.c, u.id, `{}`); code != http.StatusGone {
				t.Errorf("event: status %d, want 410", code)
			}
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db: %v", err)
			}
		})
	}
}

// A start that keeps failing is given up after retryGiveUp: the row is
// marked "error" with the last cause, and the retries end.
func TestFrontSystemsRetry_GivesUpAndMarksError(t *testing.T) {
	f := newRetryFixture(t, 5*time.Millisecond)
	f.c.retryGiveUp = time.Nanosecond // the first retry's failure is past it
	u := f.conn("conn-fs-giveup", urTenantA, 2)
	expectStatusChecks(f.mock, u, 1, "running", nil)
	expectMarkError(f.mock, u, "HTTP 500")
	f.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusInternalServerError, 1000 })

	fgAwait(t, goDone(func() { f.start(u) }), "the failing start")
	harness.Eventually(t, urWait, "the retries to give up", func() bool {
		_, pending := pendingTenant(f.c, u.id)
		return !pending && retryLoopsFor(f.c) == 0
	})
	if code := postEvent(f.c, u.id, `{}`); code != http.StatusGone {
		t.Errorf("event after giving up: status %d, want 410", code)
	}
	posts := f.postCount()
	f.quiet()
	if got := f.postCount(); got != posts || got != 2 {
		t.Errorf("creates = %d then %d, want 2 (the start and one retry) and no more", posts, got)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}

// Tenant isolation while a start waits to retry: an event whose row belongs
// to another tenant gets 410, and a stop naming another tenant leaves the
// retry alone. A stop naming the right tenant ends it.
func TestFrontSystemsRetry_OtherTenant(t *testing.T) {
	f := newRetryFixture(t, time.Hour)
	u := f.conn("conn-fs-tenant-retry", urTenantA, 1)
	f.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusServiceUnavailable, 1 })
	fgAwait(t, goDone(func() { f.start(u) }), "the failing start")
	harness.Eventually(t, urWait, "one retry loop after a transient failure", func() bool { return retryLoopsFor(f.c) == 1 })

	f.owners.set(u.id, urTenantB)
	if code := postEvent(f.c, u.id, `{}`); code != http.StatusGone {
		t.Errorf("event for another tenant's row while retrying: status %d, want 410", code)
	}
	f.owners.set(u.id, urTenantA)
	if code := postEvent(f.c, u.id, `{}`); code != http.StatusServiceUnavailable {
		t.Errorf("event while retrying: status %d, want 503", code)
	}

	f.c.handleStopCommand(cmdMsg("stop", u.id, urTenantB))
	if tenant, ok := pendingTenant(f.c, u.id); !ok || tenant != urTenantA {
		t.Errorf("pending start after another tenant's stop = %q, %v; want %q", tenant, ok, urTenantA)
	}
	if n := retryLoopsFor(f.c); n != 1 {
		t.Errorf("retry loops after another tenant's stop = %d, want 1", n)
	}
	if code := postEvent(f.c, u.id, `{}`); code != http.StatusServiceUnavailable {
		t.Errorf("event after another tenant's stop: status %d, want 503", code)
	}

	f.stop(u)
	harness.Eventually(t, urWait, "the retry loop to end", func() bool { return retryLoopsFor(f.c) == 0 })
	if code := postEvent(f.c, u.id, `{}`); code != http.StatusGone {
		t.Errorf("event after the stop: status %d, want 410", code)
	}
}

// Through the real runner and NATS: boot restore's config lookup hits a
// database blip, the event route answers 503 after restore while the start
// waits to retry, the retry starts the connection, and its events are
// published on the bus.
func TestFrontSystemsRetry_HarnessRestoreAfterDatabaseBlip(t *testing.T) {
	fs := newFakeFS(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	mock.MatchExpectationsInOrder(false)
	t.Cleanup(func() { _ = db.Close() })

	x := urConn{id: "conn-fs-blip", tenant: urTenantA}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id::text, tenant_id FROM connections`)).
		WithArgs("front_systems").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id"}).AddRow(x.id, x.tenant))
	expectLookupErrors(mock, x, 1, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET})
	expectConfig(mock, fs, x, 1)
	expectStatusChecks(mock, x, 1, "running", nil)

	own := &owners{m: map[string]string{x.id: x.tenant}}
	c := &frontSystemsConsumer{httpClient: fs.srv.Client(), resolveTenant: own.resolve,
		retryBase: 20 * time.Millisecond, retryMax: 80 * time.Millisecond}

	reached, release := make(chan struct{}), make(chan struct{})
	fs.set(func(fs *fakeFS) {
		fs.onList = func(n int) {
			if n == 1 {
				close(reached)
				waitOrTimeout(release)
			}
		}
	})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	defer open()

	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "front-systems-consumer", DB: db})
	fgAwait(t, reached, "the retry to be registering")
	c.mu.Lock()
	restoring := c.restoring
	c.mu.Unlock()
	if restoring {
		t.Error("boot restore still running while its failed start is being retried")
	}
	if code := postEvent(c, x.id, `{"event":"SaleCreated"}`); code != http.StatusServiceUnavailable {
		t.Errorf("event after restore while the start is retried: status %d, want 503", code)
	}
	open()
	harness.Eventually(t, urWait, "the retried start to activate", func() bool {
		_, ok := activeTenant(c, x.id)
		return ok
	})
	if code := postEvent(c, x.id, `{"event":"SaleCreated","saleId":9}`); code != http.StatusAccepted {
		t.Fatalf("event once active: status %d, want 202", code)
	}
	env := h.ExpectEnvelope(t, func(e *envelope.Envelope) bool { return e.IntegrationID == x.id }, urWait)
	if env.TenantID != x.tenant {
		t.Errorf("published tenant = %q, want %q", env.TenantID, x.tenant)
	}
	wantOnePerEvent(t, fs, x, "after the retried restore")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}

func TestClassifyRegisterError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want startOutcome
	}{
		{"408", &fsStatusError{status: 408}, startTransient},
		{"429", &fsStatusError{status: 429}, startTransient},
		{"500", &fsStatusError{status: 500}, startTransient},
		{"503 wrapped", fmt.Errorf("register X: %w", &fsStatusError{status: 503}), startTransient},
		{"network", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, startTransient},
		{"timeout", context.DeadlineExceeded, startTransient},
		{"EOF", io.EOF, startTransient},
		{"301", &fsStatusError{status: 301}, startPermanent},
		{"400", &fsStatusError{status: 400}, startPermanent},
		{"401", &fsStatusError{status: 401}, startPermanent},
		{"403", &fsStatusError{status: 403}, startPermanent},
		{"404", &fsStatusError{status: 404}, startPermanent},
		{"422", &fsStatusError{status: 422}, startPermanent},
		{"bad base URL", fmt.Errorf("register X: %w", errBadBaseURL), startPermanent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := classifyRegisterError(tc.err)
			if got != tc.want {
				t.Errorf("classifyRegisterError(%v) = %v, want %v", tc.err, got, tc.want)
			}
			if (got == startPermanent) != (reason != "") {
				t.Errorf("reason %q for outcome %v: a permanent failure needs one, a transient one has none", reason, got)
			}
		})
	}
}

func TestTransientDBError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"no rows", sql.ErrNoRows, false},
		{"secret not found", errors.New("secret x not found for tenant y"), false},
		{"connection reset", fmt.Errorf("resolve: %w", syscall.ECONNRESET), true},
		{"connection refused", syscall.ECONNREFUSED, true},
		{"net error", &net.OpError{Op: "read", Err: errors.New("x")}, true},
		{"bad conn", driver.ErrBadConn, true},
		{"timeout", context.DeadlineExceeded, true},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"pq admin shutdown", &pq.Error{Code: "57P01"}, true},
		{"pq cannot connect now", &pq.Error{Code: "57P03"}, true},
		{"pq connection failure", &pq.Error{Code: "08006"}, true},
		{"pq too many connections", &pq.Error{Code: "53300"}, true},
		{"pq undefined table", &pq.Error{Code: "42P01"}, false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := transientDBError(tc.err); got != tc.want {
				t.Errorf("transientDBError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	c := &frontSystemsConsumer{retryBase: 5 * time.Second, retryMax: 5 * time.Minute}
	for _, tc := range []struct {
		n    int
		full time.Duration // before jitter
	}{
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{6, 160 * time.Second},
		{7, 5 * time.Minute},
		{50, 5 * time.Minute},
	} {
		t.Run(fmt.Sprint(tc.n), func(t *testing.T) {
			lo, hi := time.Duration(1<<62), time.Duration(0)
			for i := 0; i < 200; i++ {
				d := c.retryDelay(tc.n)
				lo, hi = min(lo, d), max(hi, d)
			}
			if lo < tc.full/2 || hi > tc.full {
				t.Errorf("retryDelay(%d) in [%v, %v], want within [%v, %v]", tc.n, lo, hi, tc.full/2, tc.full)
			}
			if hi == lo {
				t.Errorf("retryDelay(%d) = %v every time; want jitter", tc.n, lo)
			}
		})
	}
	if d := (&frontSystemsConsumer{}).retryDelay(1); d < defaultRetryBase/2 || d > defaultRetryBase {
		t.Errorf("default first delay %v, want within [%v, %v]", d, defaultRetryBase/2, defaultRetryBase)
	}
}
