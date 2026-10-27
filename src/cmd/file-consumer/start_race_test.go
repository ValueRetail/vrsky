package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #375: two starts for one file connection (the boot restore and a
// start command, or two commands) must never end with two watchers, a failed
// start must not block the next one, and a stop that lands while a start is
// in progress must win.
//
// The database is a small fake driver rather than sqlmock: sqlmock answers
// each expectation once, so a second concurrent start would fail on the mock
// by itself and hide the very double start these tests look for. The fake
// answers every lookup and status write and counts them.

const (
	raceConnID   = "conn-file-race"
	raceTenant   = "tenant-race"
	racePoll     = 10 * time.Millisecond
	raceWaitStep = 5 * time.Second // upper bound on any single wait, so a regression fails instead of hanging
)

const inboxNodes = `[{"id":"c1","type":"consumer","config":{"type":"file","file":{"path":"inbox"}}}]`

func nodesWithPath(path string) string {
	b, _ := json.Marshal(path)
	return `[{"id":"c1","type":"consumer","config":{"type":"file","file":{"path":` + string(b) + `}}}]`
}

// --- fake database -----------------------------------------------------------

// fakeConnDB answers the three statements the start path and the boot restore
// issue, as often as they are asked, and counts them.
type fakeConnDB struct {
	mu        sync.Mutex
	connID    string
	tenant    string
	nodes     string
	lookupErr error // when set, the connection lookup fails with it

	restoreQueries int
	lookups        int
	statusWrites   map[string]int // status value -> number of UPDATE ... SET status writes
}

func (f *fakeConnDB) set(nodes string, lookupErr error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes, f.lookupErr = nodes, lookupErr
}

func (f *fakeConnDB) counts() (lookups, running, stopped int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookups, f.statusWrites["running"], f.statusWrites["stopped"]
}

func (f *fakeConnDB) query(query string, args []driver.NamedValue) (driver.Rows, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.Contains(query, "SELECT id::text, tenant_id FROM connections"):
		f.restoreQueries++
		return &fakeRows{cols: []string{"id", "tenant_id"},
			data: [][]driver.Value{{f.connID, f.tenant}}}, nil
	case strings.Contains(query, "SELECT id, tenant_id, name, nodes, edges FROM connections"):
		f.lookups++
		if f.lookupErr != nil {
			return nil, f.lookupErr
		}
		if len(args) != 2 || args[0].Value != f.connID || args[1].Value != f.tenant {
			return &fakeRows{cols: []string{"id", "tenant_id", "name", "nodes", "edges"}}, nil
		}
		return &fakeRows{cols: []string{"id", "tenant_id", "name", "nodes", "edges"},
			data: [][]driver.Value{{f.connID, f.tenant, "File Conn", []byte(f.nodes), []byte(`[]`)}}}, nil
	}
	return nil, fmt.Errorf("fakeConnDB: unexpected query %q", query)
}

func (f *fakeConnDB) exec(query string, args []driver.NamedValue) (driver.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Contains(query, "UPDATE connections SET status") {
		if f.statusWrites == nil {
			f.statusWrites = make(map[string]int)
		}
		if len(args) > 0 {
			if s, ok := args[0].Value.(string); ok {
				f.statusWrites[s]++
			}
		}
		return driver.RowsAffected(1), nil
	}
	// Anything else (e.g. a last_payload write) is accepted and ignored.
	return driver.RowsAffected(1), nil
}

type fakeConnector struct{ db *fakeConnDB }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return fakeConn(c), nil }
func (c fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fakeDriver: use sql.OpenDB with fakeConnector")
}

type fakeConn struct{ db *fakeConnDB }

func (fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fakeConn: prepared statements not supported")
}
func (fakeConn) Close() error              { return nil }
func (fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("fakeConn: no transactions") }
func (c fakeConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.db.query(q, args)
}
func (c fakeConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.db.exec(q, args)
}

