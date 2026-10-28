package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// #423 and #424 against a real database: the file consumer's own
// startConnection and handleStopCommand, with the connection lookup and the
// status writes going to Postgres. Skipped without VRSKY_TEST_POSTGRES_URL;
// CI sets it.
//
//   - #423: a stop that lands after a start has made the connection active,
//     before its "running" write, ends with connections.status = 'stopped'
//     and no watcher.
//   - #424: a start refused because another is in flight runs once that one
//     fails, and ends 'running' with one watcher.
//
// Ordering is forced through the guard's AfterActivate hook, the consumer's
// beforeStartWork hook and a row lock held in a transaction; waits are
// bounded (raceWaitStep).

// --- database with a failing lookup -------------------------------------------

// lookupQuery is the connection lookup at the start of startConnection.
const lookupQuery = "SELECT id, tenant_id, name, nodes, edges FROM connections"

// flakyLookups wraps the Postgres driver. Every statement goes to the real
// database, except that the next failNext connection lookups fail as if the
// connection to the database had dropped: a transient error during a start.
type flakyLookups struct {
	base     driver.Connector
	failNext atomic.Int32
	lookups  atomic.Int32
}

func (f *flakyLookups) Connect(ctx context.Context) (driver.Conn, error) {
	c, err := f.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return flakyConn{Conn: c, f: f}, nil
}

func (f *flakyLookups) Driver() driver.Driver { return f.base.Driver() }

type flakyConn struct {
	driver.Conn
	f *flakyLookups
}

