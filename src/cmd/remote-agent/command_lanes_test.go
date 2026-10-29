package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #429: the remote agent runs each connection's starts and stops one
// at a time, in arrival order (commandLanes). Without that, two starts both
// subscribed the output durable and the second session overwrote the first,
// whose subscriber nobody halted; a stop that landed while a start was loading
// was lost, and the start then wrote "running".
//
// A leaked subscriber is counted directly: every live output subscription runs
// one messaging dispatch loop goroutine, and the tests count those (as
// kafka-consumer's gfPollers counts pollers). A session overwritten in the map
// leaves its loop running after every session is stopped.
//
// Ordering is forced through the afterPipelineLoad seam and a status write's
// own sqlmock argument matcher, never with sleeps. A hook that lets a second
// command in waits, bounded, until that command is parked behind the running
// one in its lane, or (with the lanes gone) has run.

const (
	clConn = "conn-lanes"
	clWait = 5 * time.Second

	// Frames, matched without the package path: in a test binary the package
	// is named by its import path, not "main".
	// creatorDispatchLoop is the "created by" line of an output dispatch
	// loop (messaging.Subscribe's `go s.loop()`). It is there from the moment
	// the goroutine exists, before it has run once, unlike the loop's own
	// frame.
	creatorDispatchLoop = "/messaging.Subscribe in goroutine "
	frameCommand        = ".(*gateway).onCommand."
	frameLaneWait       = ".(*commandTurn).wait("
)

const clOutDirs = `[{"name":"out","mode":"write"}]`

// goroutinesWith counts the goroutines whose stack has every frame in frames
// (not counting "created by" lines, which name a goroutine's parent).
func goroutinesWith(frames ...string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		var lines []string
		for _, line := range strings.Split(g, "\n") {
			if !strings.HasPrefix(line, "created by ") {
				lines = append(lines, line)
			}
		}
		body := strings.Join(lines, "\n")
		all := true
		for _, f := range frames {
			if !strings.Contains(body, f) {
				all = false
				break
			}
		}
		if all {
			count++
		}
	}
	return count
}

// dispatchLoops counts the goroutines messaging.Subscribe started that have
// not exited: one per live subscription, whether or not it has run yet.
func dispatchLoops() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		for _, line := range strings.Split(g, "\n") {
			if strings.HasPrefix(line, "created by ") && strings.Contains(line, creatorDispatchLoop) {
				count++
				break
			}
		}
	}
	return count
}

// pollUntil polls cond for at most clWait and reports whether it held. For
// hooks that run off the test goroutine, where t.Fatal is not allowed.
func pollUntil(cond func() bool) bool {
	deadline := time.Now().Add(clWait)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// commandsSettled reports whether every command goroutine except self (the
// caller's own, 0 or 1) is parked in its lane, waiting for its turn. With the
// lanes gone, a command never parks; it is settled once it has run.
func commandsSettled(self int) bool {
	return goroutinesWith(frameCommand)-goroutinesWith(frameCommand, frameLaneWait) <= self
}

// statusLog records the status writes the gateway makes, in order. Each write
// is matched by a scripted expectation whose last argument is a statusArg.
type statusLog struct {
	mu     sync.Mutex
	writes []string
}

func (l *statusLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writes = append(l.writes, s)
}

func (l *statusLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.writes...)
}

func (l *statusLog) count(s string) int {
	n := 0
	for _, w := range l.get() {
		if w == s {
			n++
		}
	}
	return n
}

// statusArg matches the tenant argument of one status write and records the
// write. sqlmock calls a matcher more than once per statement, so it records
// once. before, if set, runs inside the write before it is recorded: the test
// lands a command there.
type statusArg struct {
	log    *statusLog
	status string
	tenant string
	before func()
	once   sync.Once
}

func (a *statusArg) Match(v driver.Value) bool {
	if v != a.tenant {
		return false
	}
	a.once.Do(func() {
		if a.before != nil {
			a.before()
		}
		a.log.add(a.status)
	})
	return true
}

type laneEnv struct {
	*testEnv
	log      *statusLog
	baseline int // dispatch loops already running when the test began
}

