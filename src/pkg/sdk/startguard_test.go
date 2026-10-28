package sdk

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for StartGuard (#375, #397): whatever order the boot restore, start
// commands and stop commands arrive in, a connection ends with at most one
// poller, a stop that lands mid-start wins, and a start that gives up leaves
// no claim behind.
//
// guardOwner is a minimal command-driven consumer: an active map under one
// mutex, and a start function that uses the guard exactly as the StartGuard
// doc comment shows. Ordering is forced with channels in AfterReserve, never
// with sleeps; time only bounds a wait so a regression fails instead of
// hanging.

const guardWait = 5 * time.Second

// guardHold parks one start inside AfterReserve until release is closed.
type guardHold struct {
	reached chan struct{}
	release chan struct{}
}

type guardOwner struct {
	mu     sync.Mutex
	active map[string]context.CancelFunc
	starts StartGuard

	holdMu sync.Mutex
	holds  []*guardHold // handed out in order to the next starts that reserve

	inserts   atomic.Int32 // insert funcs Activate ran
	pollers   atomic.Int32 // pollers ever started
	running   atomic.Int32 // pollers running right now
	abandoned atomic.Int32 // starts that found Activate false
	pollWG    sync.WaitGroup
}

func newGuardOwner() *guardOwner {
	o := &guardOwner{active: make(map[string]context.CancelFunc)}
	o.starts.AfterReserve = func(string) {
		o.holdMu.Lock()
		var h *guardHold
		if len(o.holds) > 0 {
			h, o.holds = o.holds[0], o.holds[1:]
		}
		o.holdMu.Unlock()
		if h != nil {
			close(h.reached)
			<-h.release
		}
	}
	return o
}

// hold makes the next start that takes a claim wait in its slow part.
func (o *guardOwner) hold() *guardHold {
	h := &guardHold{reached: make(chan struct{}), release: make(chan struct{})}
	o.holdMu.Lock()
	o.holds = append(o.holds, h)
	o.holdMu.Unlock()
	return h
}

// start is the consumer's startConnection. fail stands for any error in the
// slow part (a lookup, a secret, a client that will not open). It reports
// whether this call started a poller.
func (o *guardOwner) start(ctx context.Context, id, _ string, fail error) bool {
	claim, ok := o.starts.Reserve(&o.mu, id, func() bool { _, ok := o.active[id]; return ok })
	if !ok {
		return false
	}
	defer claim.Release()

	if fail != nil {
		return false
	}

	pctx, cancel := context.WithCancel(ctx)
	if !claim.Activate(func() {
		o.inserts.Add(1)
		o.active[id] = cancel
	}) {
		cancel()
		o.abandoned.Add(1)
		return false
	}
	o.pollers.Add(1)
	o.running.Add(1)
	o.pollWG.Add(1)
	go func() {
		defer o.pollWG.Done()
		defer o.running.Add(-1)
		<-pctx.Done()
	}()
	return true
}

// stop is the consumer's stop-command handler. It reports whether it caught a
// start in flight.
func (o *guardOwner) stop(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if cancel, ok := o.active[id]; ok {
		cancel()
		delete(o.active, id)
	}
	return o.starts.StopLocked(id)
}

// stopAll is the consumer's Stop.
func (o *guardOwner) stopAll() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for id, cancel := range o.active {
		cancel()
		delete(o.active, id)
	}
	o.starts.StopAllLocked()
}

func (o *guardOwner) activeCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.active)
}

// shutdown cancels every poller and waits for them, so -race sees no stray
// goroutines.
func (o *guardOwner) shutdown(t *testing.T) {
	t.Helper()
	o.stopAll()
	done := make(chan struct{})
	go func() { o.pollWG.Wait(); close(done) }()
	waitFor(t, done, "pollers to exit")
}

func (o *guardOwner) assertPollers(t *testing.T, want int) {
	t.Helper()
	if got := o.pollers.Load(); int(got) != want {
		t.Errorf("pollers started = %d, want %d", got, want)
	}
	if got := o.running.Load(); int(got) != want {
		t.Errorf("pollers running = %d, want %d", got, want)
	}
	if got := o.activeCount(); got != want {
		t.Errorf("active entries = %d, want %d", got, want)
	}
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(guardWait):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// goStart runs a start on its own goroutine; the returned channel yields its
// result.
func (o *guardOwner) goStart(id string, fail error) <-chan bool {
	res := make(chan bool, 1)
	go func() { res <- o.start(context.Background(), id, "tenant", fail) }()
	return res
}

func result(t *testing.T, ch <-chan bool, what string) bool {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(guardWait):
		t.Fatalf("timed out waiting for %s", what)
		return false
	}
}