type fakeRows struct {
	cols []string
	data [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}

// --- log counting ------------------------------------------------------------

// msgCounter counts log records by message. Every watcher goroutine logs
// "Starting directory watch" once, so its count is the number of watchers.
type msgCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func (m *msgCounter) count(msg string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.n[msg]
}

type countingHandler struct{ c *msgCounter }

func (h countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	if h.c.n == nil {
		h.c.n = make(map[string]int)
	}
	h.c.n[r.Message]++
	return nil
}
func (h countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h countingHandler) WithGroup(string) slog.Handler      { return h }

const watchStartedMsg = "Starting directory watch"

// --- fixture -----------------------------------------------------------------

type raceFixture struct {
	t     *testing.T
	base  string
	inbox string
	fdb   *fakeConnDB
	db    *sql.DB
	probe *recordingProbe
	logs  *msgCounter
	c     *fileConsumer
}

// newRaceFixture builds a consumer by hand around the fake database, a
// recording publish and a short poll interval. Every watcher still active at
// the end of the test is cancelled.
func newRaceFixture(t *testing.T) *raceFixture {
	t.Helper()
	base := t.TempDir()
	fdb := &fakeConnDB{connID: raceConnID, tenant: raceTenant, nodes: inboxNodes}
	db := sql.OpenDB(fakeConnector{db: fdb})
	t.Cleanup(func() { _ = db.Close() })
	f := &raceFixture{
		t:     t,
		base:  base,
		inbox: filepath.Join(base, raceTenant, "inbox"),
		fdb:   fdb,
		db:    db,
		probe: &recordingProbe{},
		logs:  &msgCounter{},
	}
	f.c = &fileConsumer{
		db:                db,
		logger:            slog.New(countingHandler{c: f.logs}),
		publish:           f.probe.publish,
		inlineMax:         1 << 20,
		baseDir:           base,
		activeConnections: make(map[string]*ActiveConnection),
		pollInterval:      racePoll,
	}
	t.Cleanup(f.cancelAll)
	return f
}

func (f *raceFixture) cancelAll() {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	for _, ac := range f.c.activeConnections {
		ac.Cancel()
	}
}

func (f *raceFixture) startCmd() *nats.Msg {
	data, err := json.Marshal(map[string]string{"connection_id": raceConnID, "tenant_id": raceTenant})
	if err != nil {
		f.t.Fatalf("marshal command: %v", err)
	}
	return &nats.Msg{Subject: "vrsky.commands." + raceTenant + ".connection.start", Data: data}
}

func (f *raceFixture) stopCmd() *nats.Msg {
	data, err := json.Marshal(map[string]string{"connection_id": raceConnID, "tenant_id": raceTenant})
	if err != nil {
		f.t.Fatalf("marshal command: %v", err)
	}
	return &nats.Msg{Subject: "vrsky.commands." + raceTenant + ".connection.stop", Data: data}
}

func (f *raceFixture) restore() {
	sdk.RestoreRunning(context.Background(), f.db, f.c.logger, "file", f.c.startConnection)
}

func (f *raceFixture) command() { f.c.handleStartCommand(f.startCmd()) }

// requireOneWatcherPublishingOnce writes name into the inbox, waits for it to
// be published, lets several more poll intervals pass, and fails unless
// exactly one envelope was published for it and exactly one watcher was ever
// started. A second watcher (its baseline taken before the file existed)
// would publish the file a second time within a few polls.
func (f *raceFixture) requireOneWatcherPublishingOnce(step, name string) {
	f.t.Helper()
	content := []byte(`{"file":"` + name + `"}`)
	if err := os.WriteFile(filepath.Join(f.inbox, name), content, 0o600); err != nil {
		f.t.Fatalf("%s: write %s: %v", step, name, err)
	}
	harness.Eventually(f.t, raceWaitStep, step+": "+name+" published", func() bool {
		return len(f.probe.forName(name)) > 0
	})
	time.Sleep(10 * racePoll)
	envs := f.probe.forName(name)
	if len(envs) != 1 {
		f.t.Fatalf("%s: %d envelopes for %s, want exactly 1 (%s)", step, len(envs), name, describe(f.probe.published()))
	}
	assertWatchEnvelope(f.t, envs[0], name, content)
	if env := envs[0]; env.IntegrationID != raceConnID || env.TenantID != raceTenant {
		f.t.Errorf("%s: envelope for %s/%s, want %s/%s", step, env.TenantID, env.IntegrationID, raceTenant, raceConnID)
	}
}

// holdFirstStart makes the first start that reaches the start window (past
// its "already active" check, before the database lookup) signal entered and
// wait there until release is closed. Any later start passes straight on, so
// a start the gate wrongly lets through fails the assertions instead of
// deadlocking. The hold gives up after raceWaitStep so nothing hangs.
func (f *raceFixture) holdFirstStart() (entered, release chan struct{}) {
	entered = make(chan struct{})
	release = make(chan struct{})
	var calls atomic.Int32
	f.c.beforeStartWork = func(string) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-time.After(raceWaitStep):
			}
		}
	}
	return entered, release
}

