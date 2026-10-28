package sdk

import (
	"context"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for the two StartGuard additions (#423, #424), written against the
// acceptance criteria in plans/issue-423-424-start-guard.md:
//
//   - #423: a start's "running" write and a stop's "stopped" write end in the
//     order of the state changes that caused them (StatusWrite), whichever
//     reaches the database first.
//   - #424: a start refused while another is in flight is run again when that
//     one gives up (ReserveOrRetry), and only then.
//
// orderOwner is a command-driven consumer that writes status, using the guard
// the way the StartGuard doc comment shows. Its "database" is a map that
// keeps the last status written per connection. Ordering is forced with
// channels in AfterReserve and AfterActivate, never with sleeps; waits are
// bounded (guardWait) so a regression fails instead of hanging.

type orderOwner struct {
	mu     sync.Mutex
	active map[string]context.CancelFunc
	starts StartGuard

	holdMu sync.Mutex
	holds  map[string][]*guardHold // per connection, handed out to the next starts that reserve

	// onActivate, when set, runs on the start's goroutine after Activate
	// returned and before the start's "running" write (the #423 window).
	onActivate func(id string)
	// onRunningWrite, when set, runs inside the "running" write, as if the
	// database call were in flight.
	onRunningWrite func(id string)

	statusMu sync.Mutex
	status   map[string]string   // last status written per connection
	log      map[string][]string // every status write per connection, in order

	pollMu  sync.Mutex
	polls   map[string][]context.Context // every poller started, by connection
	pollWG  sync.WaitGroup
	retries sync.Map // connection ID -> *atomic.Int32: retries run
}

func newOrderOwner() *orderOwner {
	o := &orderOwner{
		active: make(map[string]context.CancelFunc),
		holds:  make(map[string][]*guardHold),
		status: make(map[string]string),
		log:    make(map[string][]string),
		polls:  make(map[string][]context.Context),
	}
	o.starts.AfterReserve = func(id string) {
		o.holdMu.Lock()
		var h *guardHold
		if q := o.holds[id]; len(q) > 0 {
			h, o.holds[id] = q[0], q[1:]
		}
		o.holdMu.Unlock()
		if h != nil {
			close(h.reached)
			<-h.release
		}
	}
	o.starts.AfterActivate = func(id string) {
		if o.onActivate != nil {
			o.onActivate(id)
		}
	}
	return o
}

// hold makes the next start of id that takes a claim wait in its slow part.
func (o *orderOwner) hold(id string) *guardHold {
	h := &guardHold{reached: make(chan struct{}), release: make(chan struct{})}
	o.holdMu.Lock()
	o.holds[id] = append(o.holds[id], h)
	o.holdMu.Unlock()
	return h
}

func (o *orderOwner) retryCounter(id string) *atomic.Int32 {
	v, _ := o.retries.LoadOrStore(id, new(atomic.Int32))
	return v.(*atomic.Int32)
}

// retriesRun is how many refused starts of id were run again.
func (o *orderOwner) retriesRun(id string) int { return int(o.retryCounter(id).Load()) }

// start is the consumer's startConnection: fail stands for any error in the
// slow part. A refused start leaves itself, the same start again, as retry.
func (o *orderOwner) start(ctx context.Context, id string, fail error) bool {
	claim, ok := o.starts.ReserveOrRetry(&o.mu, id,
		func() bool { _, ok := o.active[id]; return ok },
		func() {
			o.retryCounter(id).Add(1)
			o.start(ctx, id, fail)
		})
	if !ok {
		return false
	}
	defer claim.Release()

	if fail != nil {
		return false
	}

	pctx, cancel := context.WithCancel(ctx)
	if !claim.Activate(func() { o.active[id] = cancel }) {
		cancel()
		return false
	}
	claim.RunningStatus().Write(func() {
		if o.onRunningWrite != nil {
			o.onRunningWrite(id)
		}
		o.record(id, "running")
	})
	o.pollMu.Lock()
	o.polls[id] = append(o.polls[id], pctx)
	o.pollMu.Unlock()
	o.pollWG.Add(1)
	go func() {
		defer o.pollWG.Done()
		<-pctx.Done()
	}()
	return true
}

func (o *orderOwner) record(id, status string) {
	o.statusMu.Lock()
	defer o.statusMu.Unlock()
	o.status[id] = status
	o.log[id] = append(o.log[id], status)
}

// stopLocked is the stop handler's critical section. It returns whether the
// connection was active and the stop's place in the status order.
func (o *orderOwner) stopLocked(id string) (bool, StatusWrite) {
	o.mu.Lock()
	defer o.mu.Unlock()
	cancel, wasActive := o.active[id]
	if wasActive {
		cancel()
		delete(o.active, id)
	}
	o.starts.StopLocked(id)
	return wasActive, o.starts.StopStatusLocked(id)
}

// writeStopped is the stop handler's status write after unlocking.
func (o *orderOwner) writeStopped(id string, w StatusWrite) bool {
	return w.Write(func() { o.record(id, "stopped") })
}

// stop is the whole stop handler. Like the consumers, it writes "stopped"
// only when the connection was active.
func (o *orderOwner) stop(id string) {
	if wasActive, w := o.stopLocked(id); wasActive {
		o.writeStopped(id, w)
	}
}

// stopAll is the consumer's Stop.
func (o *orderOwner) stopAll() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for id, cancel := range o.active {
		cancel()
		delete(o.active, id)
	}
	o.starts.StopAllLocked()
}

func (o *orderOwner) goStart(id string, fail error) <-chan bool {
	res := make(chan bool, 1)
	go func() { res <- o.start(context.Background(), id, fail) }()
	return res
}

func (o *orderOwner) statusOf(id string) string {
	o.statusMu.Lock()
	defer o.statusMu.Unlock()
	return o.status[id]
}

func (o *orderOwner) setStatus(id, status string) {
	o.statusMu.Lock()
	defer o.statusMu.Unlock()
	o.status[id] = status
}

func (o *orderOwner) isActive(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.active[id]
	return ok
}

// pollers reports how many pollers were ever started for id and how many are
// still running (their context not cancelled).
func (o *orderOwner) pollers(id string) (started, live int) {
	o.pollMu.Lock()
	defer o.pollMu.Unlock()
	for _, ctx := range o.polls[id] {
		started++
		if ctx.Err() == nil {
			live++
		}
	}
	return started, live
}

// claimsLeft is the number of claims the guard still holds (white box).
func (o *orderOwner) claimsLeft() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.starts.starting)
}