var errSlowPart = errors.New("lookup failed")

func TestStartGuard_Race(t *testing.T) {
	const id = "conn-1"

	cases := []struct {
		name string
		run  func(t *testing.T, o *guardOwner)
	}{
		{
			// The boot restore and a start command for the same connection,
			// both through the real RestoreRunning, released together.
			name: "restore plus start command, concurrent",
			run: func(t *testing.T, o *guardOwner) {
				db := sql.OpenDB(restoreConnector{rows: [][2]string{{id, "tenant"}}})
				defer db.Close()
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				go1 := make(chan struct{})
				var wg sync.WaitGroup
				var restored atomic.Int32
				var commanded atomic.Bool
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-go1
					RestoreRunning(context.Background(), db, logger, "http", func(ctx context.Context, cid, tid string) {
						if o.start(ctx, cid, tid, nil) {
							restored.Add(1)
						}
					})
				}()
				go func() {
					defer wg.Done()
					<-go1
					commanded.Store(o.start(context.Background(), id, "tenant", nil))
				}()
				close(go1)
				wg.Wait()
				if n := restored.Load(); (n == 1) == commanded.Load() {
					t.Errorf("restore started %d, command started %v; want exactly one of them", n, commanded.Load())
				}
				o.assertPollers(t, 1)
			},
		},
		{
			name: "two start commands, concurrent",
			run: func(t *testing.T, o *guardOwner) {
				go1 := make(chan struct{})
				var wg sync.WaitGroup
				var won atomic.Int32
				for range 2 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-go1
						if o.start(context.Background(), id, "tenant", nil) {
							won.Add(1)
						}
					}()
				}
				close(go1)
				wg.Wait()
				if n := won.Load(); n != 1 {
					t.Errorf("starts that reported a poller = %d, want 1", n)
				}
				o.assertPollers(t, 1)
			},
		},
		{
			// Start A held in its slow part; start B arrives and must be
			// refused without doing anything.
			name: "second start while one is in flight",
			run: func(t *testing.T, o *guardOwner) {
				ha := o.hold()
				a := o.goStart(id, nil)
				waitFor(t, ha.reached, "start A to reserve")

				if o.start(context.Background(), id, "tenant", nil) {
					t.Error("start B started a poller while A was in flight")
				}
				if n := o.pollers.Load(); n != 0 {
					t.Errorf("pollers before A finished = %d, want 0", n)
				}
				close(ha.release)
				if !result(t, a, "start A") {
					t.Error("start A did not start a poller")
				}
				o.assertPollers(t, 1)

				// Once active, a further start is refused too.
				if o.start(context.Background(), id, "tenant", nil) {
					t.Error("start after activation started a second poller")
				}
				o.assertPollers(t, 1)
			},
		},
		{
			name: "stop mid-start wins",
			run: func(t *testing.T, o *guardOwner) {
				ha := o.hold()
				a := o.goStart(id, nil)
				waitFor(t, ha.reached, "start A to reserve")

				if !o.stop(id) {
					t.Error("StopLocked reported no start in flight")
				}
				close(ha.release)
				if result(t, a, "start A") {
					t.Error("start A started a poller after a stop")
				}
				if n := o.inserts.Load(); n != 0 {
					t.Errorf("insert ran %d times after the stop, want 0", n)
				}
				if n := o.abandoned.Load(); n != 1 {
					t.Errorf("abandoned starts = %d, want 1", n)
				}
				o.assertPollers(t, 0)

				// No claim is left: a fresh start goes through.
				if !o.start(context.Background(), id, "tenant", nil) {
					t.Fatal("start after the stop was refused")
				}
				o.assertPollers(t, 1)
			},
		},
		{
			// A start arriving after the stop, while the abandoned one is
			// still working, is not refused.
			name: "start after stop while the abandoned start still works",
			run: func(t *testing.T, o *guardOwner) {
				ha := o.hold()
				a := o.goStart(id, nil)
				waitFor(t, ha.reached, "start A to reserve")
				o.stop(id)

				if !o.start(context.Background(), id, "tenant", nil) {
					t.Fatal("start B after the stop was refused")
				}
				close(ha.release)
				if result(t, a, "start A") {
					t.Error("abandoned start A started a poller")
				}
				o.assertPollers(t, 1)
			},
		},
		{
			name: "consumer Stop mid-start wins for every connection",
			run: func(t *testing.T, o *guardOwner) {
				ha, hb := o.hold(), o.hold()
				a := o.goStart("conn-a", nil)
				waitFor(t, ha.reached, "start A to reserve")
				b := o.goStart("conn-b", nil)
				waitFor(t, hb.reached, "start B to reserve")

				o.stopAll()
				// Stop dropped the claims: a new start is not refused while
				// the abandoned ones are still working.
				if !o.start(context.Background(), "conn-a", "tenant", nil) {
					t.Fatal("start of conn-a after Stop was refused while the old one was in flight")
				}
				close(ha.release)
				close(hb.release)
				if result(t, a, "start A") || result(t, b, "start B") {
					t.Error("a start in flight at Stop went through")
				}
				if n := o.abandoned.Load(); n != 2 {
					t.Errorf("abandoned starts = %d, want 2", n)
				}
				if n := o.inserts.Load(); n != 1 {
					t.Errorf("insert ran %d times, want 1 (the start after Stop)", n)
				}
				o.assertPollers(t, 1)

				if !o.start(context.Background(), "conn-b", "tenant", nil) {
					t.Fatal("start of conn-b after Stop was refused")
				}
				o.assertPollers(t, 2)
			},
		},
		{
			name: "failed start leaves no claim",
			run: func(t *testing.T, o *guardOwner) {
				if o.start(context.Background(), id, "tenant", errSlowPart) {
					t.Fatal("failing start reported a poller")
				}
				o.assertPollers(t, 0)
				if !o.start(context.Background(), id, "tenant", nil) {
					t.Fatal("start after a failed start was refused")
				}
				o.assertPollers(t, 1)
			},
		},
		{
			// #375's second review: A is held; a stop takes A's claim; B claims
			// and is held; A gives up and its deferred Release runs; C arrives
			// while B is still in flight and must be refused. A Release that
			// drops the claim without checking it is its own frees B's claim
			// and lets C start a second poller.
			name: "three starts, abandoned start fails",
			run:  func(t *testing.T, o *guardOwner) { threeStarts(t, o, id, errSlowPart) },
		},
		{
			// Same, but A gets as far as Activate (which refuses) before its
			// deferred Release runs.
			name: "three starts, abandoned start reaches Activate",
			run:  func(t *testing.T, o *guardOwner) { threeStarts(t, o, id, nil) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iterations := 1
			if strings.Contains(tc.name, "concurrent") {
				iterations = 200
			}
			for i := 0; i < iterations; i++ {
				o := newGuardOwner()
				tc.run(t, o)
				o.shutdown(t)
				if t.Failed() {
					t.Fatalf("failed on iteration %d", i)
				}
			}
		})
	}
}

