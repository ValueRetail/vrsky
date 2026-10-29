package main

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A start queued behind one that fails for good (#574). The queued start is
// dropped only when it would repeat the refused call: the failed start's
// "error" write landed, or the row is still the version that start read. If a
// stop and a new start (with fixed keys) moved the row on while the old start
// was working, the "updated_at" guard rightly keeps the old start from
// writing "error", and the queued start is the new run: it must run.

// newerVersion is the row's updated_at after the management API restarted the
// pipeline.
var newerVersion = cfgVersion.Add(time.Minute)

// expectRestartedConfig scripts the config lookups of a queued start test:
// the old start's (cfgVersion) and the new start's (newerVersion). Where the
// queued start must be dropped the second lookup stays unused; a start that
// runs anyway then finds it, registers and shows up as active.
func expectRestartedConfig(f *urFixture, u urConn) {
	f.mock.ExpectQuery(configLookupSQL).WithArgs(u.id, u.tenant).
		WillReturnRows(configRows(u.nodes(f.fs.srv.URL)))
	f.mock.ExpectQuery(configLookupSQL).WithArgs(u.id, u.tenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes", "updated_at"}).AddRow(u.nodes(f.fs.srv.URL), newerVersion))
}

// expectVersionCheck scripts the one status check the queued start makes.
func expectVersionCheck(f *urFixture, u urConn, version time.Time) {
	f.mock.ExpectQuery(statusCheckSQL).WithArgs(u.id, u.tenant).
		WillReturnRows(sqlmock.NewRows([]string{"status", "updated_at"}).AddRow("running", version))
}

func TestFrontSystemsRetry_QueuedStartAfterPermanentFailure(t *testing.T) {
	for i, tc := range []struct {
		name string
		// script sets up the database side after the two config lookups.
		script     func(f *urFixture, u urConn)
		wantActive bool
		wantPosts  int // the old start's refused create, plus one per event for a start that runs
	}{
		{
			// Stop lost, pipeline restarted with fixed keys: the old start's
			// "error" write finds a newer row and changes nothing, the row has
			// the new version.
			name: "old start's error write refused, row restarted: queued start runs",
			script: func(f *urFixture, u urConn) {
				f.mock.ExpectExec(markErrorSQL).WithArgs(reasonArg{"HTTP 401"}, u.id, u.tenant, cfgVersion).
					WillReturnResult(sqlmock.NewResult(0, 0))
				expectVersionCheck(f, u, newerVersion)
			},
			wantActive: true,
			wantPosts:  3,
		},
		{
			// The old start's "error" write landed: the queued start is the
			// same request; no row read is needed (none is scripted).
			name: "old start wrote error: queued start dropped",
			script: func(f *urFixture, u urConn) {
				expectMarkError(f.mock, u, "HTTP 401")
			},
			wantPosts: 1,
		},
		{
			// The "error" write failed (database blip) and the row is still the
			// version the old start read: the same request again.
			name: "error write failed, row unchanged: queued start dropped",
			script: func(f *urFixture, u urConn) {
				f.mock.ExpectExec(markErrorSQL).WithArgs(reasonArg{"HTTP 401"}, u.id, u.tenant, cfgVersion).
					WillReturnError(errors.New("connection reset"))
				expectVersionCheck(f, u, cfgVersion)
			},
			wantPosts: 1,
		},
		{
			// Neither is known: run, and let the start's own lookup decide.
			name: "error write failed, row unreadable: queued start runs",
			script: func(f *urFixture, u urConn) {
				f.mock.ExpectExec(markErrorSQL).WithArgs(reasonArg{"HTTP 401"}, u.id, u.tenant, cfgVersion).
					WillReturnError(errors.New("connection reset"))
				f.mock.ExpectQuery(statusCheckSQL).WithArgs(u.id, u.tenant).
					WillReturnError(errors.New("connection reset"))
			},
			wantActive: true,
			wantPosts:  3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRetryFixture(t, 5*time.Millisecond)
			u := urConn{id: "conn-fs-queued-" + strconv.Itoa(i), tenant: urTenantA}
			f.owners.set(u.id, u.tenant)
			expectRestartedConfig(f, u)
			tc.script(f, u)
			f.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusUnauthorized, 1 })
			g := newGate(f, 1)
			defer g.open()

			first := goDone(func() { f.start(u) })
			fgAwait(t, g.reached, "the old start to be registering")
			fgAwait(t, goDone(func() { f.c.handleStartCommand(cmdMsg("start", u.id, u.tenant)) }), "the new start, queued")
			g.open()
			fgAwait(t, first, "the old start and whatever it runs from the queue")

			_, active := activeTenant(f.c, u.id)
			if active != tc.wantActive {
				t.Errorf("active = %v, want %v", active, tc.wantActive)
			}
			if n := f.postCount(); n != tc.wantPosts {
				t.Errorf("creates = %d, want %d", n, tc.wantPosts)
			}
			wantEvent := http.StatusGone
			if tc.wantActive {
				wantEvent = http.StatusAccepted
			}
			if code := postEvent(f.c, u.id, `{"event":"SaleCreated"}`); code != wantEvent {
				t.Errorf("event: status %d, want %d", code, wantEvent)
			}
			if _, ok := pendingTenant(f.c, u.id); ok {
				t.Error("start still pending")
			}
			// A dropped start leaves its lookup unused, by design.
			if err := f.mock.ExpectationsWereMet(); tc.wantActive && err != nil {
				t.Errorf("db: %v", err)
			}
		})
	}
}

