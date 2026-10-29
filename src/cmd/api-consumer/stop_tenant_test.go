package main

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"
)

// Tests for #498: a stop command acts only when it names, in its payload and
// on its subject, the tenant the connection was started for. They reuse the
// sqlmock fixture in start_race_test.go. A status write the stop should not
// make is caught by a sentinel expectation that matches any status UPDATE:
// the test takes it itself afterwards, which fails if the code took it first.

const stOther = "tenant-other"

func stStop(subjectTenant, payloadTenant string) *nats.Msg {
	data, _ := json.Marshal(map[string]string{"connection_id": srConn, "tenant_id": payloadTenant})
	return &nats.Msg{Subject: "vrsky.commands." + subjectTenant + ".connection.stop", Data: data}
}

// stForeignStops are stops that must change nothing for srTenant's connection.
var stForeignStops = []struct {
	name string
	msg  func() *nats.Msg
}{
	{"payload and subject name another tenant", func() *nats.Msg { return stStop(stOther, stOther) }},
	{"payload names the tenant, subject another", func() *nats.Msg { return stStop(stOther, srTenant) }},
	{"payload names another tenant, subject the tenant", func() *nats.Msg { return stStop(srTenant, stOther) }},
}

// stExpectSentinel scripts one status UPDATE with any arguments.
func stExpectSentinel(f *srFixture) {
	f.mock.ExpectExec("UPDATE connections SET status").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// stTakeSentinel takes the sentinel; it fails if the code wrote a status.
func stTakeSentinel(t *testing.T, f *srFixture, what string) {
	t.Helper()
	if _, err := f.c.db.Exec("UPDATE connections SET status = $1 WHERE id = $2 AND tenant_id = $3", "sentinel", "x", "y"); err != nil {
		t.Errorf("%s wrote a status: %v", what, err)
	}
}

// stCountCancels wraps the active entry's cancel so the test sees a cancel.
func stCountCancels(t *testing.T, f *srFixture) (*activeConn, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	ac := f.c.activePipelines[srConn]
	if ac == nil {
		t.Fatal("connection not active after its start")
	}
	orig := ac.cancel
	ac.cancel = func() { n.Add(1); orig() }
	return ac, &n
}

func TestStopTenant_ForeignStopLeavesConnectionRunning(t *testing.T) {
	for _, tc := range stForeignStops {
		t.Run(tc.name, func(t *testing.T) {
			f := newSRFixture(t)
			f.expectLookup(nil)
			f.expectRunning()
			f.c.startConnection(context.Background(), srConn, srTenant)
			ac, cancels := stCountCancels(t, f)

			stExpectSentinel(f)
			f.c.handleStopCommand(tc.msg())

			if got := f.entry(); got != ac {
				t.Fatalf("active entry after a foreign stop = %p, want the start's %p", got, ac)
			}
			if n := cancels.Load(); n != 0 {
				t.Errorf("connection cancelled %d times by a foreign stop, want 0", n)
			}
			stTakeSentinel(t, f, "a foreign stop")
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db: %v", err)
			}

			// The tenant's own stop still stops it.
			f.expectStatus("stopped")
			f.c.handleStopCommand(stStop(srTenant, srTenant))
			if got := f.entry(); got != nil {
				t.Error("connection still active after its tenant's stop")
			}
			if n := cancels.Load(); n != 1 {
				t.Errorf("connection cancelled %d times by its tenant's stop, want 1", n)
			}
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf(`db ("stopped" for the tenant): %v`, err)
			}
		})
	}
}

// A foreign stop while the start is held in its slow part does not abandon
// the start: it activates and writes "running".
func TestStopTenant_ForeignStopMidStartDoesNotAbandon(t *testing.T) {
	for _, tc := range stForeignStops {
		t.Run(tc.name, func(t *testing.T) {
			f := newSRFixture(t)
			f.expectLookup(nil)
			f.expectRunning()
			h := srHoldFirst(&f.c.starts)

			done := f.goStart()
			srAwait(t, h.reached, "start to claim the connection")
			f.c.handleStopCommand(tc.msg())
			close(h.release)
			srAwait(t, done, "start")

			if f.entry() == nil {
				t.Fatal("start abandoned by a foreign stop")
			}
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf(`db (lookup and "running"): %v`, err)
			}
		})
	}
}
