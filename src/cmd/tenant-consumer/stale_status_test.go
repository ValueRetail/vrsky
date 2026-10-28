package main

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #456: after start A, stop, start B, the status in the database is
// B's "running". A's bridge failing late must not write "error" on top of it.
// A bridge that fails while its entry is still its own still writes "error"
// and drops its entry.
//
// The setup failures covered are the ones the fixtures can drive: no approved
// data connection, source connection not shared, and subscribe fails. The
// JetStream-context failure is not covered: nc.JetStream() on the harness
// connection does not fail.
//
// sqlmock does not report unexpected calls: an unexpected Exec only returns an
// error to the caller, and the consumers ignore that error. So the stale-write
// tests register a catch-all Exec as the last expectation, after checking that
// everything before it was met, and fail if anything reached it.

// ssAllMet fails the test unless every expectation so far was met, so that a
// catch-all registered next is the only one a later write can match.
func (f *srFixture) ssAllMet(t *testing.T, when string) {
	t.Helper()
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("db %s: %v", when, err)
	}
}

// ssCatchAllSQL is the catch-all's SQL pattern: any statement.
const ssCatchAllSQL = "."

// ssCatchAnyExec registers, as the next expectation, an Exec of any SQL with
// any number of arguments (no WithArgs means sqlmock checks none). It has to be
// this broad. An Exec whose SQL or argument count doesn't match the next
// expectation is not counted against it: sqlmock just returns an error, which
// the consumers drop. A catch-all narrower than the ending write's exact SQL
// and argument count would then stay unmet and the test would pass vacuously.
func (f *srFixture) ssCatchAnyExec() {
	f.mock.ExpectExec(ssCatchAllSQL).WillReturnResult(sqlmock.NewResult(0, 1))
}

// ssNoLateExec fails the test if any Exec reached the catch-all from
// ssCatchAnyExec, i.e. the late bridge wrote to the database at all. It also
// fails if the catch-all is not the one unmet expectation: in order, an earlier
// one left unmet (the data-connection lookup) would keep any write from
// reaching it.
func (f *srFixture) ssNoLateExec(t *testing.T, what string) {
	t.Helper()
	err := f.mock.ExpectationsWereMet()
	switch {
	case err == nil:
		t.Errorf(`%s wrote to the database over B's "running" (an Exec reached the catch-all), want no write`, what)
	case !strings.Contains(err.Error(), "matches sql: '"+ssCatchAllSQL+"'"):
		t.Errorf("%s left an earlier expectation unmet, so the catch-all could not see a write: %v", what, err)
	}
}

var ssDCCols = []string{"allowed_fields", "denied_fields", "shared_connection_ids"}

// ssFailures are the bridge setup failures the fixtures can drive, each with
// the data-connection lookup the failing bridge makes first.
var ssFailures = []struct {
	name   string
	cfg    TenantConsumerConfig
	expect func(sqlmock.Sqlmock)
}{
	{
		name: "no approved data connection",
		cfg:  TenantConsumerConfig{SourceTenantID: srSourceTenant, SourceConnectionID: srSourceConn},
		expect: func(m sqlmock.Sqlmock) {
			m.ExpectQuery("FROM tenant_data_connections").WithArgs(srTenant, srSourceTenant).
				WillReturnError(sqlmock.ErrCancelled)
		},
	},
	{
		name: "source connection not shared",
		cfg:  TenantConsumerConfig{SourceTenantID: srSourceTenant, SourceConnectionID: srSourceConn},
		expect: func(m sqlmock.Sqlmock) {
			m.ExpectQuery("FROM tenant_data_connections").WithArgs(srTenant, srSourceTenant).
				WillReturnRows(sqlmock.NewRows(ssDCCols).AddRow(nil, nil, []byte(`["conn-other"]`)))
		},
	},
	{
		// ">" mid-subject is not a valid filter, so creating the consumer fails.
		name: "subscribe fails",
		cfg:  TenantConsumerConfig{SourceTenantID: "bad.>", SourceConnectionID: srSourceConn},
		expect: func(m sqlmock.Sqlmock) {
			m.ExpectQuery("FROM tenant_data_connections").WithArgs(srTenant, "bad.>").
				WillReturnRows(sqlmock.NewRows(ssDCCols).AddRow(nil, nil, nil))
		},
	},
}