// The same sequence against Postgres, with the real row and the real guard:
// the old start's "error" write is refused by the updated_at it read, the row
// stays "running", and the queued start (the fixed keys) becomes active and
// leaves the row "running" with no last_error.
func TestFrontSystemsRetryPG_QueuedStartRunsAfterRestartedRow(t *testing.T) {
	e := newRetryPGEnv(t)
	u := e.connection("running")
	g := &gate{reached: make(chan struct{}), release: make(chan struct{})}
	defer g.open()
	e.fs.set(func(fs *fakeFS) {
		fs.postErr, fs.postErrLeft = http.StatusUnauthorized, 1 // the old keys
		fs.onPost = func(n int, _ string) {
			if n == 1 {
				close(g.reached)
				waitOrTimeout(g.release)
			}
		}
	})

	first := goDone(func() { e.c.startConnection(t.Context(), u.id, u.tenant) })
	fgAwait(t, g.reached, "the old start to be registering")
	// The stop never reaches this consumer; the management API stops and
	// starts the pipeline again, bumping updated_at each time.
	for _, status := range []string{"stopped", "running"} {
		if _, err := e.db.Exec(`UPDATE connections SET status = $3, updated_at = clock_timestamp()
			WHERE id = $1 AND tenant_id = $2`, u.id, u.tenant, status); err != nil {
			t.Fatal(err)
		}
	}
	fgAwait(t, goDone(func() { e.c.handleStartCommand(cmdMsg("start", u.id, u.tenant)) }), "the new start, queued")
	g.open()
	fgAwait(t, first, "the old start and the queued one")

	harness.Eventually(t, urWait, "the new start to activate", func() bool {
		_, ok := activeTenant(e.c, u.id)
		return ok
	})
	if status, lastErr := e.row(u); status != "running" || lastErr.Valid {
		t.Errorf("row = %q, %v; want running with no last_error", status, lastErr)
	}
	if n := func() int { e.fs.mu.Lock(); defer e.fs.mu.Unlock(); return e.fs.posts }(); n != 2 {
		t.Errorf("creates = %d, want 2 (the refused one and the new start's)", n)
	}
	if code := postEvent(e.c, u.id, `{"event":"SaleCreated"}`); code != http.StatusAccepted {
		t.Errorf("event: status %d, want 202", code)
	}
}

// The counterpart on Postgres: no restart meanwhile, so the old start's
// "error" write lands and the queued start is dropped (one create only).
func TestFrontSystemsRetryPG_QueuedStartDroppedWhenErrorWritten(t *testing.T) {
	e := newRetryPGEnv(t)
	u := e.connection("running")
	g := &gate{reached: make(chan struct{}), release: make(chan struct{})}
	defer g.open()
	e.fs.set(func(fs *fakeFS) {
		fs.postErr, fs.postErrLeft = http.StatusUnauthorized, 1000
		fs.onPost = func(n int, _ string) {
			if n == 1 {
				close(g.reached)
				waitOrTimeout(g.release)
			}
		}
	})

	first := goDone(func() { e.c.startConnection(t.Context(), u.id, u.tenant) })
	fgAwait(t, g.reached, "the first start to be registering")
	fgAwait(t, goDone(func() { e.c.handleStartCommand(cmdMsg("start", u.id, u.tenant)) }), "a start command, queued")
	g.open()
	fgAwait(t, first, "the first start")

	if status, lastErr := e.row(u); status != "error" || !lastErr.Valid {
		t.Errorf("row = %q, %v; want error with a last_error", status, lastErr)
	}
	e.fs.mu.Lock()
	posts := e.fs.posts
	e.fs.mu.Unlock()
	if posts != 1 {
		t.Errorf("creates = %d, want 1 (the queued start must not try the refused keys again)", posts)
	}
}
