package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// Tests for a poller's own ending status write (the one-shot end in runPoller) going
// through the StatusWrite ticket dropActive takes (#423, backlog item 91). A
// poller A that has dropped its entry and not yet written its final status
// must not overwrite the "running" of a start B that activated after the drop.
//
// They reuse the sqlmock fixture (start_race_test.go), entry/expectStatus
// (poller_entry_test.go) and the catch-all helpers (stale_status_test.go).
// Ordering is forced with the guard's BeforeStatusWrite and AfterActivate
// hooks and with a hold inside the database call; waits are bounded by srWait.

// geEnding is one way this consumer's poller ends on its own, with the
// status it writes then. start registers the expectations the ending makes
// before its status write and returns the ending itself.
type geEnding struct {
	name   string
	status string
	start  func(t *testing.T, f *srFixture) func(ctx context.Context, a *activeConn)
}

// geWriteHook holds the first status write that reaches BeforeStatusWrite
// once armed; later writes pass.
type geWriteHook struct {
	armed   atomic.Bool
	calls   atomic.Int32
	reached chan struct{}
	rel     chan struct{}
	once    sync.Once
}

func (h *geWriteHook) release() { h.once.Do(func() { close(h.rel) }) }

func geInstallWriteHook(t *testing.T, f *srFixture) *geWriteHook {
	h := &geWriteHook{reached: make(chan struct{}), rel: make(chan struct{})}
	t.Cleanup(h.release)
	f.c.starts.BeforeStatusWrite = func(string) {
		if !h.armed.Load() {
			return
		}
		if h.calls.Add(1) == 1 {
			close(h.reached)
			select {
			case <-h.rel:
			case <-time.After(srWait):
			}
		}
	}
	return h
}

// geExecHold holds the first Exec whose first argument is status, inside the
// database call, before sqlmock sees it: a status write in flight.
type geExecHold struct {
	status string
	armed  atomic.Bool
	in     chan struct{}
	rel    chan struct{}
	once   sync.Once
}

func (h *geExecHold) release() { h.once.Do(func() { close(h.rel) }) }

func (h *geExecHold) exec(args []driver.NamedValue) {
	if len(args) == 0 || args[0].Value != h.status || !h.armed.CompareAndSwap(true, false) {
		return
	}
	close(h.in)
	select {
	case <-h.rel:
	case <-time.After(srWait):
	}
}

type geConnector struct {
	drv driver.Driver
	dsn string
	h   *geExecHold
}

func (c geConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return geConn{Conn: conn, h: c.h}, nil
}

func (c geConnector) Driver() driver.Driver { return c.drv }

type geConn struct {
	driver.Conn
	h *geExecHold
}

func (c geConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.h.exec(args)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}