func newLaneEnv(t *testing.T) *laneEnv {
	t.Helper()
	l := &laneEnv{testEnv: newTestEnv(t), log: &statusLog{}, baseline: dispatchLoops()}
	return l
}

// outNodes is tenant 1's single-agent output pipeline; pattern varies the
// configuration (it is part of the fingerprint).
func outNodes(pattern string) []any {
	return []any{outputNode("out-1", agentA, "out", pattern)}
}

func (l *laneEnv) expectLoad(connID, tenantID string, nodes []any, err error) {
	q := l.mock.ExpectQuery(`SELECT nodes, edges FROM connections\s+WHERE id::text = \$1 AND tenant_id = \$2`).
		WithArgs(connID, tenantID)
	if err != nil {
		q.WillReturnError(err)
		return
	}
	q.WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow(mustJSON(nodes), mustJSON([]any{})))
}

// expectAgent scripts one lookup of agent A in tenant 1; found false answers
// as for an agent not in the workspace.
func (l *laneEnv) expectAgent(found bool) {
	q := l.mock.ExpectQuery(`SELECT directories FROM agents\s+WHERE id::text = \$1 AND tenant_id::text = \$2 AND revoked_at IS NULL`).
		WithArgs(agentA, tenant1)
	if found {
		q.WillReturnRows(sqlmock.NewRows([]string{"directories"}).AddRow([]byte(clOutDirs)))
	} else {
		q.WillReturnRows(sqlmock.NewRows([]string{"directories"}))
	}
}