func (c flakyConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, lookupQuery) {
		c.f.lookups.Add(1)
		for {
			n := c.f.failNext.Load()
			if n <= 0 {
				break
			}
			if c.f.failNext.CompareAndSwap(n, n-1) {
				return nil, errors.New("read tcp: connection reset by peer")
			}
		}
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func (c flakyConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}

// --- logs per connection ------------------------------------------------------

// connLogs counts log records by message and connection_id, so watchers can
// be counted per connection: each watcher logs watchStartedMsg when it starts
// and watchStoppedMsg when its context is cancelled.
type connLogs struct {
	mu sync.Mutex
	n  map[string]int
}

func (l *connLogs) count(msg, connID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n[msg+"|"+connID]
}

// watchers reports how many watchers were started for connID and how many of
// them are still running.
func (l *connLogs) watchers(connID string) (started, live int) {
	s := l.count(watchStartedMsg, connID)
	return s, s - l.count(watchStoppedMsg, connID)
}

type connLogHandler struct {
	l     *connLogs
	attrs []slog.Attr
}

func (h connLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h connLogHandler) Handle(_ context.Context, r slog.Record) error {
	conn := ""
	for _, a := range h.attrs {
		if a.Key == "connection_id" {
			conn = a.Value.String()
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "connection_id" {
			conn = a.Value.String()
		}
		return true
	})
	h.l.mu.Lock()
	defer h.l.mu.Unlock()
	if h.l.n == nil {
		h.l.n = make(map[string]int)
	}
	h.l.n[r.Message+"|"+conn]++
	return nil
}
func (h connLogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return connLogHandler{l: h.l, attrs: append(append([]slog.Attr(nil), h.attrs...), as...)}
}
func (h connLogHandler) WithGroup(string) slog.Handler { return h }

// --- fixture ------------------------------------------------------------------

type statusPGEnv struct {
	t     *testing.T
	ctx   context.Context
	db    *sql.DB       // plain Postgres, for setup and assertions
	flaky *flakyLookups // the consumer's driver
	cdb   *sql.DB       // the consumer's database
}

func newStatusPGEnv(t *testing.T) *statusPGEnv {
	t.Helper()
	url := testdb.Fresh(t)
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	base, err := pq.NewConnector(url)
	if err != nil {
		t.Fatal(err)
	}
	flaky := &flakyLookups{base: base}
	cdb := sql.OpenDB(flaky)
	t.Cleanup(func() { _ = cdb.Close() })
	return &statusPGEnv{t: t, ctx: context.Background(), db: db, flaky: flaky, cdb: cdb}
}

// with is the env for a subtest: the same databases, failures reported (and
// cleanups run) on t.
func (e *statusPGEnv) with(t *testing.T) *statusPGEnv {
	c := *e
	c.t = t
	return &c
}

func (e *statusPGEnv) tenant() string {
	e.t.Helper()
	var owner, tenant string
	if err := e.db.QueryRowContext(e.ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('status-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		e.t.Fatal(err)
	}
	if err := e.db.QueryRowContext(e.ctx, `INSERT INTO tenants (name, slug, owner_id)
		VALUES ('status', 'status-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(&tenant); err != nil {
		e.t.Fatal(err)
	}
	return tenant
}

// connection inserts a file connection watching "inbox" with the given status
// and no started_at, and returns its id.
func (e *statusPGEnv) connection(tenant, status string) string {
	e.t.Helper()
	id := uuid.New().String()
	if _, err := e.db.ExecContext(e.ctx, `INSERT INTO connections (id, tenant_id, name, status, nodes, edges)
		VALUES ($1, $2, $3, $4, $5::jsonb, '[]'::jsonb)`, id, tenant, "file-"+id, status, inboxNodes); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *statusPGEnv) delete(id string) {
	e.t.Helper()
	if _, err := e.db.ExecContext(e.ctx, `DELETE FROM connections WHERE id = $1`, id); err != nil {
		e.t.Fatal(err)
	}
}

// status reads connections.status and whether started_at is set.
func (e *statusPGEnv) status(id string) (string, bool) {
	e.t.Helper()
	var status string
	var started sql.NullTime
	if err := e.db.QueryRowContext(e.ctx, `SELECT status, started_at FROM connections WHERE id = $1`, id).
		Scan(&status, &started); err != nil {
		e.t.Fatal(err)
	}
	return status, started.Valid
}

func (e *statusPGEnv) requireStatus(id, want string) {
	e.t.Helper()
	if got, _ := e.status(id); got != want {
		e.t.Errorf("connections.status of %s = %q, want %q", id, got, want)
	}
}

// statusConsumer is a file consumer on the env's database.
type statusConsumer struct {
	t    *testing.T
	e    *statusPGEnv
	c    *fileConsumer
	logs *connLogs
}

func (e *statusPGEnv) consumer() *statusConsumer {
	e.t.Helper()
	logs := &connLogs{}
	sc := &statusConsumer{t: e.t, e: e, logs: logs}
	sc.c = &fileConsumer{
		db:                e.cdb,
		logger:            slog.New(connLogHandler{l: logs}),
		publish:           (&recordingProbe{}).publish,
		inlineMax:         1 << 20,
		baseDir:           e.t.TempDir(),
		activeConnections: make(map[string]*ActiveConnection),
		pollInterval:      racePoll,
	}
	e.t.Cleanup(sc.cancelAll)
	return sc
}

func (sc *statusConsumer) cancelAll() {
	sc.c.mu.Lock()
	defer sc.c.mu.Unlock()
	for _, ac := range sc.c.activeConnections {
		ac.Cancel()
	}
}

func commandMsg(kind, connID, tenant string) *nats.Msg {
	data, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	return &nats.Msg{Subject: "vrsky.commands." + tenant + ".connection." + kind, Data: data}
}

func (sc *statusConsumer) start(connID, tenant string) {
	sc.c.handleStartCommand(commandMsg("start", connID, tenant))
}

func (sc *statusConsumer) stop(connID, tenant string) {
	sc.c.handleStopCommand(commandMsg("stop", connID, tenant))
}

func (sc *statusConsumer) restore() {
	sdk.RestoreRunning(context.Background(), sc.e.cdb, sc.c.logger, "file", sc.c.startConnection)
}

// async runs f on its own goroutine and returns a channel closed when it ends.
func async(f func()) <-chan struct{} {
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	return done
}

// afterActivate runs f once, on the start's goroutine, the first time connID
// becomes active: after Activate, before the start's "running" write.
func (sc *statusConsumer) afterActivate(connID string, f func()) {
	var done atomic.Bool
	sc.c.starts.AfterActivate = func(id string) {
		if id == connID && done.CompareAndSwap(false, true) {
			f()
		}
	}
}

// holdFirstStart holds the first start that reaches beforeStartWork (after
// its claim, before its lookup) until release is closed; later starts pass.
func (sc *statusConsumer) holdFirstStart(connID string) (entered, release chan struct{}) {
	entered, release = make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	sc.c.beforeStartWork = func(id string) {
		if id == connID && calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-time.After(raceWaitStep):
			}
		}
	}
	return entered, release
}

// requireWatchers waits until connID has started `started` watchers of which
// `live` still run, then checks the active map agrees.
func (sc *statusConsumer) requireWatchers(connID string, started, live int) {
	sc.t.Helper()
	harness.Eventually(sc.t, raceWaitStep, fmt.Sprintf("%s: %d watcher(s) started, %d running", connID, started, live), func() bool {
		s, l := sc.logs.watchers(connID)
		return s == started && l == live
	})
	if got, want := sc.c.getActiveConnection(connID) != nil, live == 1; got != want {
		sc.t.Errorf("%s: active = %v, want %v", connID, got, want)
	}
}

// waitRowLockWait waits until a statement on this database is blocked on a
// row lock with its text starting with prefix.
func (e *statusPGEnv) waitRowLockWait(prefix string) {
	e.t.Helper()
	harness.Eventually(e.t, raceWaitStep, "a statement waiting on the row lock: "+prefix, func() bool {
		var n int
		if err := e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE $1`, prefix+"%").Scan(&n); err != nil {
			e.t.Fatal(err)
		}
		return n > 0
	})
}