func (c geConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

var geDSN atomic.Int64

// geSwapHeldDB gives the consumer a fresh sqlmock database whose Execs pass
// through a geExecHold for status, and makes it the fixture's mock.
func geSwapHeldDB(t *testing.T, f *srFixture, status string) *geExecHold {
	t.Helper()
	dsn := fmt.Sprintf("guard-end-%d", geDSN.Add(1))
	mdb, mock, err := sqlmock.NewWithDSN(dsn)
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	h := &geExecHold{status: status, in: make(chan struct{}), rel: make(chan struct{})}
	t.Cleanup(h.release)
	db := sql.OpenDB(geConnector{drv: mdb.Driver(), dsn: dsn, h: h})
	t.Cleanup(func() { _ = db.Close(); _ = mdb.Close() })
	f.c.db, f.mock = db, mock
	return h
}

// The late write is held before it takes its lane (BeforeStatusWrite), after
// A dropped its entry. Start B activates and writes "running" meanwhile; A's
// write, released after, must not reach the database at all.
func TestGuardEnd_LateWriteHeldBeforeLane(t *testing.T) {
	for _, end := range geEndings() {
		t.Run(end.name, func(t *testing.T) {
			f := geNewFixture(t)
			hook := geInstallWriteHook(t, f)
			a := geStartA(t, f)
			runEnd := end.start(t, f)

			hook.armed.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			endDone := make(chan struct{})
			go func() { defer close(endDone); runEnd(ctx, a) }()
			select {
			case <-hook.reached:
			case <-endDone:
				t.Fatalf("A's ending returned without its %q write going through the guard's status order (BeforeStatusWrite never ran)", end.status)
			case <-time.After(srWait):
				t.Fatalf("timed out waiting for A's %q write", end.status)
			}
			if got := f.entry(); got != nil {
				t.Fatalf("entry while A's write is held = %p, want none (A dropped it)", got)
			}

			// No stop needed: A's entry is already gone.
			f.expectLookup(nil)
			geExpectBTail(f)
			f.c.startConnection(context.Background(), srConn, srTenant)
			b := f.entry()
			if b == nil || b == a {
				t.Fatalf("start B entry = %p, want a new entry (A was %p)", b, a)
			}
			geAfterB(t, f)
			if n := hook.calls.Load(); n < 2 {
				t.Errorf("BeforeStatusWrite calls = %d, want B's running write to pass it too", n)
			}

			// The last write is B's "running"; anything A writes now lands here.
			f.ssAllMet(t, `after start B wrote "running"`)
			f.ssCatchAnyExec()

			hook.release()
			srAwait(t, endDone, "A's ending after its write was released")
			f.ssNoLateExec(t, "A's held "+end.status+" write")
			if got := f.entry(); got != b {
				t.Errorf("entry after A's ending = %p, want B's %p", got, b)
			}
		})
	}
}

// The late write is already in flight in the database (past its lane) when
// start B activates. B's "running" must still be the last status write, so
// B's write waits for A's and lands after it.
func TestGuardEnd_LateWriteInFlightKeepsNewerRunning(t *testing.T) {
	for _, end := range geEndings() {
		t.Run(end.name, func(t *testing.T) {
			f := geNewFixture(t)
			held := geSwapHeldDB(t, f, end.status)
			a := geStartA(t, f)
			runEnd := end.start(t, f)
			f.expectLookup(nil)        // start B
			f.expectStatus(end.status) // A's write, released once B is active
			geExpectBTail(f)           // B's "running", last

			held.armed.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			endDone := make(chan struct{})
			go func() { defer close(endDone); runEnd(ctx, a) }()
			select {
			case <-held.in:
			case <-endDone:
				t.Fatalf("A's ending made no %q write", end.status)
			case <-time.After(srWait):
				t.Fatalf("timed out waiting for A's %q write", end.status)
			}

			// B's entry, taken right after its Activate (before its "running"
			// write, which may wait for A's lane).
			bActive := make(chan *activeConn, 1)
			f.c.starts.AfterActivate = func(string) {
				select {
				case bActive <- f.entry():
				default:
				}
			}
			bDone := make(chan struct{})
			go func() { defer close(bDone); f.c.startConnection(context.Background(), srConn, srTenant) }()
			var b *activeConn
			select {
			case b = <-bActive:
			case <-bDone:
				t.Fatal("start B returned without activating")
			case <-time.After(srWait):
				t.Fatal("timed out waiting for start B to activate")
			}
			if b == nil || b == a {
				t.Fatalf("start B entry = %p, want a new entry (A was %p)", b, a)
			}

			held.release()
			srAwait(t, endDone, "A's ending after its write was released")
			srAwait(t, bDone, "start B")
			geSettleB(t, f, b)

			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf(`B's "running" is not the last status write after A's in-flight %q: %v`, end.status, err)
			}
			if got := f.entry(); got != b {
				t.Errorf("entry after A's ending = %p, want B's %p", got, b)
			}
		})
	}
}

// Sanity: a poller that ends with no newer start still writes its final
// status and drops its own entry.
func TestGuardEnd_OwnEndStillWrites(t *testing.T) {
	for _, end := range geEndings() {
		t.Run(end.name, func(t *testing.T) {
			f := geNewFixture(t)
			a := geStartA(t, f)
			f.ssAllMet(t, `after start A wrote "running"`)
			runEnd := end.start(t, f)
			f.expectStatus(end.status)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runEnd(ctx, a)

			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("A's own ending did not write %q: %v", end.status, err)
			}
			if got := f.entry(); got != nil {
				t.Errorf("entry after A's own ending = %p, want none", got)
			}
		})
	}
}

func geNewFixture(t *testing.T) *srFixture { return newSRFixture(t) }

// geStartA starts A and returns its entry. A's own poller from the start
// runs until cancelled; once the entry is gone, Stop in the fixture cleanup
// no longer reaches it.
func geStartA(t *testing.T, f *srFixture) *activeConn {
	t.Helper()
	f.expectLookup(nil)
	f.expectRunning()
	f.c.startConnection(context.Background(), srConn, srTenant)
	a := f.entry()
	if a == nil {
		t.Fatal("start A left no entry")
	}
	t.Cleanup(a.cancel)
	return a
}

func geExpectBTail(f *srFixture) { f.expectRunning() }

func geAfterB(*testing.T, *srFixture) {}

func geSettleB(*testing.T, *srFixture, *activeConn) {}

func geEndings() []geEnding {
	return []geEnding{{
		name:   "one-shot poll done",
		status: "stopped",
		start: func(_ *testing.T, f *srFixture) func(context.Context, *activeConn) {
			return func(ctx context.Context, a *activeConn) { f.c.runPoller(ctx, a, srConn, srTenant, ssOneShot()) }
		},
	}}
}
