package main

import (
	"context"
	"database/sql/driver"
	"log/slog"
	"sync"
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
// tests register a catch-all status write as the last expectation, after
// checking that everything before it was met, and fail if anything reached it.

// ssStatusArg matches any status value and records it, so a test can say which
// status a stale bridge wrote.
type ssStatusArg struct {
	mu  sync.Mutex
	got []string
}

func (a *ssStatusArg) Match(v driver.Value) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, _ := v.(string)
	a.got = append(a.got, s)
	return true
}

func (a *ssStatusArg) writes() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.got...)
}

// ssAllMet fails the test unless every expectation so far was met, so that a
// catch-all registered next is the only one a later status write can match.
func (f *srFixture) ssAllMet(t *testing.T, when string) {
	t.Helper()
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("db %s: %v", when, err)
	}
}

// ssCatchStatusWrite registers a status write of any value for any connection
// as the next expectation and returns the recorder for its status argument.
func (f *srFixture) ssCatchStatusWrite() *ssStatusArg {
	catch := &ssStatusArg{}
	f.mock.ExpectExec("UPDATE connections SET status").
		WithArgs(catch, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	return catch
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
			catch := f.ssCatchStatusWrite()

			ctx, cancel := context.WithCancel(context.Background())
			cancel() // A's context, cancelled by the stop
			cfg := tc.cfg
			f.c.runBridge(ctx, a, srConn, srTenant, &cfg)

			if got := catch.writes(); len(got) != 0 {
				t.Errorf(`A's late bridge error wrote status %q over B's "running", want no write`, got)
			}
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