// --- tests ----------------------------------------------------------------------

// #423 AC 1: a stop that lands after the connection became active and before
// the start's "running" write ends 'stopped' with no watcher, whichever of
// the two status writes reaches Postgres first.
func TestStartStatusPG_StopAfterActivateEndsStopped(t *testing.T) {
	env := newStatusPGEnv(t)
	tenant := env.tenant()

	cases := []struct {
		name string
		run  func(t *testing.T, sc *statusConsumer, id string)
	}{
		{
			// The stop handler runs in full inside the window: its
			// 'stopped' reaches the database before the start's 'running'.
			name: "stopped written first",
			run: func(t *testing.T, sc *statusConsumer, id string) {
				sc.afterActivate(id, func() { sc.stop(id, tenant) })
				waitDone(t, async(func() { sc.start(id, tenant) }), "start")
			},
		},
		{
			// The start's 'running' UPDATE is in flight (blocked on a row
			// lock) when the stop arrives; the stop's write lands after it.
			name: "running in flight, stopped written after",
			run: func(t *testing.T, sc *statusConsumer, id string) {
				e := sc.e
				tx, err := e.db.BeginTx(e.ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				if _, err := tx.ExecContext(e.ctx, `SELECT 1 FROM connections WHERE id = $1 FOR UPDATE`, id); err != nil {
					t.Fatal(err)
				}
				started := async(func() { sc.start(id, tenant) })
				e.waitRowLockWait("UPDATE connections SET status")
				stopped := async(func() { sc.stop(id, tenant) })
				// The stop's critical section is done once the connection
				// has left the active map; its write now waits for the
				// start's.
				harness.Eventually(t, raceWaitStep, "stop to take the connection out of the active map", func() bool {
					return sc.c.getActiveConnection(id) == nil
				})
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				waitDone(t, started, "start")
				waitDone(t, stopped, "stop")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := env.with(t)
			// 'running' as the boot restore finds it, so ending 'stopped'
			// takes the stop's write landing last.
			id := e.connection(tenant, "running")
			sc := e.consumer()
			tc.run(t, sc, id)

			e.requireStatus(id, "stopped")
			if _, live := sc.logs.watchers(id); live < 0 || live > 1 {
				t.Fatalf("watcher count out of range: %d", live)
			}
			harness.Eventually(t, raceWaitStep, "every watcher started for the stopped connection to stop", func() bool {
				_, live := sc.logs.watchers(id)
				return live == 0
			})
			if sc.c.getActiveConnection(id) != nil {
				t.Error("connection still active after the stop")
			}
			// A start sent after the stop still works and wins.
			sc.start(id, tenant)
			e.requireStatus(id, "running")
			harness.Eventually(t, raceWaitStep, "the new start's watcher to run", func() bool {
				_, live := sc.logs.watchers(id)
				return live == 1
			})
			if sc.c.getActiveConnection(id) == nil {
				t.Error("start after the stop did not make the connection active")
			}
		})
	}
}

// #424 AC 3: a start refused because another start of the same connection is
// in flight runs once that one fails on a transient database error, and the
// connection ends 'running' with one watcher.
func TestStartStatusPG_RefusedStartRunsWhenFirstFails(t *testing.T) {
	env := newStatusPGEnv(t)
	tenant := env.tenant()

	cases := []struct {
		name   string
		status string // before the starts
		first  func(sc *statusConsumer, id string)
	}{
		{
			// The pod restarts; the boot restore's lookup hits a dropped
			// database connection while a start command arrives.
			name:   "boot restore fails, start command refused meanwhile",
			status: "running",
			first:  func(sc *statusConsumer, _ string) { sc.restore() },
		},
		{
			name:   "start command fails, second start command refused meanwhile",
			status: "stopped",
			first:  func(sc *statusConsumer, id string) { sc.start(id, tenant) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := env.with(t)
			id := e.connection(tenant, tc.status)
			t.Cleanup(func() { e.delete(id) })
			sc := e.consumer()
			e.flaky.lookups.Store(0)
			e.flaky.failNext.Store(1)

			entered, release := sc.holdFirstStart(id)
			first := async(func() { tc.first(sc, id) })
			waitDone(t, entered, "first start to take its claim")

			// Refused: it returns at once without a lookup.
			waitDone(t, async(func() { sc.start(id, tenant) }), "refused start")
			if n := e.flaky.lookups.Load(); n != 0 {
				t.Fatalf("refused start looked the connection up (%d lookups)", n)
			}

			close(release)
			waitDone(t, first, "first start and the refused start it runs")

			if n := e.flaky.lookups.Load(); n != 2 {
				t.Errorf("lookups = %d, want 2 (the failed one and the refused start's)", n)
			}
			if st, started := e.status(id); st != "running" || !started {
				t.Errorf("connections.status = %q, started_at set = %v; want running with started_at", st, started)
			}
			sc.requireWatchers(id, 1, 1)
		})
	}
}

// #423/#424 AC 4: two starts of one connection never give two watchers,
// whether they overlap by chance or one is refused while the other is held
// (and then activates, so the refused start is not run again).
func TestStartStatusPG_NoDoubleStart(t *testing.T) {
	env := newStatusPGEnv(t)
	tenant := env.tenant()

	t.Run("refused while the first is held, first activates", func(t *testing.T) {
		e := env.with(t)
		id := e.connection(tenant, "stopped")
		t.Cleanup(func() { e.delete(id) })
		sc := e.consumer()
		e.flaky.lookups.Store(0)

		entered, release := sc.holdFirstStart(id)
		first := async(func() { sc.start(id, tenant) })
		waitDone(t, entered, "first start to take its claim")
		waitDone(t, async(func() { sc.start(id, tenant) }), "refused start")
		close(release)
		waitDone(t, first, "first start")

		if n := e.flaky.lookups.Load(); n != 1 {
			t.Errorf("lookups = %d, want 1 (the refused start ran again)", n)
		}
		e.requireStatus(id, "running")
		sc.requireWatchers(id, 1, 1)
	})

	const iterations = 20
	for i := 0; i < iterations; i++ {
		t.Run(fmt.Sprintf("concurrent-%02d", i), func(t *testing.T) {
			e := env.with(t)
			// Restore needs a 'running' row to find; for two commands start
			// from 'stopped'.
			viaRestore := i%2 == 0
			status := "stopped"
			if viaRestore {
				status = "running"
			}
			id := e.connection(tenant, status)
			sc := e.consumer()
			t.Cleanup(func() { sc.cancelAll(); e.delete(id) })

			first := func() { sc.start(id, tenant) }
			if viaRestore {
				first = sc.restore
			}
			gate := make(chan struct{})
			a := async(func() { <-gate; first() })
			b := async(func() { <-gate; sc.start(id, tenant) })
			close(gate)
			waitDone(t, a, "first start")
			waitDone(t, b, "second start")

			e.requireStatus(id, "running")
			sc.requireWatchers(id, 1, 1)
		})
	}
}

// AC 6: connections of two tenants on one consumer are independent. A stop of
// one, landing in the middle of the other's start or its own, leaves the
// other 'running' with its watcher.
func TestStartStatusPG_TenantsIndependent(t *testing.T) {
	env := newStatusPGEnv(t)
	tenantA, tenantB := env.tenant(), env.tenant()

	cases := []struct {
		name string
		// statusA is A's status before; wantA after.
		statusA, wantA string
		run            func(t *testing.T, sc *statusConsumer, a, b string)
		// watchers A ever started
		startedA int
	}{
		{
			// A's stop lands in B's window, between B's Activate and its
			// 'running' write.
			name:    "stop of A inside B's start",
			statusA: "stopped", wantA: "stopped", startedA: 1,
			run: func(t *testing.T, sc *statusConsumer, a, b string) {
				sc.start(a, tenantA)
				sc.e.requireStatus(a, "running")
				sc.afterActivate(b, func() { sc.stop(a, tenantA) })
				waitDone(t, async(func() { sc.start(b, tenantB) }), "start of B")
			},
		},
		{
			// Both start together; A is stopped in its own window.
			name:    "both starting, A stopped after its Activate",
			statusA: "running", wantA: "stopped", startedA: -1, // 0 or 1: the watcher may or may not get going
			run: func(t *testing.T, sc *statusConsumer, a, b string) {
				sc.afterActivate(a, func() { sc.stop(a, tenantA) })
				gate := make(chan struct{})
				da := async(func() { <-gate; sc.start(a, tenantA) })
				db := async(func() { <-gate; sc.start(b, tenantB) })
				close(gate)
				waitDone(t, da, "start of A")
				waitDone(t, db, "start of B")
			},
		},
		{
			// A is stopped before it activates (the management API set
			// 'stopped' already); B starts meanwhile.
			name:    "A stopped mid-start, B starts meanwhile",
			statusA: "stopped", wantA: "stopped", startedA: 0,
			run: func(t *testing.T, sc *statusConsumer, a, b string) {
				entered, release := sc.holdFirstStart(a)
				da := async(func() { sc.start(a, tenantA) })
				waitDone(t, entered, "start of A to take its claim")
				waitDone(t, async(func() { sc.start(b, tenantB) }), "start of B")
				sc.stop(a, tenantA)
				close(release)
				waitDone(t, da, "start of A")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := env.with(t)
			a := e.connection(tenantA, tc.statusA)
			b := e.connection(tenantB, "stopped")
			sc := e.consumer()
			t.Cleanup(func() { sc.cancelAll(); e.delete(a); e.delete(b) })

			tc.run(t, sc, a, b)

			e.requireStatus(a, tc.wantA)
			e.requireStatus(b, "running")
			sc.requireWatchers(b, 1, 1)
			if tc.startedA >= 0 {
				sc.requireWatchers(a, tc.startedA, 0)
			} else {
				harness.Eventually(t, raceWaitStep, "A's watchers to stop", func() bool {
					_, live := sc.logs.watchers(a)
					return live == 0
				})
				if sc.c.getActiveConnection(a) != nil {
					t.Error("A still active after its stop")
				}
			}
		})
	}
}