func threeStarts(t *testing.T, o *guardOwner, id string, aFails error) {
	t.Helper()
	ha, hb := o.hold(), o.hold()
	a := o.goStart(id, aFails)
	waitFor(t, ha.reached, "start A to reserve")

	if !o.stop(id) {
		t.Error("stop did not find A in flight")
	}

	b := o.goStart(id, nil)
	waitFor(t, hb.reached, "start B to reserve")

	close(ha.release)
	if result(t, a, "start A") {
		t.Error("abandoned start A started a poller")
	}

	// B is still held. C must be refused; if A's Release freed B's claim, C
	// starts here (no hold left for it) and B later starts a second poller.
	if o.start(context.Background(), id, "tenant", nil) {
		t.Error("start C went through while B was in flight")
	}

	close(hb.release)
	if !result(t, b, "start B") {
		t.Error("start B did not start a poller")
	}
	o.assertPollers(t, 1)
}

// Direct checks of the contract's edges.
func TestStartGuard_Edges(t *testing.T) {
	t.Run("Reserve refused when already active", func(t *testing.T) {
		var mu sync.Mutex
		var g StartGuard
		c, ok := g.Reserve(&mu, "x", func() bool { return true })
		if ok || c != nil {
			t.Fatalf("Reserve on an active connection = (%v, %v), want (nil, false)", c, ok)
		}
		// Nothing was recorded: a Reserve that is not active goes through.
		c, ok = g.Reserve(&mu, "x", func() bool { return false })
		if !ok {
			t.Fatal("Reserve after a refused Reserve was refused")
		}
		c.Release()
	})

	t.Run("AfterReserve runs only for a granted claim, with the lock free", func(t *testing.T) {
		var mu sync.Mutex
		var g StartGuard
		var calls []string
		g.AfterReserve = func(id string) {
			if !mu.TryLock() {
				t.Error("AfterReserve ran with the owner's lock held")
			} else {
				mu.Unlock()
			}
			calls = append(calls, id)
		}
		c, ok := g.Reserve(&mu, "x", func() bool { return false })
		if !ok {
			t.Fatal("first Reserve refused")
		}
		if _, ok := g.Reserve(&mu, "x", func() bool { return false }); ok {
			t.Fatal("second Reserve while the first holds the claim was granted")
		}
		if _, ok := g.Reserve(&mu, "y", func() bool { return true }); ok {
			t.Fatal("Reserve on an active connection was granted")
		}
		c.Release()
		if len(calls) != 1 || calls[0] != "x" {
			t.Errorf("AfterReserve calls = %v, want [x]", calls)
		}
	})

	t.Run("Release on a nil claim is safe", func(t *testing.T) {
		var c *StartClaim
		c.Release()
	})

	t.Run("Release after Activate is a no-op", func(t *testing.T) {
		var mu sync.Mutex
		var g StartGuard
		active := map[string]bool{}
		isActive := func() bool { return active["x"] }

		a, ok := g.Reserve(&mu, "x", isActive)
		if !ok {
			t.Fatal("Reserve A refused")
		}
		if !a.Activate(func() { active["x"] = true }) {
			t.Fatal("Activate A refused without a stop")
		}
		// The connection ends on its own (a one-shot poller removing itself)
		// and B claims it; A's late Release must leave B's claim alone.
		mu.Lock()
		delete(active, "x")
		mu.Unlock()
		b, ok := g.Reserve(&mu, "x", isActive)
		if !ok {
			t.Fatal("Reserve B refused")
		}
		a.Release()
		if _, ok := g.Reserve(&mu, "x", isActive); ok {
			t.Fatal("Release after Activate freed a newer start's claim")
		}
		b.Release()
	})

	t.Run("Activate after StopLocked does not insert", func(t *testing.T) {
		var mu sync.Mutex
		var g StartGuard
		c, ok := g.Reserve(&mu, "x", func() bool { return false })
		if !ok {
			t.Fatal("Reserve refused")
		}
		mu.Lock()
		if !g.StopLocked("x") {
			t.Error("StopLocked did not find the start")
		}
		if g.StopLocked("x") {
			t.Error("second StopLocked found a start")
		}
		mu.Unlock()
		inserted := false
		if c.Activate(func() { inserted = true }) || inserted {
			t.Errorf("Activate after a stop ran insert (%v)", inserted)
		}
		c.Release()
	})

	t.Run("StopLocked with nothing in flight", func(t *testing.T) {
		var mu sync.Mutex
		var g StartGuard
		mu.Lock()
		defer mu.Unlock()
		if g.StopLocked("x") {
			t.Error("StopLocked on a zero guard reported a start")
		}
		g.StopAllLocked()
	})
}

// --- a fake database that answers RestoreRunning's one query -----------------

type restoreConnector struct{ rows [][2]string }

func (c restoreConnector) Connect(context.Context) (driver.Conn, error) { return restoreConn(c), nil }
func (c restoreConnector) Driver() driver.Driver                        { return restoreDriver{} }

type restoreDriver struct{}

func (restoreDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("restoreDriver: use sql.OpenDB")
}

type restoreConn struct{ rows [][2]string }

func (restoreConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("restoreConn: no prepared statements")
}
func (restoreConn) Close() error              { return nil }
func (restoreConn) Begin() (driver.Tx, error) { return nil, errors.New("restoreConn: no transactions") }
func (c restoreConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(q, "FROM connections") {
		return nil, errors.New("restoreConn: unexpected query")
	}
	r := &restoreRows{}
	for _, p := range c.rows {
		r.data = append(r.data, []driver.Value{p[0], p[1]})
	}
	return r, nil
}

type restoreRows struct {
	data [][]driver.Value
	i    int
}

func (r *restoreRows) Columns() []string { return []string{"id", "tenant_id"} }
func (r *restoreRows) Close() error      { return nil }
func (r *restoreRows) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}