// Criterion 1: A's bridge failing after stop + start B writes no status.
func TestStaleStatus_OldBridgeErrorWritesNoStatus(t *testing.T) {
	for _, tc := range ssFailures {
		t.Run(tc.name, func(t *testing.T) {
			f := newSRFixture(t)
			var stopped atomic.Int32
			f.c.logger = slog.New(peStopLog{n: &stopped})

			// Start A and wait until its bridge is up.
			f.expectLookup(nil)
			f.expectRunning()
			f.expectBridge()
			f.c.startConnection(context.Background(), srConn, srTenant)
			a := f.entry()
			if a == nil {
				t.Fatal("start A left no entry")
			}
			harness.Eventually(t, srWait, "bridge A to trigger its source", func() bool { return f.triggers.Load() >= 1 })

			// Stop A and wait until its bridge goroutine has returned.
			f.expectStatus("stopped")
			f.c.handleStopCommand(srStopMsg())
			harness.Eventually(t, srWait, "bridge A to stop", func() bool { return stopped.Load() >= 2 })

			// Start B and wait until its bridge is up.
			f.expectLookup(nil)
			f.expectRunning()
			f.expectBridge()
			f.c.startConnection(context.Background(), srConn, srTenant)
			b := f.entry()
			if b == nil || b == a {
				t.Fatalf("start B entry = %p, want a new entry (A was %p)", b, a)
			}
			harness.Eventually(t, srWait, "bridge B to trigger its source", func() bool { return f.triggers.Load() >= 2 })

			// The last write is B's "running"; anything A writes now lands in
			// the catch-all after its data-connection lookup.
			f.ssAllMet(t, `after start B wrote "running"`)
			tc.expect(f.mock)
			f.ssCatchAnyExec()

			ctx, cancel := context.WithCancel(context.Background())
			cancel() // A's context, cancelled by the stop
			cfg := tc.cfg
			f.c.runBridge(ctx, a, srConn, srTenant, &cfg)

			f.ssNoLateExec(t, "A's late bridge error")
			if got := f.entry(); got != b {
				t.Errorf("entry after A's bridge error = %p, want B's %p", got, b)
			}
		})
	}
}

// Criterion 2: a bridge failing with no newer start writes "error" and drops
// its own entry.
func TestStaleStatus_OwnBridgeErrorWritesError(t *testing.T) {
	for _, tc := range ssFailures {
		t.Run(tc.name, func(t *testing.T) {
			f := newSRFixture(t)

			f.expectLookup(nil) // start A
			f.expectRunning()
			f.expectBridge()
			f.c.startConnection(context.Background(), srConn, srTenant)
			a := f.entry()
			if a == nil {
				t.Fatal("start A left no entry")
			}
			// A's own bridge from the start keeps running until cancelled; once
			// the entry is gone, Stop in the fixture cleanup no longer reaches it.
			t.Cleanup(a.cancel)
			harness.Eventually(t, srWait, "bridge A to trigger its source", func() bool { return f.triggers.Load() >= 1 })

			f.ssAllMet(t, `after start A wrote "running"`)
			tc.expect(f.mock)
			f.expectStatus("error") // A's own bridge error

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := tc.cfg
			f.c.runBridge(ctx, a, srConn, srTenant, &cfg)

			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf(`A's own bridge error did not write "error": %v`, err)
			}
			if got := f.entry(); got != nil {
				t.Errorf("entry after A's own bridge error = %p, want none", got)
			}
		})
	}
}