func (o *orderOwner) cleanup(t *testing.T) {
	t.Helper()
	o.stopAll()
	done := make(chan struct{})
	go func() { o.pollWG.Wait(); close(done) }()
	waitFor(t, done, "pollers to exit")
}

// requireState checks the end state of one connection: its status, how many
// pollers were ever started and how many still run, and whether it is in the
// active map.
func (o *orderOwner) requireState(t *testing.T, id, status string, started, live int) {
	t.Helper()
	if got := o.statusOf(id); got != status {
		o.statusMu.Lock()
		log := append([]string(nil), o.log[id]...)
		o.statusMu.Unlock()
		t.Errorf("%s: status = %q, want %q (writes in order: %v)", id, got, status, log)
	}
	s, l := o.pollers(id)
	if s != started {
		t.Errorf("%s: pollers started = %d, want %d", id, s, started)
	}
	if l != live {
		t.Errorf("%s: pollers running = %d, want %d", id, l, live)
	}
	if got, want := o.isActive(id), live == 1; got != want {
		t.Errorf("%s: active = %v, want %v", id, got, want)
	}
}

// once wraps f so it runs only for id and only the first time.
func once(id string, f func()) func(string) {
	var done atomic.Bool
	return func(got string) {
		if got == id && done.CompareAndSwap(false, true) {
			f()
		}
	}
}

// --- #423: status order ------------------------------------------------------

// AC 1: a stop that lands after Activate and before the start's "running"
// write ends "stopped", whichever of the two writes is attempted first. And
// the plain order, "running" written then a stop, ends "stopped" too.
func TestStartGuardOrder_StopAfterActivate(t *testing.T) {
	const id = "conn-1"

	cases := []struct {
		name string
		run  func(t *testing.T, o *orderOwner)
	}{
		{
			// The stop handler runs in full inside the window, so its
			// "stopped" reaches the database before the start's "running".
			name: "stopped written first",
			run: func(t *testing.T, o *orderOwner) {
				o.onActivate = once(id, func() { o.stop(id) })
				result(t, o.goStart(id, nil), "start")
			},
		},
		{
			// The stop's critical section runs inside the window but its
			// write is held back until the start has written "running".
			name: "running written first, then stopped",
			run: func(t *testing.T, o *orderOwner) {
				var w StatusWrite
				var wasActive bool
				o.onActivate = once(id, func() { wasActive, w = o.stopLocked(id) })
				result(t, o.goStart(id, nil), "start")
				if !wasActive {
					t.Fatal("stop did not find the connection active")
				}
				if !o.writeStopped(id, w) {
					t.Error("stop's write skipped although it is the latest change")
				}
			},
		},
		{
			// The start's "running" write is in flight (inside the database
			// call) when the stop arrives; the stop's write has to wait for
			// it and lands after.
			name: "stop arrives while running write is in flight",
			run: func(t *testing.T, o *orderOwner) {
				stopDone := make(chan struct{})
				o.onRunningWrite = once(id, func() {
					wasActive, w := o.stopLocked(id)
					go func() {
						defer close(stopDone)
						if wasActive {
							o.writeStopped(id, w)
						}
					}()
				})
				result(t, o.goStart(id, nil), "start")
				waitFor(t, stopDone, "stop's status write")
			},
		},
		{
			name: "running written, then a stop",
			run: func(t *testing.T, o *orderOwner) {
				if !result(t, o.goStart(id, nil), "start") {
					t.Fatal("start did not start")
				}
				if got := o.statusOf(id); got != "running" {
					t.Fatalf("status after start = %q, want running", got)
				}
				o.stop(id)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrderOwner()
			o.setStatus(id, "stopped")
			tc.run(t, o)
			o.requireState(t, id, "stopped", 1, 0)
			if n := o.claimsLeft(); n != 0 {
				t.Errorf("claims left = %d, want 0", n)
			}
			o.cleanup(t)
		})
	}
}