// waitDone fails the test if done is not closed within raceWaitStep.
func waitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(raceWaitStep):
		t.Fatalf("%s did not finish within %s", what, raceWaitStep)
	}
}

// --- tests -------------------------------------------------------------------

// AC 1 and 3: the boot restore and a start command for the same connection,
// released together, many times over (run under -race). Each iteration has
// its own consumer, database and directory. Afterwards there is exactly one
// watcher, one "running" status write and one publish per file.
func TestStartRace_RestoreAndStartCommandConcurrent(t *testing.T) {
	const iterations = 60
	for i := 0; i < iterations; i++ {
		t.Run(fmt.Sprintf("iter-%02d", i), func(t *testing.T) {
			t.Parallel()
			f := newRaceFixture(t)

			first, second := f.restore, f.command
			if i%2 == 1 {
				first, second = second, first
			}
			gate := make(chan struct{})
			var wg sync.WaitGroup
			for _, start := range []func(){first, second} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-gate
					start()
				}()
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			close(gate)
			waitDone(t, done, "concurrent restore and start command")

			if f.c.getActiveConnection(raceConnID) == nil {
				t.Fatal("connection not active after both starts returned")
			}
			if _, running, _ := f.fdb.counts(); running != 1 {
				t.Errorf("status running written %d times, want 1", running)
			}
			f.requireOneWatcherPublishingOnce("after concurrent starts", "race.json")
			if n := f.logs.count(watchStartedMsg); n != 1 {
				t.Errorf("%d watchers started (%q logged %d times), want 1", n, watchStartedMsg, n)
			}
		})
	}
}

// AC 1, deterministic: one start is held inside the window (past its "already
// active" check, before its database lookup) while the other start arrives
// and runs to completion. The second must not start a watcher of its own,
// whichever of restore and command is the one being held.
func TestStartRace_SecondStartWhileFirstInProgress(t *testing.T) {
	cases := []struct {
		name          string
		held, arrives func(f *raceFixture)
	}{
		{"restore held, start command arrives", (*raceFixture).restore, (*raceFixture).command},
		{"start command held, restore arrives", (*raceFixture).command, (*raceFixture).restore},
		{"start command held, second start command arrives", (*raceFixture).command, (*raceFixture).command},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRaceFixture(t)

			entered, release := f.holdFirstStart()

			heldDone := make(chan struct{})
			go func() { defer close(heldDone); tc.held(f) }()
			waitDone(t, entered, "held start reaching the start window")

			arrivedDone := make(chan struct{})
			go func() { defer close(arrivedDone); tc.arrives(f) }()
			waitDone(t, arrivedDone, "second start while the first is in progress")

			close(release)
			waitDone(t, heldDone, "held start after release")

			if f.c.getActiveConnection(raceConnID) == nil {
				t.Fatal("connection not active after the held start finished")
			}
			lookups, running, _ := f.fdb.counts()
			if running != 1 {
				t.Errorf("status running written %d times, want 1", running)
			}
			if lookups < 1 {
				t.Errorf("connection looked up %d times, want at least 1", lookups)
			}
			f.requireOneWatcherPublishingOnce("after overlapping starts", "overlap.json")
			if n := f.logs.count(watchStartedMsg); n != 1 {
				t.Errorf("%d watchers started, want 1", n)
			}
		})
	}
}