// expectRunning scripts n recorded "running" writes; before runs in the first.
func (l *laneEnv) expectRunning(connID, tenantID string, n int, before func()) {
	for i := 0; i < n; i++ {
		arg := &statusArg{log: l.log, status: "running", tenant: tenantID}
		if i == 0 {
			arg.before = before
		}
		l.mock.ExpectExec(`UPDATE connections SET status = \$1`).WithArgs("running", connID, arg).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

// expectStopped scripts n recorded "stopped" writes. Script these before any
// "running" write whose matcher blocks: sqlmock locks each expectation while
// matching it, and walks them in the order scripted.
func (l *laneEnv) expectStopped(connID, tenantID string, n int) {
	for i := 0; i < n; i++ {
		l.mock.ExpectExec(`UPDATE connections SET status = 'stopped'`).
			WithArgs(connID, &statusArg{log: l.log, status: "stopped", tenant: tenantID}).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

func (l *laneEnv) expectRefused(connID, tenantID string) {
	l.mock.ExpectExec(`UPDATE connections SET status = 'error'`).WithArgs(sqlmock.AnyArg(), connID, tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func cmdMsg(start bool, connID, tenantID string) *nats.Msg {
	kind := "stop"
	if start {
		kind = "start"
	}
	return &nats.Msg{
		Subject: "vrsky.commands." + tenantID + ".connection." + kind,
		Data:    mustJSON(map[string]string{"connection_id": connID, "tenant_id": tenantID}),
	}
}

// command delivers a start or stop command the way the NATS subscription does.
func (l *laneEnv) command(start bool, connID, tenantID string) {
	l.g.onCommand(start)(cmdMsg(start, connID, tenantID))
}

// awaitCommands waits until no command goroutine is left.
func (l *laneEnv) awaitCommands(t *testing.T) {
	t.Helper()
	harness.Eventually(t, clWait, "every command to finish", func() bool { return goroutinesWith(frameCommand) == 0 })
}

func (l *laneEnv) sessions() map[string]*connSession {
	l.g.mu.Lock()
	defer l.g.mu.Unlock()
	out := make(map[string]*connSession, len(l.g.sessions))
	for k, v := range l.g.sessions {
		out[k] = v
	}
	return out
}

// loops is the number of output dispatch loops this test started that still
// run: one per live subscription.
func (l *laneEnv) loops() int { return dispatchLoops() - l.baseline }

// wantLive checks the connection has exactly one session, with the given
// fingerprint if non-empty, and exactly one live subscription.
func (l *laneEnv) wantLive(t *testing.T, connID, fp string) {
	t.Helper()
	sess := l.sessions()
	if len(sess) != 1 || sess[connID] == nil {
		t.Fatalf("sessions = %v, want exactly one, for %s", keys(sess), connID)
	}
	if fp != "" && sess[connID].fingerprint != fp {
		t.Errorf("session fingerprint = %s, want %s (the latest configuration)", sess[connID].fingerprint, fp)
	}
	// Bounded wait: a stopped loop's goroutine may still be on its way out
	// for a moment after Stop returns. A leaked or missing one stays.
	if !pollUntil(func() bool { return l.loops() == 1 }) {
		t.Errorf("live output subscriptions = %d, want 1", l.loops())
	}
}

// wantNothingLeft stops the connection (when stop is set) and checks no
// session and no subscription is left: a subscriber whose session was
// overwritten, or built after a stop or shutdown, would still be running.
func (l *laneEnv) wantNothingLeft(t *testing.T, connID string, stop bool) {
	t.Helper()
	if stop {
		l.g.stopConnection(context.Background(), connID, tenant1)
	}
	if sess := l.sessions(); len(sess) != 0 {
		t.Errorf("sessions left = %v, want none", keys(sess))
	}
	// Bounded wait, as in wantLive: a leaked subscriber never goes away.
	if !pollUntil(func() bool { return l.loops() == 0 }) {
		t.Errorf("output subscriptions still running with no session = %d, want 0 (a leaked subscriber)", l.loops())
	}
}

func keys(m map[string]*connSession) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func fingerprintOf(t *testing.T, nodes []any) string {
	t.Helper()
	in, out, err := parseRemoteNodes(mustJSON(nodes), mustJSON([]any{}))
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint(in, out)
}

// secondStartHook lets a second start in while the first is loading. The
// first load fires second() and waits until the second is parked in the lane
// or has itself loaded; the second load, if it gets there while the first is
// still held (no lanes), waits for the first to be released, so the two then
// race for the session. Errors go to errs: the hook runs off the test
// goroutine in some cases.
func secondStartHook(second func(), errs chan<- string) func(string) {
	var loads atomic.Int32
	secondLoaded, release := make(chan struct{}), make(chan struct{})
	return func(connID string) {
		switch loads.Add(1) {
		case 1:
			second()
			if !pollUntil(func() bool {
				select {
				case <-secondLoaded:
					return true
				default:
					return goroutinesWith(frameLaneWait) >= 1
				}
			}) {
				errs <- "second start neither queued behind the first nor ran"
			}
			close(release)
		case 2:
			close(secondLoaded)
			select {
			case <-release:
			case <-time.After(clWait):
			}
		}
	}
}

func drainErrs(t *testing.T, errs chan string) {
	t.Helper()
	for {
		select {
		case e := <-errs:
			t.Error(e)
		default:
			return
		}
	}
}

// AC1, AC4: two starts of one connection, in whichever order restore and the
// start command come, leave one session and one live output subscription, and
// "running" written once; once stopped, no subscriber is left.
func TestCommandLanes_ConcurrentStartsOneSession(t *testing.T) {
	type starter func(l *laneEnv, done chan<- struct{})
	restore := func(l *laneEnv, done chan<- struct{}) {
		go func() {
			defer close(done)
			l.g.startConnection(context.Background(), clConn, tenant1)
		}()
	}
	command := func(l *laneEnv, done chan<- struct{}) {
		l.command(true, clConn, tenant1)
		close(done)
	}
	cases := []struct {
		name          string
		first, second starter
	}{
		{"restore, then a start command", restore, command},
		{"a start command, then restore", command, restore},
		{"two start commands", command, command},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLaneEnv(t)
			nodes := outNodes("")
			// Enough for both starts to run in full; with the lanes, the
			// second finds the session running and stops after its load.
			l.expectLoad(clConn, tenant1, nodes, nil)
			l.expectLoad(clConn, tenant1, nodes, nil)
			l.expectAgent(true)
			l.expectAgent(true)
			l.expectRunning(clConn, tenant1, 2, nil)

			errs := make(chan string, 4)
			firstDone, secondDone := make(chan struct{}), make(chan struct{})
			l.g.afterPipelineLoad = secondStartHook(func() { tc.second(l, secondDone) }, errs)
			tc.first(l, firstDone)

			for _, ch := range []chan struct{}{firstDone, secondDone} {
				select {
				case <-ch:
				case <-time.After(clWait):
					t.Fatal("timed out waiting for the starts to be delivered")
				}
			}
			harness.Eventually(t, clWait, "both starts to finish", func() bool {
				return goroutinesWith(frameCommand) == 0 && goroutinesWith(".(*gateway).startConnection(") == 0
			})
			drainErrs(t, errs)

			l.wantLive(t, clConn, "")
			if n := l.log.count("running"); n != 1 {
				t.Errorf(`"running" written %d times, want 1`, n)
			}
			l.wantNothingLeft(t, clConn, true)
		})
	}
}

// AC2, AC4: a stop that arrives while a start is in progress, or queued right
// behind it, wins: no session, no subscription, no "running".
func TestCommandLanes_StopDuringStartWins(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, l *laneEnv, errs chan<- string)
	}{
		{"stop while restore loads", func(t *testing.T, l *laneEnv, errs chan<- string) {
			l.g.afterPipelineLoad = func(string) {
				l.command(false, clConn, tenant1)
				if !pollUntil(func() bool { return commandsSettled(0) }) {
					errs <- "stop neither queued nor ran"
				}
			}
			l.g.startConnection(context.Background(), clConn, tenant1)
		}},
		{"stop while a start command loads", func(t *testing.T, l *laneEnv, errs chan<- string) {
			l.g.afterPipelineLoad = func(string) {
				l.command(false, clConn, tenant1)
				if !pollUntil(func() bool { return commandsSettled(1) }) {
					errs <- "stop neither queued nor ran"
				}
			}
			l.command(true, clConn, tenant1)
		}},
		{"stop queued right behind a start", func(t *testing.T, l *laneEnv, errs chan<- string) {
			// A turn held ahead of both keeps the start from running before
			// the stop is queued behind it.
			ahead := l.g.commands.queue(clConn, tenant1, false)
			l.command(true, clConn, tenant1)
			l.command(false, clConn, tenant1)
			ahead.done()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLaneEnv(t)
			// Scripted so that a start the stop failed to stop could run in
			// full and show up.
			l.expectStopped(clConn, tenant1, 1)
			l.expectLoad(clConn, tenant1, outNodes(""), nil)
			l.expectAgent(true)
			l.expectRunning(clConn, tenant1, 1, nil)

			errs := make(chan string, 4)
			tc.run(t, l, errs)
			l.awaitCommands(t)
			drainErrs(t, errs)

			if w := l.log.get(); len(w) != 0 {
				t.Errorf("status writes = %v, want none (the stop overtook the start before it ran)", w)
			}
			l.wantNothingLeft(t, clConn, false)
		})
	}
}

// AC2: a stop that arrives after the start has registered its session, while
// the start is still writing "running", ends the session once the start's turn
// is over and writes "stopped" after "running".
func TestCommandLanes_StopAfterInsertWritesStoppedLast(t *testing.T) {
	l := newLaneEnv(t)
	errs := make(chan string, 4)
	l.expectStopped(clConn, tenant1, 1) // first: the running write's matcher holds its own expectation
	l.expectLoad(clConn, tenant1, outNodes(""), nil)
	l.expectAgent(true)
	l.expectRunning(clConn, tenant1, 1, func() {
		l.command(false, clConn, tenant1)
		if !pollUntil(func() bool { return commandsSettled(0) }) {
			errs <- "stop neither queued nor ran"
		}
	})

	l.g.startConnection(context.Background(), clConn, tenant1)
	l.awaitCommands(t)
	drainErrs(t, errs)

	if got := strings.Join(l.log.get(), ","); got != "running,stopped" {
		t.Errorf(`status writes = %q, want "running,stopped" (the stop's "stopped" last)`, got)
	}
	l.wantNothingLeft(t, clConn, false)
}

// AC3: a start queued behind a start that fails is not lost: it runs once the
// first is done and starts the pipeline.
func TestCommandLanes_StartQueuedBehindFailingStartStarts(t *testing.T) {
	cases := []struct {
		name   string
		script func(l *laneEnv)
	}{
		{"first lookup fails", func(l *laneEnv) {
			l.expectLoad(clConn, tenant1, nil, errors.New("read tcp: connection reset by peer"))
			l.expectLoad(clConn, tenant1, outNodes(""), nil)
			l.expectAgent(true)
		}},
		{"first refused (agent lookup)", func(l *laneEnv) {
			l.expectLoad(clConn, tenant1, outNodes(""), nil)
			l.expectLoad(clConn, tenant1, outNodes(""), nil)
			l.expectAgent(false)
			l.expectAgent(true)
			l.expectRefused(clConn, tenant1)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLaneEnv(t)
			tc.script(l)
			l.expectRunning(clConn, tenant1, 2, nil)

			l.command(true, clConn, tenant1)
			l.command(true, clConn, tenant1)
			l.awaitCommands(t)

			l.wantLive(t, clConn, "")
			if n := l.log.count("running"); n != 1 {
				t.Errorf(`"running" written %d times, want 1`, n)
			}
			l.wantNothingLeft(t, clConn, true)
		})
	}
}

// AC4: a start with a changed configuration replaces the running session,
// whether it arrives after the first start finished or while it is loading:
// one session, on the new configuration, one subscription.
func TestCommandLanes_ChangedConfigReplacesSession(t *testing.T) {
	cases := []struct {
		name       string
		concurrent bool
	}{
		{"after the first start", false},
		{"while the first start loads", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLaneEnv(t)
			oldNodes, newNodes := outNodes(""), outNodes("{id}.json")
			l.expectLoad(clConn, tenant1, oldNodes, nil)
			l.expectLoad(clConn, tenant1, newNodes, nil)
			l.expectAgent(true)
			l.expectAgent(true)
			l.expectRunning(clConn, tenant1, 2, nil)

			errs := make(chan string, 4)
			if tc.concurrent {
				l.g.afterPipelineLoad = secondStartHook(func() { l.command(true, clConn, tenant1) }, errs)
			}
			l.g.startConnection(context.Background(), clConn, tenant1)
			if !tc.concurrent {
				l.wantLive(t, clConn, fingerprintOf(t, oldNodes))
				l.command(true, clConn, tenant1)
			}
			l.awaitCommands(t)
			drainErrs(t, errs)

			l.wantLive(t, clConn, fingerprintOf(t, newNodes))
			if n := l.log.count("running"); n != 2 {
				t.Errorf(`"running" written %d times, want 2 (one per configuration)`, n)
			}
			l.wantNothingLeft(t, clConn, true)
		})
	}
}

// AC4: shutdown while a start is loading, or before a queued start runs,
// leaves no session and no subscription.
func TestCommandLanes_ShutdownDuringStartLeavesNothing(t *testing.T) {
	cases := []struct {
		name string
		run  func(l *laneEnv)
	}{
		{"while the start loads", func(l *laneEnv) {
			l.g.afterPipelineLoad = func(string) { _ = l.g.Stop(context.Background()) }
			l.g.startConnection(context.Background(), clConn, tenant1)
		}},
		{"before a start command runs", func(l *laneEnv) {
			_ = l.g.Stop(context.Background())
			l.command(true, clConn, tenant1)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLaneEnv(t)
			l.expectLoad(clConn, tenant1, outNodes(""), nil)
			l.expectAgent(true)
			l.expectRunning(clConn, tenant1, 1, nil)

			tc.run(l)
			l.awaitCommands(t)

			if n := l.log.count("running"); n != 0 {
				t.Errorf(`"running" written %d times after shutdown, want 0`, n)
			}
			l.wantNothingLeft(t, clConn, false)
		})
	}
}

// A stop naming another tenant for the connection, arriving while tenant 1's
// start loads, stops nothing: the start completes and nothing writes "stopped".
func TestCommandLanes_StopForAnotherTenantDoesNotCancelStart(t *testing.T) {
	l := newLaneEnv(t)
	errs := make(chan string, 4)
	l.expectStopped(clConn, tenant1, 1)
	l.expectStopped(clConn, tenant2, 1)
	l.expectLoad(clConn, tenant1, outNodes(""), nil)
	l.expectAgent(true)
	l.expectRunning(clConn, tenant1, 1, nil)
	l.g.afterPipelineLoad = func(string) {
		l.command(false, clConn, tenant2)
		if !pollUntil(func() bool { return commandsSettled(0) }) {
			errs <- "stop neither queued nor ran"
		}
	}

	l.g.startConnection(context.Background(), clConn, tenant1)
	l.awaitCommands(t)
	drainErrs(t, errs)

	l.wantLive(t, clConn, "")
	if got := strings.Join(l.log.get(), ","); got != "running" {
		t.Errorf(`status writes = %q, want "running" only`, got)
	}
	l.wantNothingLeft(t, clConn, true)
}

// commandLanes on its own: order, stops per tenant, cleanup.
func TestCommandLanes_Unit(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, c *commandLanes)
	}{
		{"turns run in queue order", func(t *testing.T, c *commandLanes) {
			const n = 5
			var mu sync.Mutex
			var order []int
			turns := make([]*commandTurn, n)
			for i := range turns {
				turns[i] = c.queue("conn", tenant1, false)
			}
			var wg sync.WaitGroup
			// Started in reverse, so only the lane can put them in order.
			for i := n - 1; i >= 0; i-- {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					turns[i].wait()
					mu.Lock()
					order = append(order, i)
					mu.Unlock()
					turns[i].done()
				}(i)
			}
			waitGroup(t, &wg)
			if got := fmt.Sprint(order); got != "[0 1 2 3 4]" {
				t.Errorf("order = %s, want [0 1 2 3 4]", got)
			}
		}},
		{"a turn waits for the one before it only", func(t *testing.T, c *commandLanes) {
			first := c.queue("conn", tenant1, false)
			second := c.queue("conn", tenant1, false)
			other := c.queue("other-conn", tenant1, false)
			waited := make(chan struct{})
			go func() { second.wait(); close(waited) }()
			waitChan(t, goDo(other.wait), "a turn on another connection")
			select {
			case <-waited:
				t.Fatal("second turn ran before the first was done")
			default:
			}
			first.done()
			waitChan(t, waited, "second turn once the first is done")
			second.done()
			other.done()
		}},
		{"a stop is seen by earlier starts of its tenant only", func(t *testing.T, c *commandLanes) {
			startT1 := c.queue("conn", tenant1, false)
			startT2 := c.queue("conn", tenant2, false)
			if startT1.stoppedSince() || startT2.stoppedSince() {
				t.Fatal("stoppedSince before any stop")
			}
			stopT2 := c.queue("conn", tenant2, true)
			if startT1.stoppedSince() {
				t.Error("tenant 1's start sees tenant 2's stop")
			}
			if !startT2.stoppedSince() {
				t.Error("tenant 2's start does not see tenant 2's stop")
			}
			later := c.queue("conn", tenant2, false)
			if later.stoppedSince() {
				t.Error("a start queued after the stop sees it")
			}
			otherConn := c.queue("other-conn", tenant2, false)
			c.queue("other-conn", tenant2, true).done()
			if startT2.stoppedSince() != true || later.stoppedSince() {
				t.Error("a stop on another connection moved this one's stops")
			}
			for _, tr := range []*commandTurn{startT1, startT2, stopT2, later, otherConn} {
				tr.done()
			}
		}},
		{"lanes are dropped once every turn is done", func(t *testing.T, c *commandLanes) {
			a := c.queue("conn", tenant1, false)
			b := c.queue("conn", tenant1, true)
			x := c.queue("other-conn", tenant1, false)
			a.done()
			x.done()
			if n := laneCount(c); n != 1 {
				t.Errorf("lanes with a turn still queued = %d, want 1", n)
			}
			b.done()
			if n := laneCount(c); n != 0 {
				t.Errorf("lanes after every turn is done = %d, want 0", n)
			}
			// A fresh lane starts with no stops counted and nothing to wait for.
			fresh := c.queue("conn", tenant1, false)
			waitChan(t, goDo(fresh.wait), "first turn on a fresh lane")
			if fresh.stoppedSince() {
				t.Error("a fresh lane remembers an old stop")
			}
			fresh.done()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, &commandLanes{})
		})
	}
}

func laneCount(c *commandLanes) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.lanes)
}

func goDo(fn func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	return done
}

func waitChan(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(clWait):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitGroup(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	waitChan(t, goDo(wg.Wait), "every turn")
}