// AC 2: a stop followed by a new start ends "running", even when the stop's
// "stopped" write is attempted after the new start's "running" write.
func TestStartGuardOrder_StopThenNewStart(t *testing.T) {
	const id = "conn-1"

	cases := []struct {
		name string
		run  func(t *testing.T, o *orderOwner)
	}{
		{
			name: "stop's write before the new start's",
			run: func(t *testing.T, o *orderOwner) {
				result(t, o.goStart(id, nil), "first start")
				o.stop(id)
				result(t, o.goStart(id, nil), "second start")
			},
		},
		{
			name: "stop's write after the new start's",
			run: func(t *testing.T, o *orderOwner) {
				result(t, o.goStart(id, nil), "first start")
				wasActive, w := o.stopLocked(id)
				if !wasActive {
					t.Fatal("stop did not find the connection active")
				}
				if !result(t, o.goStart(id, nil), "second start") {
					t.Fatal("second start did not start")
				}
				if o.writeStopped(id, w) {
					t.Error("stale stop's write ran after the newer start's")
				}
			},
		},
		{
			// Both the stop and the new start land in the first start's
			// #423 window; the first start's "running" and the stop's
			// "stopped" are then both older than the new start's "running".
			name: "stop and new start inside the first start's window",
			run: func(t *testing.T, o *orderOwner) {
				var w StatusWrite
				var second bool
				o.onActivate = once(id, func() {
					_, w = o.stopLocked(id)
					second = o.start(context.Background(), id, nil)
				})
				result(t, o.goStart(id, nil), "first start")
				if !second {
					t.Error("second start did not start")
				}
				if o.writeStopped(id, w) {
					t.Error("stale stop's write ran after the newer start's")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrderOwner()
			o.setStatus(id, "stopped")
			tc.run(t, o)
			o.requireState(t, id, "running", 2, 1)
			o.cleanup(t)
		})
	}
}

// AC 5: the consumer shutting down (StopAllLocked) right after Activate does
// not suppress the start's "running" write, so the next boot restores the
// connection. A shutdown before Activate writes nothing either way.
func TestStartGuardOrder_ShutdownKeepsRunning(t *testing.T) {
	const id = "conn-1"

	t.Run("shutdown after Activate", func(t *testing.T) {
		o := newOrderOwner()
		o.setStatus(id, "stopped")
		o.onActivate = once(id, o.stopAll)
		if !result(t, o.goStart(id, nil), "start") {
			t.Fatal("start reported no poller although Activate succeeded")
		}
		o.requireState(t, id, "running", 1, 0)
		o.cleanup(t)
	})

	t.Run("shutdown mid-start leaves the restored status alone", func(t *testing.T) {
		o := newOrderOwner()
		o.setStatus(id, "running") // what the boot restore read
		h := o.hold(id)
		res := o.goStart(id, nil)
		waitFor(t, h.reached, "start to reserve")
		o.stopAll()
		close(h.release)
		if result(t, res, "start") {
			t.Error("start went ahead after shutdown")
		}
		o.requireState(t, id, "running", 0, 0)
		o.cleanup(t)
	})
}

// --- #424: a refused start is not lost ----------------------------------------

// AC 3 and 4: a start refused because another holds the claim runs again when
// that one gives up, and only then.
func TestStartGuardOrder_RefusedStartRetry(t *testing.T) {
	const id = "conn-1"

	cases := []struct {
		name string
		run  func(t *testing.T, o *orderOwner)
		// end state
		retries, started, live int
		status                 string
	}{
		{
			name: "first start fails: refused start runs",
			run: func(t *testing.T, o *orderOwner) {
				h := o.hold(id)
				first := o.goStart(id, errSlowPart)
				waitFor(t, h.reached, "first start to reserve")
				if o.start(context.Background(), id, nil) {
					t.Fatal("second start not refused while the first is in flight")
				}
				close(h.release)
				result(t, first, "first start and the retry it runs")
			},
			retries: 1, started: 1, live: 1, status: "running",
		},
		{
			// Of several refused starts the latest is kept: here the
			// earlier would fail again, the latest succeeds.
			name: "several refused: latest runs",
			run: func(t *testing.T, o *orderOwner) {
				h := o.hold(id)
				first := o.goStart(id, errSlowPart)
				waitFor(t, h.reached, "first start to reserve")
				o.start(context.Background(), id, errSlowPart)
				o.start(context.Background(), id, nil)
				close(h.release)
				result(t, first, "first start and the retry it runs")
			},
			retries: 1, started: 1, live: 1, status: "running",
		},
		{
			name: "first start activates: no retry, no double start",
			run: func(t *testing.T, o *orderOwner) {
				h := o.hold(id)
				first := o.goStart(id, nil)
				waitFor(t, h.reached, "first start to reserve")
				o.start(context.Background(), id, nil)
				close(h.release)
				if !result(t, first, "first start") {
					t.Error("first start did not start")
				}
			},
			retries: 0, started: 1, live: 1, status: "running",
		},
		{
			name: "stop after the refusal, first start fails: no retry",
			run: func(t *testing.T, o *orderOwner) {
				h := o.hold(id)
				first := o.goStart(id, errSlowPart)
				waitFor(t, h.reached, "first start to reserve")
				o.start(context.Background(), id, nil)
				o.stop(id)
				close(h.release)
				result(t, first, "first start")
			},
			retries: 0, started: 0, live: 0, status: "stopped",
		},
		{
			name: "stop after the refusal, first start reaches Activate: no retry",
			run: func(t *testing.T, o *orderOwner) {
				h := o.hold(id)
				first := o.goStart(id, nil)
				waitFor(t, h.reached, "first start to reserve")
				o.start(context.Background(), id, nil)
				o.stop(id)
				close(h.release)
				if result(t, first, "first start") {
					t.Error("first start went ahead after the stop")
				}
			},
			retries: 0, started: 0, live: 0, status: "stopped",
		},
		{
			name: "shutdown after the refusal: no retry",
			run: func(t *testing.T, o *orderOwner) {
				h := o.hold(id)
				first := o.goStart(id, errSlowPart)
				waitFor(t, h.reached, "first start to reserve")
				o.start(context.Background(), id, nil)
				o.stopAll()
				close(h.release)
				result(t, first, "first start")
			},
			retries: 0, started: 0, live: 0, status: "stopped",
		},
		{
			name: "refused because active: nothing kept",
			run: func(t *testing.T, o *orderOwner) {
				result(t, o.goStart(id, nil), "first start")
				if o.start(context.Background(), id, nil) {
					t.Fatal("start not refused while the connection is active")
				}
				o.stop(id)
				// A later start that gives up must not find the refused
				// start's retry anywhere.
				result(t, o.goStart(id, errSlowPart), "failing start")
			},
			retries: 0, started: 1, live: 0, status: "stopped",
		},
		{
			// The retry is itself a start: if it fails too and another start
			// was refused meanwhile, that one runs next.
			name: "retry fails, a start refused by the retry runs",
			run: func(t *testing.T, o *orderOwner) {
				h1 := o.hold(id)
				first := o.goStart(id, errSlowPart)
				waitFor(t, h1.reached, "first start to reserve")
				o.start(context.Background(), id, errSlowPart) // retried, fails again
				h2 := o.hold(id)                               // holds the retry
				close(h1.release)
				waitFor(t, h2.reached, "retry to reserve")
				o.start(context.Background(), id, nil) // refused by the retry
				close(h2.release)
				result(t, first, "first start and both retries")
			},
			retries: 2, started: 1, live: 1, status: "running",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrderOwner()
			o.setStatus(id, "stopped")
			tc.run(t, o)
			if got := o.retriesRun(id); got != tc.retries {
				t.Errorf("retries run = %d, want %d", got, tc.retries)
			}
			o.requireState(t, id, tc.status, tc.started, tc.live)
			if n := o.claimsLeft(); n != 0 {
				t.Errorf("claims left = %d, want 0", n)
			}
			o.cleanup(t)
		})
	}
}

