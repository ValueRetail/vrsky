package main

import (
	"fmt"
	"net/http"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

// From the review of the retry commit (#503). Their database mocks follow
// getConfig's lookup, which now reads updated_at too.

// Reviewer: a base_url without a scheme (the field is documented as the
// partner's "APIM host", so a bare host is a likely entry) is a broken
// base_url. The PR says that is permanent; it is retried for 6 h instead.
func TestReview_SchemelessBaseURLIsPermanent(t *testing.T) {
	f := newRetryFixture(t, 5*time.Millisecond)
	u := urConn{id: "conn-fs-noscheme", tenant: urTenantA}
	f.owners.set(u.id, u.tenant)
	f.mock.ExpectQuery(configLookupSQL).
		WithArgs(u.id, u.tenant).
		WillReturnRows(configRows(u.nodes("fs-apim.example.net")))
	expectMarkError(f.mock, u, "base_url")

	fgAwait(t, goDone(func() { f.start(u) }), "the failing start")
	if _, ok := pendingTenant(f.c, u.id); ok {
		t.Error("start still pending (retrying) for a base_url without a scheme; want permanent")
	}
	if code := postEvent(f.c, u.id, `{}`); code != http.StatusGone {
		t.Errorf("event: status %d, want 410", code)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}

// Reviewer: a start naming the wrong tenant that holds the claim makes the
// right tenant's queued start run untracked (attachPendingLocked returns nil
// while the wrong tenant's pending start is still there), so its transient
// failure is never retried and the connection refuses events with 410 while
// its row says running.
func TestReview_WrongTenantStartSuppressesRetry(t *testing.T) {
	f := newRetryFixture(t, time.Hour) // no retry fires during the test
	u := urConn{id: "conn-fs-wrongtenant", tenant: urTenantA}
	f.owners.set(u.id, u.tenant)
	// The wrong tenant's lookup finds no row; the right tenant's hits a blip.
	f.mock.ExpectQuery(configLookupSQL).
		WithArgs(u.id, urTenantB).WillReturnRows(sqlmock.NewRows([]string{"nodes", "updated_at"}))
	expectLookupErrors(f.mock, u, 1, fmt.Errorf("read tcp: %w", syscall.ECONNRESET))

	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.c.starts.AfterReserve = func(string) {
		first := false
		once.Do(func() { first = true })
		if first {
			close(reached)
			<-release
		}
	}
	wrong := goDone(func() { f.c.startConnection(t.Context(), u.id, urTenantB) })
	fgAwait(t, reached, "the wrong tenant's start to hold the claim")
	fgAwait(t, goDone(func() { f.start(u) }), "the right tenant's start (refused, queued)")
	close(release)
	fgAwait(t, wrong, "both starts")

	if tenant, ok := pendingTenant(f.c, u.id); !ok || tenant != u.tenant {
		t.Errorf("pending start after the right tenant's transient failure = %q, %v; want %q (retrying)", tenant, ok, u.tenant)
	}
	if code := postEvent(f.c, u.id, `{}`); code != http.StatusServiceUnavailable {
		t.Errorf("event after a transient failure: status %d, want 503", code)
	}
}

// A start queued behind one that fails for good (boot restore and a start
// command, say) is dropped: it would make the same refused call again.
func TestFrontSystemsRetry_QueuedStartDroppedAfterPermanentFailure(t *testing.T) {
	f := newRetryFixture(t, 5*time.Millisecond)
	u := f.conn("conn-fs-queued-401", urTenantA, 2) // a second lookup would be the queued start's
	expectMarkError(f.mock, u, "HTTP 401")
	f.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusUnauthorized, 1000 })
	g := newGate(f, 1)
	defer g.open()

	first := goDone(func() { f.start(u) })
	fgAwait(t, g.reached, "the first start to be registering")
	fgAwait(t, goDone(func() { f.c.handleStartCommand(cmdMsg("start", u.id, u.tenant)) }), "a start command, queued")
	g.open()
	fgAwait(t, first, "the first start and whatever it runs from the queue")
	f.quiet()
	if n := f.postCount(); n != 1 {
		t.Errorf("creates = %d, want 1 (the queued start must not try the refused keys again)", n)
	}
	if _, ok := pendingTenant(f.c, u.id); ok {
		t.Error("start pending after a permanent failure")
	}
}

// A connection lookup that fails for a reason other than the database not
// answering (here an id that isn't a UUID) is not retried. The row was never
// read, so nothing is written either.
func TestFrontSystemsRetry_NonTransientLookupErrorNotRetried(t *testing.T) {
	f := newRetryFixture(t, 5*time.Millisecond)
	u := urConn{id: "not-a-uuid", tenant: urTenantA}
	f.owners.set(u.id, u.tenant)
	expectLookupErrors(f.mock, u, 1, &pq.Error{Code: "22P02", Message: "invalid input syntax for type uuid"})

	fgAwait(t, goDone(func() { f.start(u) }), "the failing start")
	if _, ok := pendingTenant(f.c, u.id); ok {
		t.Error("start pending (retrying) after a lookup error no retry fixes")
	}
	f.quiet()
	if n := retryLoopsFor(f.c); n != 0 {
		t.Errorf("retry loops = %d, want 0", n)
	}
	if code := postEvent(f.c, u.id, `{}`); code != http.StatusGone {
		t.Errorf("event: status %d, want 410", code)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}

// Front Systems refusing the keys on the first list (401, 403) is permanent:
// nothing is posted, the row is marked error, and there's no retry.
func TestFrontSystemsRetry_ListRefusedIsPermanent(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newRetryFixture(t, 5*time.Millisecond)
			u := f.conn("conn-fs-list-refused", urTenantA, 1)
			expectMarkError(f.mock, u, fmt.Sprintf("HTTP %d", status))
			f.fs.set(func(fs *fakeFS) { fs.listStatus = status })

			fgAwait(t, goDone(func() { f.start(u) }), "the failing start")
			f.quiet()
			if n := f.postCount(); n != 0 {
				t.Errorf("creates = %d, want 0", n)
			}
			if _, ok := pendingTenant(f.c, u.id); ok {
				t.Error("start pending (retrying) after Front Systems refused the keys")
			}
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db: %v", err)
			}
		})
	}
}