// AC 1: a start for a connection that is already active is a no-op, from
// either path.
func TestStartRace_StartWhenAlreadyActiveIsNoop(t *testing.T) {
	f := newRaceFixture(t)
	f.command()
	if f.c.getActiveConnection(raceConnID) == nil {
		t.Fatal("connection not active after the first start")
	}
	f.restore()
	f.command()
	if _, running, _ := f.fdb.counts(); running != 1 {
		t.Errorf("status running written %d times, want 1", running)
	}
	f.requireOneWatcherPublishingOnce("after repeated starts", "again.json")
	if n := f.logs.count(watchStartedMsg); n != 1 {
		t.Errorf("%d watchers started, want 1", n)
	}
}

// AC 2: a start that fails part-way leaves nothing behind, so the next start
// for the same connection succeeds and its watcher publishes.
func TestStartRace_FailedStartLeavesNothingBehind(t *testing.T) {
	outside := t.TempDir() // a real absolute directory that is not under the base
	cases := []struct {
		name      string
		nodes     string
		lookupErr error
	}{
		{name: "database error on lookup", nodes: inboxNodes, lookupErr: errors.New("connection reset by peer")},
		{name: "path into another tenant", nodes: nodesWithPath("../other-tenant")},
		{name: "absolute path outside the base", nodes: nodesWithPath(filepath.Join(outside, "elsewhere"))},
	}
	for _, tc := range cases {
		for _, via := range []string{"restore", "command"} {
			t.Run(tc.name+" via "+via, func(t *testing.T) {
				f := newRaceFixture(t)
				f.fdb.set(tc.nodes, tc.lookupErr)

				if via == "restore" {
					f.restore()
				} else {
					f.command()
				}
				if f.c.getActiveConnection(raceConnID) != nil {
					t.Fatal("failed start left the connection active")
				}
				lookups, running, _ := f.fdb.counts()
				if lookups != 1 {
					t.Fatalf("failing start looked the connection up %d times, want 1", lookups)
				}
				if running != 0 {
					t.Errorf("failed start wrote status running %d times, want 0", running)
				}
				if n := f.logs.count(watchStartedMsg); n != 0 {
					t.Errorf("failed start started %d watchers, want 0", n)
				}
				if _, err := os.Stat(filepath.Join(f.base, "other-tenant")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("refused path was created anyway (stat err = %v)", err)
				}
				if _, err := os.Stat(filepath.Join(outside, "elsewhere")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("refused absolute path was created anyway (stat err = %v)", err)
				}

				// The config is fixed (or the database is back); the next start
				// must go through, not be refused as "already starting".
				f.fdb.set(inboxNodes, nil)
				f.command()
				if f.c.getActiveConnection(raceConnID) == nil {
					t.Fatal("start after a failed start did not make the connection active")
				}
				lookups, running, _ = f.fdb.counts()
				if lookups != 2 {
					t.Errorf("connection looked up %d times in all, want 2 (the retry was refused before its lookup?)", lookups)
				}
				if running != 1 {
					t.Errorf("status running written %d times, want 1", running)
				}
				f.requireOneWatcherPublishingOnce("after retry", "retry.json")
				if n := f.logs.count(watchStartedMsg); n != 1 {
					t.Errorf("%d watchers started, want 1", n)
				}
			})
		}
	}
}