// AC 4, white box: a retry that must never run is not kept on the claim
// either. Release already refuses to run it once the claim is no longer
// registered, so these pin the other half of the design (Activate, StopLocked
// and StopAllLocked drop it), which keeps the refused start's closure from
// outliving the decision and from running should Release's check change.
func TestStartGuardOrder_DroppedRetryNotKept(t *testing.T) {
	const id = "conn-1"
	never := func() bool { return false }

	cases := []struct {
		name string
		drop func(g *StartGuard, mu *sync.Mutex, c *StartClaim)
	}{
		{"Activate", func(_ *StartGuard, _ *sync.Mutex, c *StartClaim) {
			if !c.Activate(func() {}) {
				panic("Activate refused")
			}
		}},
		{"StopLocked", func(g *StartGuard, mu *sync.Mutex, _ *StartClaim) {
			mu.Lock()
			g.StopLocked(id)
			mu.Unlock()
		}},
		{"StopAllLocked", func(g *StartGuard, mu *sync.Mutex, _ *StartClaim) {
			mu.Lock()
			g.StopAllLocked()
			mu.Unlock()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var g StartGuard
			var mu sync.Mutex
			var ran atomic.Int32
			c, ok := g.ReserveOrRetry(&mu, id, never, nil)
			if !ok {
				t.Fatal("first reserve refused")
			}
			if _, ok := g.ReserveOrRetry(&mu, id, never, func() { ran.Add(1) }); ok {
				t.Fatal("second reserve not refused")
			}
			mu.Lock()
			kept := c.retry != nil
			mu.Unlock()
			if !kept {
				t.Fatal("refused start's retry not kept on the claim in flight")
			}

			tc.drop(&g, &mu, c)

			mu.Lock()
			kept = c.retry != nil
			mu.Unlock()
			if kept {
				t.Errorf("retry still kept on the claim after %s", tc.name)
			}
			c.Release()
			if n := ran.Load(); n != 0 {
				t.Errorf("retry ran %d times after %s, want 0", n, tc.name)
			}
		})
	}
}

