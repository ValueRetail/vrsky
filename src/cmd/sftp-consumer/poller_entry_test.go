package main

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Tests for #408: a poller that ends on its own removes its connection from
// the active map. After a stop and a new start the entry belongs to the new
// poller, and the old one ending late must leave it alone; otherwise the next
// start finds nothing active and runs a second poller beside the new one.
//
// A and B are created through the real startConnection and handleStopCommand.
// A's one-shot ending path is then run late by calling runPoller with A's own
// entry on the test goroutine, so "A ends after stop + start B" is a fixed
// order rather than a timing guess.

func (f *srFixture) entry() *activeConn {
	f.c.mu.RLock()
	defer f.c.mu.RUnlock()
	return f.c.active[srConn]
}

func (f *srFixture) expectStatus(status string) {
	f.mock.ExpectExec("UPDATE connections SET status").WithArgs(status, srConn, srTenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestPollerEntry_OldOneShotEndKeepsNewEntry(t *testing.T) {
	cases := []struct {
		name      string
		cancelled bool // A's context was cancelled by the stop before its one-shot poll
	}{
		{name: "context cancelled by the stop", cancelled: true},
		{name: "context still live", cancelled: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSRFixture(t)
			var claims atomic.Int32
			f.c.starts.AfterReserve = func(string) { claims.Add(1) }

			f.expectLookup(nil) // start A
			f.expectRunning()
			f.expectStatus("stopped") // stop A
			f.expectLookup(nil)       // start B
			f.expectRunning()
			f.expectStatus("stopped") // A's one-shot ending path

			f.c.startConnection(context.Background(), srConn, srTenant)
			a := f.entry()
			if a == nil {
				t.Fatal("start A left no entry")
			}
			f.c.handleStopCommand(srStopMsg())
			if f.entry() != nil {
				t.Fatal("stop left A's entry in place")
			}
			f.c.startConnection(context.Background(), srConn, srTenant)
			b := f.entry()
			if b == nil || b == a {
				t.Fatalf("start B entry = %p, want a new entry (A was %p)", b, a)
			}

			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancelled {
				cancel()
			} else {
				defer cancel()
			}
			cfg := &SFTPConfig{
				Host: "sftp", Username: "u", Password: "p", RemoteDir: "/in",
				AfterAction: "delete", PollIntervalSeconds: 0, // 0 = run once
			}
			f.c.runPoller(ctx, a, srConn, srTenant, cfg)

			if got := f.entry(); got != b {
				t.Errorf("entry after A's one-shot end = %p, want B's %p", got, b)
			}

			// A further start must be refused: no claim, no lookup, no poller.
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
		})
	}
}
