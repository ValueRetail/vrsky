package main

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Behavioural tests for this consumer's wiring of the start guard fixes
// (#424; this consumer writes no status, so #423 does not apply), on the sqlmock fixture and hold helpers in start_race_test.go.
// Ordering is forced through the guard's AfterReserve and AfterActivate
// hooks, never with sleeps; every wait is bounded by srWait.

// gfPollerFrame names the function of the goroutine every start of this
// consumer runs once it is active. In a test binary the package is named by
// its import path, not "main", so it is matched without the package.
const gfPollerFrame = ".(*bcConsumer).runPoller("

// gfPollers counts the goroutines that have gfPollerFrame on their stack
// (not counting "created by" lines, which name a goroutine's parent).
func gfPollers() int {
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
			if !strings.HasPrefix(line, "created by ") && strings.Contains(line, gfPollerFrame) {
				count++
				break
			}
		}
	}
	return count
}

// gfAwaitPollers waits until exactly want poller goroutines run.
func gfAwaitPollers(t *testing.T, want int) {
	t.Helper()
	harness.Eventually(t, srWait, fmt.Sprintf("%d poller goroutine(s) running", want), func() bool {
		return gfPollers() == want
	})
}

// #424: a start refused because another start of the connection is in flight
// runs once that one fails (here on its lookup), and the connection ends with
// exactly one poller.
func TestGuardFix_RefusedStartRunsWhenFirstFails(t *testing.T) {
	f := newSRFixture(t)
	f.expectLookup(errors.New("read tcp: connection reset by peer"))
	f.expectLookup(nil)
	f.expectRunning()
	h := srHoldFirst(&f.c.starts)

	first := f.goStart()
	srAwait(t, h.reached, "first start to claim the connection")
	srAwait(t, f.goStart(), "second start, refused while the first is in flight")
	if n := h.claims.Load(); n != 1 {
		t.Fatalf("starts that claimed the connection while the first is held = %d, want 1", n)
	}
	close(h.release)
	srAwait(t, first, "first start, and the refused start it runs again")

	if n := h.claims.Load(); n != 2 {
		t.Errorf("starts that claimed the connection = %d, want 2 (the failed one, then the refused one)", n)
	}
	if n := f.activeCount(); n != 1 {
		t.Fatalf("active connections = %d, want 1", n)
	}
	harness.Eventually(t, srWait, "the poller to get to work (data requests the fake API received)", func() bool { return f.polls.Load() >= 1 })
	gfAwaitPollers(t, 1)
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db (failed lookup, lookup, then status): %v", err)
	}
	if n := f.polls.Load(); n != 1 {
		t.Errorf("data requests the fake API received = %d, want 1 (one poller)", n)
	}
}