// --- independence ----------------------------------------------------------

// AC 6: two connections (stand-ins for two tenants' connections) on one
// guard. A stop, a retry or a status write for one never affects the other.
func TestStartGuardOrder_ConnectionsIndependent(t *testing.T) {
	const a, b = "conn-tenant-a", "conn-tenant-b"

	cases := []struct {
		name string
		run  func(t *testing.T, o *orderOwner)
		// end state per connection: status, pollers started, running, retries
		wantA, wantB [4]any
	}{
		{
			// A's stop lands in B's #423 window and writes first.
			name: "stop of one inside the other's window",
			run: func(t *testing.T, o *orderOwner) {
				result(t, o.goStart(a, nil), "start a")
				o.onActivate = once(b, func() { o.stop(a) })
				result(t, o.goStart(b, nil), "start b")
			},
			wantA: [4]any{"stopped", 1, 0, 0},
			wantB: [4]any{"running", 1, 1, 0},
		},
		{
			// Many stops and starts of A move A's status order far ahead;
			// B's first "running" write still goes through.
			name: "status order of one does not hold back the other",
			run: func(t *testing.T, o *orderOwner) {
				for i := 0; i < 5; i++ {
					result(t, o.goStart(a, nil), "start a")
					o.stop(a)
				}
				var w StatusWrite
				o.onActivate = once(b, func() { _, w = o.stopLocked(a) })
				result(t, o.goStart(b, nil), "start b")
				o.writeStopped(a, w)
			},
			wantA: [4]any{"stopped", 5, 0, 0},
			wantB: [4]any{"running", 1, 1, 0},
		},
		{
			// B's start fails while A's refused start waits on A's claim:
			// A's retry is not run by B's Release, only by A's.
			name: "retry belongs to its own connection",
			run: func(t *testing.T, o *orderOwner) {
				ha, hb := o.hold(a), o.hold(b)
				firstA := o.goStart(a, errSlowPart)
				firstB := o.goStart(b, errSlowPart)
				waitFor(t, ha.reached, "start a to reserve")
				waitFor(t, hb.reached, "start b to reserve")
				o.start(context.Background(), a, nil) // refused, kept on A's claim
				close(hb.release)
				result(t, firstB, "start b")
				if n := o.retriesRun(a); n != 0 {
					t.Errorf("a's retry ran when b's start failed (%d)", n)
				}
				close(ha.release)
				result(t, firstA, "start a and its retry")
			},
			wantA: [4]any{"running", 1, 1, 1},
			wantB: [4]any{"stopped", 0, 0, 0},
		},
		{
			// A stop of B does not drop A's kept retry.
			name: "stop of one keeps the other's retry",
			run: func(t *testing.T, o *orderOwner) {
				result(t, o.goStart(b, nil), "start b")
				ha := o.hold(a)
				firstA := o.goStart(a, errSlowPart)
				waitFor(t, ha.reached, "start a to reserve")
				o.start(context.Background(), a, nil)
				o.stop(b)
				close(ha.release)
				result(t, firstA, "start a and its retry")
			},
			wantA: [4]any{"running", 1, 1, 1},
			wantB: [4]any{"stopped", 1, 0, 0},
		},
		{
			// Shutdown-free: a stop of A while A is mid-start leaves B,
			// mid-start too, to finish.
			name: "stop of one mid-start leaves the other's start",
			run: func(t *testing.T, o *orderOwner) {
				ha, hb := o.hold(a), o.hold(b)
				firstA := o.goStart(a, nil)
				firstB := o.goStart(b, nil)
				waitFor(t, ha.reached, "start a to reserve")
				waitFor(t, hb.reached, "start b to reserve")
				o.stop(a)
				close(ha.release)
				close(hb.release)
				if result(t, firstA, "start a") {
					t.Error("start a went ahead after its stop")
				}
				if !result(t, firstB, "start b") {
					t.Error("start b did not start")
				}
			},
			wantA: [4]any{"stopped", 0, 0, 0},
			wantB: [4]any{"running", 1, 1, 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrderOwner()
			o.setStatus(a, "stopped")
			o.setStatus(b, "stopped")
			tc.run(t, o)
			for _, c := range []struct {
				id   string
				want [4]any
			}{{a, tc.wantA}, {b, tc.wantB}} {
				o.requireState(t, c.id, c.want[0].(string), c.want[1].(int), c.want[2].(int))
				if got := o.retriesRun(c.id); got != c.want[3].(int) {
					t.Errorf("%s: retries run = %d, want %d", c.id, got, c.want[3])
				}
			}
			o.cleanup(t)
		})
	}
}

