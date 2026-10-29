package main

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #498: a stop command acts only when it names the tenant the
// connection was started for, in its payload and on its subject. On the
// sqlmock fixture and hold helpers in start_race_test.go and the poller
// counting in start_guard_fix_test.go; no sleeps, every wait bounded by srWait.

const (
	stOtherTenant = "tenant-other"
	// stWritesStopped: this consumer writes no status when a stop cancels an
	// active connection.
	stWritesStopped = false
)

// stStopMsg is a stop for srConn on subjectTenant's command subject, naming
// payloadTenant in its payload.
func stStopMsg(subjectTenant, payloadTenant string) *nats.Msg {
	data, _ := json.Marshal(map[string]string{"connection_id": srConn, "tenant_id": payloadTenant})
	return &nats.Msg{Subject: "vrsky.commands." + subjectTenant + ".connection.stop", Data: data}
}

// stRefusedStops are stops for srConn that must change nothing: each names a
// tenant other than srTenant on its subject, in its payload, or both.
var stRefusedStops = []struct {
	name, subject, payload string
}{
	{"payload and subject name another tenant", stOtherTenant, stOtherTenant},
	{"subject names another tenant", stOtherTenant, srTenant},
	{"payload names another tenant", srTenant, stOtherTenant},
}

// stRecord is a sqlmock argument matcher that matches anything and records
// what it was matched against, in order.
type stRecord struct {
	mu   sync.Mutex
	vals []string
}

func (r *stRecord) Match(v driver.Value) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vals = append(r.vals, fmt.Sprint(v))
	return true
}

func (r *stRecord) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprint(r.vals)
}

// stExpectAnyStatusWrite adds an expectation any status UPDATE takes, and
// returns the recorder of its arguments.
func stExpectAnyStatusWrite(f *srFixture) *stRecord {
	rec := &stRecord{}
	f.mock.ExpectExec("UPDATE connections SET status").WithArgs(rec, rec, rec)
	return rec
}

// stStartActive starts srConn for srTenant and waits until its poller has
// polled, with every expected query taken.
func stStartActive(t *testing.T, f *srFixture) {
	t.Helper()
	f.expectLookup(nil)
	f.expectRunning()
	f.c.startConnection(context.Background(), srConn, srTenant)
	if n := f.activeCount(); n != 1 {
		t.Fatalf("active connections after the start = %d, want 1", n)
	}
	harness.Eventually(t, srWait, "the poller to poll", func() bool { return f.polls.Load() >= 1 })
	gfAwaitPollers(t, 1)
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("db after the start: %v", err)
	}
}

// stSpyCancel wraps the cancel of srConn's active entry so the test sees
// whether, and how often, its context is cancelled.
func stSpyCancel(t *testing.T, f *srFixture) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	cancel, ok := f.c.active[srConn]
	if !ok {
		t.Fatal("no active entry to watch")
	}
	f.c.active[srConn] = func() { n.Add(1); cancel() }
	return &n
}

// A stop naming another tenant, on its payload, its subject or both, leaves
// the connection running and writes no status; a stop naming its own tenant
// then still stops it.
func TestStopTenant_RefusedStopLeavesConnectionRunning(t *testing.T) {
	for _, tc := range stRefusedStops {
		t.Run(tc.name, func(t *testing.T) {
			f := newSRFixture(t)
			stStartActive(t, f)
			cancels := stSpyCancel(t, f)
			rec := stExpectAnyStatusWrite(f)

			f.c.handleStopCommand(stStopMsg(tc.subject, tc.payload))

			if n := cancels.Load(); n != 0 {
				t.Errorf("the connection's context was cancelled %d time(s) by a refused stop, want 0", n)
			}
			if n := f.activeCount(); n != 1 {
				t.Errorf("active connections after a refused stop = %d, want 1", n)
			}
			if n := gfPollers(); n != 1 {
				t.Errorf("pollers running after a refused stop = %d, want 1", n)
			}
			if f.mock.ExpectationsWereMet() == nil {
				t.Errorf("a refused stop wrote a status: %s", rec)
			}

			f.c.handleStopCommand(srStopMsg())

			if n := cancels.Load(); n != 1 {
				t.Errorf("the connection's context was cancelled %d time(s) by its own tenant's stop, want 1", n)
			}
			if n := f.activeCount(); n != 0 {
				t.Errorf("active connections after its own tenant's stop = %d, want 0", n)
			}
			gfAwaitPollers(t, 0)
			err := f.mock.ExpectationsWereMet()
			if stWritesStopped {
				if err != nil {
					t.Errorf("db: its own tenant's stop wrote no status: %v", err)
				}
				if got, want := rec.String(), fmt.Sprint([]string{"stopped", srConn, srTenant}); got != want {
					t.Errorf("status write = %s, want %s", got, want)
				}
			} else if err == nil {
				t.Errorf("a stop wrote a status (%s); this consumer writes none on stop", rec)
			}
		})
	}
}

// A stop naming another tenant while a start is in progress does not abandon
// the start: it activates, its poller runs, and the start writes the status
// it writes without a stop.
func TestStopTenant_RefusedStopKeepsStartInProgress(t *testing.T) {
	for _, tc := range stRefusedStops {
		t.Run(tc.name, func(t *testing.T) {
			f := newSRFixture(t)
			f.expectLookup(nil)
			f.expectRunning()
			h := srHoldFirst(&f.c.starts)

			first := f.goStart()
			srAwait(t, h.reached, "start to claim the connection")
			f.c.handleStopCommand(stStopMsg(tc.subject, tc.payload))
			close(h.release)
			srAwait(t, first, "start")

			if n := f.activeCount(); n != 1 {
				t.Fatalf("active connections after a refused stop mid-start = %d, want 1", n)
			}
			harness.Eventually(t, srWait, "the poller to poll", func() bool { return f.polls.Load() >= 1 })
			gfAwaitPollers(t, 1)
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db (lookup, then status): %v", err)
			}
		})
	}
}
