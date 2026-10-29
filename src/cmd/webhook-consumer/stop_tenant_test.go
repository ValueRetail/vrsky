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
// on its subject, the tenant the webhook was registered for. They reuse the
// sqlmock fixture in start_guard_test.go. A status write the stop should not
// make is caught by a sentinel expectation that matches any status UPDATE:
// the test takes it itself afterwards, which fails if the code took it first.

const stOther = "tenant-other"

func stStop(subjectTenant, payloadTenant string) *nats.Msg {
	data, _ := json.Marshal(map[string]string{"connection_id": wgConn, "tenant_id": payloadTenant})
	return &nats.Msg{Subject: "vrsky.commands." + subjectTenant + ".connection.stop", Data: data}
}

// stForeignStops are stops that must change nothing for wgTenant's webhook.
var stForeignStops = []struct {
	name string
	msg  func() *nats.Msg
}{
	{"payload and subject name another tenant", func() *nats.Msg { return stStop(stOther, stOther) }},
	{"payload names the tenant, subject another", func() *nats.Msg { return stStop(stOther, wgTenant) }},
	{"payload names another tenant, subject the tenant", func() *nats.Msg { return stStop(wgTenant, stOther) }},
}

func (f *wgFixture) stEntry() *ActiveConnection {
	f.c.mu.RLock()
	defer f.c.mu.RUnlock()
	return f.c.activeConnections[wgConn]
}

// stCountCancels wraps the active entry's Cancel so the test sees a cancel.
func (f *wgFixture) stCountCancels(t *testing.T) (*ActiveConnection, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	ac := f.c.activeConnections[wgConn]
	if ac == nil {
		t.Fatal("webhook not registered after its start")
	}
	orig := ac.Cancel
	ac.Cancel = func() { n.Add(1); orig() }
	return ac, &n
}

func (f *wgFixture) stExpectSentinel() {
	f.mock.ExpectExec("UPDATE connections SET status").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func (f *wgFixture) stTakeSentinel(t *testing.T, what string) {
	t.Helper()
	if _, err := f.c.db.Exec("UPDATE connections SET status = $1 WHERE id = $2 AND tenant_id = $3", "sentinel", "x", "y"); err != nil {
		t.Errorf("%s wrote a status: %v", what, err)
	}
}

func TestStopTenant_ForeignStopLeavesWebhookRegistered(t *testing.T) {
	for _, tc := range stForeignStops {
		t.Run(tc.name, func(t *testing.T) {
			f := newWGFixture(t)
			f.expectLookup(nil)
			f.expectStatus("running")
			f.c.startConnection(context.Background(), wgConn, wgTenant)
			ac, cancels := f.stCountCancels(t)

			f.stExpectSentinel()
			f.c.handleStopCommand(tc.msg())

			if got := f.stEntry(); got != ac {
				t.Fatalf("registered entry after a foreign stop = %p, want the start's %p", got, ac)
			}
			if n := cancels.Load(); n != 0 {
				t.Errorf("webhook cancelled %d times by a foreign stop, want 0", n)
			}
			f.stTakeSentinel(t, "a foreign stop")
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db: %v", err)
			}

			// The tenant's own stop still unregisters it.
			f.expectStatus("stopped")
			f.c.handleStopCommand(stStop(wgTenant, wgTenant))
			if f.stEntry() != nil {
				t.Error("webhook still registered after its tenant's stop")
			}
			if n := cancels.Load(); n != 1 {
				t.Errorf("webhook cancelled %d times by its tenant's stop, want 1", n)
			}
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf(`db ("stopped" for the tenant): %v`, err)
			}
		})
	}
}

// A foreign stop while the start is held in its slow part does not abandon
// the start: it registers and writes "running".
func TestStopTenant_ForeignStopMidStartDoesNotAbandon(t *testing.T) {
	for _, tc := range stForeignStops {
		t.Run(tc.name, func(t *testing.T) {
			f := newWGFixture(t)
			f.expectLookup(nil)
			f.expectStatus("running")
			h := wgHoldFirst(&f.c.starts)

			done := goDone(func() { f.c.startConnection(context.Background(), wgConn, wgTenant) })
			wgAwait(t, h.reached, "start to claim the connection")
			f.c.handleStopCommand(tc.msg())
			close(h.release)
			wgAwait(t, done, "start")

			if f.stEntry() == nil {
				t.Fatal("start abandoned by a foreign stop")
			}
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf(`db (lookup and "running"): %v`, err)
			}
		})
	}
}