// --- zero values ---------------------------------------------------------------

// A zero StatusWrite, and RunningStatus of a nil claim, of a claim not yet
// activated, or of one whose Activate a stop refused, write nothing.
func TestStartGuardOrder_NoWriteWithoutActivate(t *testing.T) {
	const id = "conn-1"
	never := func() bool { return false }

	cases := []struct {
		name string
		w    func(t *testing.T) StatusWrite
	}{
		{"zero StatusWrite", func(*testing.T) StatusWrite { return StatusWrite{} }},
		{"nil claim", func(*testing.T) StatusWrite {
			var c *StartClaim
			return c.RunningStatus()
		}},
		{"claim not activated", func(t *testing.T) StatusWrite {
			var g StartGuard
			var mu sync.Mutex
			c, ok := g.ReserveOrRetry(&mu, id, never, nil)
			if !ok {
				t.Fatal("reserve refused")
			}
			defer c.Release()
			return c.RunningStatus()
		}},
		{"claim released without activating", func(t *testing.T) StatusWrite {
			var g StartGuard
			var mu sync.Mutex
			c, ok := g.ReserveOrRetry(&mu, id, never, nil)
			if !ok {
				t.Fatal("reserve refused")
			}
			c.Release()
			return c.RunningStatus()
		}},
		{"Activate refused by a stop", func(t *testing.T) StatusWrite {
			var g StartGuard
			var mu sync.Mutex
			c, ok := g.ReserveOrRetry(&mu, id, never, nil)
			if !ok {
				t.Fatal("reserve refused")
			}
			defer c.Release()
			mu.Lock()
			g.StopLocked(id)
			mu.Unlock()
			if c.Activate(func() { t.Error("insert ran after a stop") }) {
				t.Fatal("Activate went ahead after a stop")
			}
			return c.RunningStatus()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.w(t)
			if w.Write(func() { t.Error("write ran") }) {
				t.Error("Write reported true")
			}
		})
	}

	t.Run("activated claim writes", func(t *testing.T) {
		var g StartGuard
		var mu sync.Mutex
		c, ok := g.ReserveOrRetry(&mu, id, never, nil)
		if !ok {
			t.Fatal("reserve refused")
		}
		defer c.Release()
		if !c.Activate(func() {}) {
			t.Fatal("Activate refused")
		}
		ran := false
		if !c.RunningStatus().Write(func() { ran = true }) || !ran {
			t.Error("running write of an activated claim did not run")
		}
	})

	t.Run("zero guard: stop's write runs", func(t *testing.T) {
		var g StartGuard
		var mu sync.Mutex
		mu.Lock()
		w := g.StopStatusLocked(id)
		mu.Unlock()
		ran := false
		if !w.Write(func() { ran = true }) || !ran {
			t.Error("stop's write on a fresh guard did not run")
		}
	})
}

