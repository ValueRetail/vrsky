package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"
)

// Tests for #429: Front Systems on sdk.StartGuard. Boot restore and a start
// command (or two commands) call startConnection on different goroutines; the
// webhooks must be registered at Front Systems exactly once, a stop that lands
// mid-start leaves the connection stopped (so a later start registers again),
// and a start refused meanwhile is not lost when the first one fails.
//
// The fake Front Systems API counts registrations per event. Starts are held
// in the slow part they really have, the registration POST, or through the
// guard's AfterReserve hook while the lookup is about to run; never with
// sleeps. Every wait is bounded by fgWait.

const (
	fgConn   = "conn-front-systems-guard"
	fgTenant = "tenant-front-systems-guard"
	fgWait   = 5 * time.Second
)

var fgEvents = []string{"SaleCreated", "ReturnCreated"}

type fgFixture struct {
	c    *frontSystemsConsumer
	mock sqlmock.Sqlmock
	srv  *httptest.Server

	mu     sync.Mutex
	posts  map[string]int // registrations answered 2xx, by event
	failed int            // registrations answered 500
	total  int            // every registration POST seen
	onPost func(n int) (fail bool)
}

func newFGFixture(t *testing.T) *fgFixture {
	t.Helper()
	f := &fgFixture{posts: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/webhooks" {
			http.NotFound(w, r)
			return
		}
		var body struct{ Event, URL string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.total++
		n, hook := f.total, f.onPost
		f.mu.Unlock()
		if hook != nil && hook(n) {
			f.mu.Lock()
			f.failed++
			f.mu.Unlock()
			http.Error(w, `{"error":"upstream"}`, http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.posts[body.Event]++
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(f.srv.Close)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	// Starts that race each other look the same connection up with the same
	// arguments; the sentinel lookup (see noFurtherLookup) can be any of them.
	mock.MatchExpectationsInOrder(false)
	t.Cleanup(func() { _ = db.Close() })
	f.mock = mock
	f.c = &frontSystemsConsumer{
		db:         db,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: f.srv.Client(),
	}
	return f
}

func (f *fgFixture) setOnPost(hook func(n int) (fail bool)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onPost = hook
}

func (f *fgFixture) nodes() []byte {
	return mustJSON([]any{map[string]any{"id": "c1", "type": "consumer", "config": map[string]any{
		"type": "front_systems",
		"front_systems": map[string]any{
			"base_url":     f.srv.URL,
			"events":       fgEvents,
			"callback_url": "https://vrsky.example/frontsystems/events/" + fgConn,
		},
	}}})
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// expectLookups scripts n config lookups, the first fails of them failing.
func (f *fgFixture) expectLookups(n, fails int) {
	for i := 0; i < n; i++ {
		q := f.mock.ExpectQuery(configLookupSQL).WithArgs(fgConn, fgTenant)
		if i < fails {
			// A dropped database connection: transient, so the start queued
			// behind the failed one still runs.
			q.WillReturnError(fmt.Errorf("read tcp: %w", syscall.ECONNRESET))
			continue
		}
		q.WillReturnRows(configRows(f.nodes()))
	}
}

// noFurtherLookup takes the one lookup scripted beyond what the code should
// use, then checks every scripted lookup was used: together, the code looked
// the connection up exactly as often as the test expects.
func (f *fgFixture) noFurtherLookup(t *testing.T) {
	t.Helper()
	if _, _, err := f.c.getConfig(context.Background(), fgConn, fgTenant); err != nil {
		t.Errorf("the connection was looked up more often than expected: %v", err)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}

// registered reports each event's successful registrations.
func (f *fgFixture) registered() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int, len(f.posts))
	for k, v := range f.posts {
		out[k] = v
	}
	return out
}

func (f *fgFixture) wantRegistered(t *testing.T, want int, when string) {
	t.Helper()
	got := f.registered()
	for _, ev := range fgEvents {
		if got[ev] != want {
			t.Errorf("%s: %s registered %d time(s), want %d (all: %v)", when, ev, got[ev], want, got)
		}
	}
}

func (f *fgFixture) isActive() bool {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	_, ok := f.c.active[fgConn]
	return ok
}

func (f *fgFixture) start() { f.c.startConnection(context.Background(), fgConn, fgTenant) }

func fgCmd(kind string) *nats.Msg {
	return &nats.Msg{
		Subject: "vrsky.commands." + fgTenant + ".connection." + kind,
		Data:    mustJSON(map[string]string{"connection_id": fgConn, "tenant_id": fgTenant}),
	}
}

func fgAwait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(fgWait):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func goDone(fn func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	return done
}

// waitOrTimeout blocks until ch closes, at most fgWait, so a hook on a server
// goroutine can never hang the test.
func waitOrTimeout(ch <-chan struct{}) {
	select {
	case <-ch:
	case <-time.After(fgWait):
	}
}

// The second start of each pair, as boot restore and the start-command
// subscription make it.
var fgSecondStarts = []struct {
	name  string
	start func(c *frontSystemsConsumer)
}{
	{"start command", func(c *frontSystemsConsumer) { c.handleStartCommand(fgCmd("start")) }},
	{"restore", func(c *frontSystemsConsumer) { c.startConnection(context.Background(), fgConn, fgTenant) }},
}

// AC1: a second start arriving while the first is registering does not
// register the webhooks again.
func TestFrontSystemsStartGuard_ConcurrentStartsRegisterOnce(t *testing.T) {
	for _, tc := range fgSecondStarts {
		t.Run(tc.name, func(t *testing.T) {
			f := newFGFixture(t)
			f.expectLookups(2, 0) // one for the start, one sentinel
			reached, release := make(chan struct{}), make(chan struct{})
			f.setOnPost(func(n int) bool {
				if n == 1 {
					close(reached)
					waitOrTimeout(release)
				}
				return false
			})

			first := goDone(f.start)
			fgAwait(t, reached, "first start to be registering")
			fgAwait(t, goDone(func() { tc.start(f.c) }), "second start to return while the first registers")
			close(release)
			fgAwait(t, first, "first start")

			f.wantRegistered(t, 1, "after two starts")
			if !f.isActive() {
				t.Error("connection not recorded as started")
			}
			f.noFurtherLookup(t)
		})
	}
}

// AC1: a start of a connection already started registers nothing; once it is
// stopped, the next start registers again.
func TestFrontSystemsStartGuard_RestartRegistersOnlyAfterStop(t *testing.T) {
	f := newFGFixture(t)
	f.expectLookups(3, 0) // first start, start after the stop, sentinel

	f.start()
	f.wantRegistered(t, 1, "after the first start")
	f.start()
	f.c.handleStartCommand(fgCmd("start"))
	f.wantRegistered(t, 1, "after starting a started connection twice more")

	f.c.handleStopCommand(fgCmd("stop"))
	if f.isActive() {
		t.Fatal("connection still recorded as started after a stop")
	}
	f.start()
	f.wantRegistered(t, 2, "after a start that follows a stop")
	if !f.isActive() {
		t.Error("connection not recorded as started")
	}
	f.noFurtherLookup(t)
}

// AC2: a stop that lands while a start is in progress leaves the connection
// stopped, and a later start registers the webhooks again.
func TestFrontSystemsStartGuard_StopMidStartLeavesItStopped(t *testing.T) {
	cases := []struct {
		name string
		hook func(f *fgFixture, stop func())
	}{
		{"before the lookup", func(f *fgFixture, stop func()) {
			f.c.starts.AfterReserve = func(id string) {
				if id == fgConn {
					stop()
				}
			}
		}},
		{"during registration", func(f *fgFixture, stop func()) {
			f.setOnPost(func(int) bool { stop(); return false })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFGFixture(t)
			f.expectLookups(3, 0) // the stopped start, the later start, sentinel
			var stops atomic.Int32
			tc.hook(f, func() {
				if stops.CompareAndSwap(0, 1) {
					f.c.handleStopCommand(fgCmd("stop"))
				}
			})

			fgAwait(t, goDone(f.start), "start")
			if n := stops.Load(); n != 1 {
				t.Fatalf("stops landed mid-start = %d, want 1 (the hook never ran)", n)
			}
			if f.isActive() {
				t.Fatal("connection recorded as started although a stop arrived mid-start")
			}
			before := f.registered()

			fgAwait(t, goDone(f.start), "later start")
			after := f.registered()
			for _, ev := range fgEvents {
				if after[ev] != before[ev]+1 {
					t.Errorf("%s: the later start registered %d time(s), want 1", ev, after[ev]-before[ev])
				}
			}
			if !f.isActive() {
				t.Error("later start did not record the connection as started")
			}
			f.noFurtherLookup(t)
		})
	}
}

// AC3: a start refused while another is in flight runs once that one fails,
// on its lookup or on the registration, and the webhooks end up registered
// once.
func TestFrontSystemsStartGuard_RefusedStartRunsWhenFirstFails(t *testing.T) {
	t.Run("lookup fails", func(t *testing.T) {
		for _, tc := range fgSecondStarts {
			t.Run(tc.name, func(t *testing.T) {
				f := newFGFixture(t)
				f.expectLookups(3, 1) // failed lookup, the refused start's, sentinel
				var claims atomic.Int32
				reached, release := make(chan struct{}), make(chan struct{})
				f.c.starts.AfterReserve = func(id string) {
					if id == fgConn && claims.Add(1) == 1 {
						close(reached)
						waitOrTimeout(release)
					}
				}

				first := goDone(f.start)
				fgAwait(t, reached, "first start to claim the connection")
				fgAwait(t, goDone(func() { tc.start(f.c) }), "second start, refused while the first is in flight")
				if n := claims.Load(); n != 1 {
					t.Fatalf("starts that claimed the connection while the first is held = %d, want 1", n)
				}
				close(release)
				fgAwait(t, first, "first start, and the refused start it runs again")

				if n := claims.Load(); n != 2 {
					t.Errorf("starts that claimed the connection = %d, want 2 (the failed one, then the refused one)", n)
				}
				f.wantRegistered(t, 1, "after the retried start")
				if !f.isActive() {
					t.Error("connection not recorded as started")
				}
				f.noFurtherLookup(t)
			})
		}
	})
	t.Run("registration fails", func(t *testing.T) {
		for _, tc := range fgSecondStarts {
			t.Run(tc.name, func(t *testing.T) {
				f := newFGFixture(t)
				f.expectLookups(3, 0) // failed start, the refused start's, sentinel
				second := make(chan struct{})
				f.setOnPost(func(n int) bool {
					if n != 1 {
						return false
					}
					// The second start arrives while the first registers, and
					// the registration then fails.
					go func() {
						defer close(second)
						tc.start(f.c)
					}()
					waitOrTimeout(second)
					return true
				})

				fgAwait(t, goDone(f.start), "first start, and the refused start it runs again")
				fgAwait(t, second, "second start")

				f.mu.Lock()
				failed := f.failed
				f.mu.Unlock()
				if failed != 1 {
					t.Errorf("failed registrations = %d, want 1", failed)
				}
				f.wantRegistered(t, 1, "after the retried start")
				if !f.isActive() {
					t.Error("connection not recorded as started")
				}
				f.noFurtherLookup(t)
			})
		}
	})
}
