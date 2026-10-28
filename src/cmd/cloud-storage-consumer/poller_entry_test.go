package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// Tests for #408: a poller or event loop that ends on its own removes its
// connection from the active map (finishConn). After a stop and a new start the
// entry belongs to the new poller, and the old one ending late must leave it
// alone; otherwise the next start finds nothing active and runs a second
// poller beside the new one.
//
// A and B are created through the real startConnection and handleStopCommand.
// Each of A's ending paths is then run late by calling runPoller or
// runEventLoop with A's own entry on the test goroutine, so "A ends after
// stop + start B" is a fixed order rather than a timing guess.

// peFailBucket makes the fixture's store factory fail, for the late call only.
const peFailBucket = "fail-store"

func (f *srFixture) entry() *activeConn {
	f.c.mu.RLock()
	defer f.c.mu.RUnlock()
	return f.c.active[srConn]
}

func (f *srFixture) expectStatus(status string) {
	f.mock.ExpectExec("UPDATE connections SET status").WithArgs(status, srConn, srTenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestPollerEntry_OldEndKeepsNewEntry(t *testing.T) {
	cases := []struct {
		name   string
		cfg    cloudConfig
		event  bool   // run A's event loop instead of its poller
		status string // the terminal status A's ending path writes
	}{
		{
			name:   "poller: store open fails",
			cfg:    cloudConfig{Config: objectstore.Config{Bucket: peFailBucket}, PollIntervalSeconds: 3600},
			status: "error",
		},
		{
			name:   "poller: one-shot poll done",
			cfg:    cloudConfig{Config: objectstore.Config{Bucket: "b"}, PollIntervalSeconds: 0},
			status: "stopped",
		},
		{
			name:   "event loop: store open fails",
			cfg:    cloudConfig{Config: objectstore.Config{Bucket: peFailBucket}, Mode: "event"},
			event:  true,
			status: "error",
		},
		{
			name:   "event loop: event source open fails",
			cfg:    cloudConfig{Config: objectstore.Config{Bucket: "b"}, Mode: "event"},
			event:  true,
			status: "error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSRFixture(t)
			var claims atomic.Int32
			f.c.starts.AfterReserve = func(string) { claims.Add(1) }
			// Set before any start, so no poller reads these while they change.
			open := f.c.newStore
			f.c.newStore = func(ctx context.Context, cfg *objectstore.Config) (objectstore.ObjectStore, error) {
				if cfg.Bucket == peFailBucket {
					return nil, errors.New("store unreachable")
				}
				return open(ctx, cfg)
			}
			f.c.newEvents = func(context.Context, *cloudConfig) (eventSource, error) {
				return nil, errors.New("queue unreachable")
			}

			f.expectLookup(nil) // start A
			f.expectRunning()
			f.expectStatus("stopped") // stop A
			f.expectLookup(nil)       // start B
			f.expectRunning()
			f.expectStatus(tc.status) // A's ending path

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
			cancel() // A's context, cancelled by the stop
			cfg := tc.cfg
			if tc.event {
				f.c.runEventLoop(ctx, a, srConn, srTenant, &cfg)
			} else {
				f.c.runPoller(ctx, a, srConn, srTenant, &cfg)
			}

			if got := f.entry(); got != b {
				t.Errorf("entry after A's end = %p, want B's %p", got, b)
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