// --- stress --------------------------------------------------------------------

// Many iterations of concurrent starts (some failing), stops and refused
// starts on two connections, run under -race. Whatever the interleaving, each
// connection ends with at most one poller, its status says "running" exactly
// when a poller runs, and no claim is left behind.
func TestStartGuardOrder_StressStatusMatchesPoller(t *testing.T) {
	const iterations = 300
	ids := []string{"conn-tenant-a", "conn-tenant-b"}

	for i := 0; i < iterations; i++ {
		t.Run(fmt.Sprintf("iter-%03d", i), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(i)))
			o := newOrderOwner()
			var ops []func()
			for _, id := range ids {
				o.setStatus(id, "stopped")
				// Every iteration has at least one start, one stop and one
				// failing start per connection, plus a few random extras.
				ops = append(ops,
					func() { o.start(context.Background(), id, nil) },
					func() { o.start(context.Background(), id, errSlowPart) },
					func() { o.stop(id) },
				)
				for n := rng.Intn(4); n > 0; n-- {
					switch rng.Intn(3) {
					case 0:
						ops = append(ops, func() { o.start(context.Background(), id, nil) })
					case 1:
						ops = append(ops, func() { o.start(context.Background(), id, errSlowPart) })
					default:
						ops = append(ops, func() { o.stop(id) })
					}
				}
			}
			rng.Shuffle(len(ops), func(a, b int) { ops[a], ops[b] = ops[b], ops[a] })

			gate := make(chan struct{})
			var wg sync.WaitGroup
			for _, op := range ops {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-gate
					op()
				}()
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			close(gate)
			waitFor(t, done, "all starts and stops")

			for _, id := range ids {
				_, live := o.pollers(id)
				active := o.isActive(id)
				if live > 1 {
					t.Errorf("%s: %d pollers running", id, live)
				}
				if active != (live == 1) {
					t.Errorf("%s: active = %v with %d pollers running", id, active, live)
				}
				if st := o.statusOf(id); (st == "running") != (live == 1) {
					o.statusMu.Lock()
					log := append([]string(nil), o.log[id]...)
					o.statusMu.Unlock()
					t.Errorf("%s: status %q with %d pollers running (writes: %v)", id, st, live, log)
				}
			}
			if n := o.claimsLeft(); n != 0 {
				t.Errorf("claims left = %d, want 0", n)
			}
			o.cleanup(t)
		})
	}
}

// The #423 window under load: each iteration starts a connection and lands a
// stop (and sometimes a new start) in the window between Activate and the
// "running" write, with the stop's write racing the start's on another
// goroutine. The end status must match the end state.
func TestStartGuardOrder_StressStopInWindow(t *testing.T) {
	const id = "conn-1"
	const iterations = 300

	for i := 0; i < iterations; i++ {
		restart := i%2 == 1
		t.Run(fmt.Sprintf("iter-%03d", i), func(t *testing.T) {
			o := newOrderOwner()
			o.setStatus(id, "stopped")
			var wg sync.WaitGroup
			o.onActivate = once(id, func() {
				wasActive, w := o.stopLocked(id)
				wg.Add(1)
				go func() {
					defer wg.Done()
					if wasActive {
						o.writeStopped(id, w)
					}
				}()
				if restart {
					wg.Add(1)
					go func() {
						defer wg.Done()
						o.start(context.Background(), id, nil)
					}()
				}
			})
			result(t, o.goStart(id, nil), "start")
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			waitFor(t, done, "stop and restart")

			if restart {
				o.requireState(t, id, "running", 2, 1)
			} else {
				o.requireState(t, id, "stopped", 1, 0)
			}
			o.cleanup(t)
		})
	}
}

// --- status lanes ----------------------------------------------------------------

// heldWrite runs w.Write on its own goroutine with a write func that signals
// in, then waits for release. ran reports whether Write returned true.
type heldWrite struct {
	in, out chan struct{}
	release func()
	ran     atomic.Bool
	done    atomic.Bool // set by the write func just before it returns
}

func holdWrite(t *testing.T, w StatusWrite) *heldWrite {
	t.Helper()
	h := &heldWrite{in: make(chan struct{}), out: make(chan struct{})}
	rel := make(chan struct{})
	var once sync.Once
	h.release = func() { once.Do(func() { close(rel) }) }
	t.Cleanup(h.release) // a failing test never leaves the writer parked
	go func() {
		defer close(h.out)
		h.ran.Store(w.Write(func() {
			close(h.in)
			<-rel
			h.done.Store(true)
		}))
	}()
	return h
}

