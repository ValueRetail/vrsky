package main

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #408: a bridge that fails on its own removes its connection from
// activeBridges. After a stop and a new start the entry belongs to the new
// bridge, and the old one failing late must leave it alone; otherwise the next
// start finds nothing active and runs a second bridge beside the new one.
//
// A and B are created through the real startConnection and handleStopCommand,
// and their real bridges run against the embedded JetStream. Once A's bridge
// has stopped and B's is up, one of runBridge's error paths is run late with
// A's own entry on the test goroutine, so "A ends after stop + start B" is a
// fixed order rather than a timing guess.

// peStopLog counts "Bridge stopped" records: one from the stop handler and
// one from the bridge goroutine once it has unsubscribed and returned.
type peStopLog struct{ n *atomic.Int32 }

func (h peStopLog) Enabled(context.Context, slog.Level) bool { return true }
func (h peStopLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "Bridge stopped" {
		h.n.Add(1)
	}
	return nil
}
func (h peStopLog) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h peStopLog) WithGroup(string) slog.Handler      { return h }

func (f *srFixture) entry() *activeConn {
	f.c.mu.RLock()
	defer f.c.mu.RUnlock()
	return f.c.activeBridges[srConn]
}

func (f *srFixture) expectStatus(status string) {
	f.mock.ExpectExec("UPDATE connections SET status").WithArgs(status, srConn, srTenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestPollerEntry_OldBridgeErrorKeepsNewEntry(t *testing.T) {
	dcCols := []string{"allowed_fields", "denied_fields", "shared_connection_ids"}
	cases := []struct {
		name   string
		cfg    TenantConsumerConfig
		expect func(sqlmock.Sqlmock) // the late bridge's data-connection lookup
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
					WillReturnRows(sqlmock.NewRows(dcCols).AddRow(nil, nil, []byte(`["conn-other"]`)))
			},
		},
		{
			// ">" mid-subject is not a valid filter, so creating the consumer fails.
			name: "subscribe fails",
			cfg:  TenantConsumerConfig{SourceTenantID: "bad.>", SourceConnectionID: srSourceConn},
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery("FROM tenant_data_connections").WithArgs(srTenant, "bad.>").
					WillReturnRows(sqlmock.NewRows(dcCols).AddRow(nil, nil, nil))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSRFixture(t)
			var claims, stopped atomic.Int32
			f.c.starts.AfterReserve = func(string) { claims.Add(1) }
			f.c.logger = slog.New(peStopLog{n: &stopped})

			// Start A and wait until its bridge is up (it triggers the source once).
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
			if f.entry() != nil {
				t.Fatal("stop left A's entry in place")
			}
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

			// A's bridge fails late, holding A's entry.
			tc.expect(f.mock)
			f.expectStatus("error")
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // A's context, cancelled by the stop
			cfg := tc.cfg
			f.c.runBridge(ctx, a, srConn, srTenant, &cfg)

			if got := f.entry(); got != b {
				t.Errorf("entry after A's bridge error = %p, want B's %p", got, b)
			}

			// A further start must be refused: no claim, no lookup, no bridge.
			f.c.startConnection(context.Background(), srConn, srTenant)
			if n := claims.Load(); n != 2 {
				t.Errorf("starts that claimed the connection = %d, want 2 (A and B)", n)
			}
			if got := f.entry(); got != b {
				t.Errorf("entry after a further start = %p, want B's %p", got, b)
			}
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db: %v", err)
			}
			if n := f.triggers.Load(); n != 2 {
				t.Errorf("source triggers = %d, want 2 (bridges A and B only)", n)
			}
		})
	}
}