// AC 4: a stop that arrives while a start is in progress (past its "already
// active" check, before the connection is active) wins. Nothing is active
// afterwards, no watcher publishes a file written later, and the abandoned
// start did not write status running. A start sent after the stop still works.
func TestStartRace_StopDuringStartWins(t *testing.T) {
	for _, via := range []string{"restore", "command"} {
		t.Run("start via "+via, func(t *testing.T) {
			f := newRaceFixture(t)
			if err := os.MkdirAll(f.inbox, 0o755); err != nil {
				t.Fatal(err)
			}

			entered, release := f.holdFirstStart()

			startDone := make(chan struct{})
			go func() {
				defer close(startDone)
				if via == "restore" {
					f.restore()
				} else {
					f.command()
				}
			}()
			waitDone(t, entered, "start reaching the start window")

			f.c.handleStopCommand(f.stopCmd())

			close(release)
			waitDone(t, startDone, "start after the stop")

			if f.c.getActiveConnection(raceConnID) != nil {
				t.Fatal("connection active after a stop that arrived during its start")
			}
			if _, running, _ := f.fdb.counts(); running != 0 {
				t.Errorf("abandoned start wrote status running %d times, want 0", running)
			}

			// A file written now must not be published by anything.
			if err := os.WriteFile(filepath.Join(f.inbox, "after-stop.json"), []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			time.Sleep(10 * racePoll)
			if envs := f.probe.published(); len(envs) != 0 {
				t.Fatalf("%d envelopes published after the stop, want 0 (%s)", len(envs), describe(envs))
			}
			if n := f.logs.count(watchStartedMsg); n != 0 {
				t.Errorf("%d watchers started after the stop, want 0", n)
			}

			// The stop must not leave the connection unstartable.
			f.command()
			if f.c.getActiveConnection(raceConnID) == nil {
				t.Fatal("start after the stop did not make the connection active")
			}
			if _, running, _ := f.fdb.counts(); running != 1 {
				t.Errorf("status running written %d times after the restart, want 1", running)
			}
			f.requireOneWatcherPublishingOnce("after restart", "restart.json")
			// after-stop.json was there when the restart took its baseline.
			if envs := f.probe.forName("after-stop.json"); len(envs) != 0 {
				t.Errorf("after-stop.json published %d times, want 0 (it predates the restart)", len(envs))
			}
		})
	}
}

// AC 4, follow-on: a start command sent after the stop, while the abandoned
// start is still in flight, is the operator's latest word and must take
// effect. It must not be refused as "already starting", and the abandoned
// start must not add a second watcher when it finally returns.
func TestStartRace_StartAfterStopWhileAbandonedStartInFlight(t *testing.T) {
	f := newRaceFixture(t)
	entered, release := f.holdFirstStart()

	heldDone := make(chan struct{})
	go func() { defer close(heldDone); f.restore() }()
	waitDone(t, entered, "start reaching the start window")

	f.c.handleStopCommand(f.stopCmd())

	restartDone := make(chan struct{})
	go func() { defer close(restartDone); f.command() }()
	waitDone(t, restartDone, "start command after the stop")
	if f.c.getActiveConnection(raceConnID) == nil {
		close(release)
		t.Fatal("start command after the stop was refused while the abandoned start was still in flight")
	}

	close(release)
	waitDone(t, heldDone, "abandoned start after release")

	if f.c.getActiveConnection(raceConnID) == nil {
		t.Fatal("connection not active after the abandoned start returned")
	}
	if _, running, _ := f.fdb.counts(); running != 1 {
		t.Errorf("status running written %d times, want 1 (only the restart)", running)
	}
	f.requireOneWatcherPublishingOnce("after restart", "restart.json")
	if n := f.logs.count(watchStartedMsg); n != 1 {
		t.Errorf("%d watchers started, want 1", n)
	}
}