// goWrite runs w.Write on its own goroutine; the channel yields its result.
// write runs inside Write, if it runs.
func goWrite(w StatusWrite, write func()) <-chan bool {
	res := make(chan bool, 1)
	go func() { res <- w.Write(write) }()
	return res
}

// waitLaneUsers waits until n writes hold or wait for id's lane (white box:
// it tells that a second write has reached the lane before the first is
// released). Bounded; it yields instead of sleeping.
func waitLaneUsers(t *testing.T, g *StartGuard, id string, n int) {
	t.Helper()
	deadline := time.Now().Add(guardWait)
	for {
		g.statusMu.Lock()
		users := 0
		if l := g.lanes[id]; l != nil {
			users = l.users
		}
		g.statusMu.Unlock()
		if users >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d writes on %s's lane (have %d)", n, id, users)
		}
		runtime.Gosched()
	}
}

// Status writes for one connection run one at a time, but a write held for
// one connection (a slow database call) never holds up another connection's.
func TestStartGuardOrder_StatusWriteLanes(t *testing.T) {
	const a, b = "conn-tenant-a", "conn-tenant-b"
	never := func() bool { return false }

	// running activates id and returns its "running" write.
	running := func(t *testing.T, g *StartGuard, mu *sync.Mutex, id string) StatusWrite {
		t.Helper()
		c, ok := g.ReserveOrRetry(mu, id, never, nil)
		if !ok {
			t.Fatalf("reserve %s refused", id)
		}
		defer c.Release()
		if !c.Activate(func() {}) {
			t.Fatalf("activate %s refused", id)
		}
		return c.RunningStatus()
	}
	stopped := func(g *StartGuard, mu *sync.Mutex, id string) StatusWrite {
		mu.Lock()
		defer mu.Unlock()
		return g.StopStatusLocked(id)
	}

	t.Run("held write for one connection does not block another", func(t *testing.T) {
		var g StartGuard
		var mu sync.Mutex
		wa := running(t, &g, &mu, a)
		wbRun := running(t, &g, &mu, b)
		wbStop := stopped(&g, &mu, b)

		h := holdWrite(t, wa)
		waitFor(t, h.in, "a's running write to start")

		for _, w := range []struct {
			name string
			w    StatusWrite
		}{{"b's running write", wbRun}, {"b's stopped write", wbStop}} {
			if !result(t, goWrite(w.w, func() {}), w.name+" while a's write is held") {
				t.Errorf("%s skipped", w.name)
			}
		}
		if h.done.Load() {
			t.Fatal("a's write returned before it was released")
		}
		h.release()
		waitFor(t, h.out, "a's write to return")
		if !h.ran.Load() {
			t.Error("a's running write did not run")
		}
	})

	cases := []struct {
		name string
		// first is held; second arrives while it is held.
		order   func(t *testing.T, g *StartGuard, mu *sync.Mutex) (first, second StatusWrite)
		wantRun bool // whether second runs once first returns
	}{
		{
			name: "newer write waits, then runs",
			order: func(t *testing.T, g *StartGuard, mu *sync.Mutex) (StatusWrite, StatusWrite) {
				return running(t, g, mu, a), stopped(g, mu, a)
			},
			wantRun: true,
		},
		{
			name: "older write waits, then is skipped",
			order: func(t *testing.T, g *StartGuard, mu *sync.Mutex) (StatusWrite, StatusWrite) {
				older := running(t, g, mu, a)
				return stopped(g, mu, a), older
			},
			wantRun: false,
		},
	}
	for _, tc := range cases {
		t.Run("same connection: "+tc.name, func(t *testing.T) {
			var g StartGuard
			var mu sync.Mutex
			first, second := tc.order(t, &g, &mu)

			h := holdWrite(t, first)
			waitFor(t, h.in, "first write to start")

			var ranEarly, ran atomic.Bool
			res := goWrite(second, func() {
				ran.Store(true)
				if !h.done.Load() {
					ranEarly.Store(true)
				}
			})
			waitLaneUsers(t, &g, a, 2)
			if ran.Load() {
				t.Fatal("second write ran while the first was held")
			}
			h.release()
			got := result(t, res, "second write")
			waitFor(t, h.out, "first write to return")

			if ranEarly.Load() {
				t.Error("second write ran before the first returned")
			}
			if got != tc.wantRun || ran.Load() != tc.wantRun {
				t.Errorf("second write: Write = %v, ran = %v; want %v", got, ran.Load(), tc.wantRun)
			}
			if !h.ran.Load() {
				t.Error("first write did not run")
			}
		})
	}
}
