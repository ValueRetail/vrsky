package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// Tests for #456: after start A, stop, start B, the status in the database is
// B's "running". A's poller or event loop ending late must not write its own
// final status on top of it. One that ends while its entry is still its own
// still writes its final status and drops its entry.
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
// ssCatchAnyExec, i.e. the late ending wrote to the database at all. It also
// fails if the catch-all is not the one unmet expectation: in order, an earlier
// one left unmet would keep any write from reaching it.
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

// ssFailOpeners makes the store factory fail for peFailBucket and the event
// source factory fail always. Set before any start, so no poller reads these
// while they change.
func (f *srFixture) ssFailOpeners() {
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
}

// ssEndings are the ways a poller or event loop ends on its own, with the
// status it writes when its entry is still its own.
var ssEndings = []struct {
	name   string
	cfg    cloudConfig
	event  bool // run the event loop instead of the poller
	status string
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

// ssEnd runs one ending path with own as the poller's entry.
func (f *srFixture) ssEnd(ctx context.Context, own *activeConn, cfg cloudConfig, event bool) {
	if event {
		f.c.runEventLoop(ctx, own, srConn, srTenant, &cfg)
		return
	}
	f.c.runPoller(ctx, own, srConn, srTenant, &cfg)
}

// Criterion 1: A's end after stop + start B writes no status.
func TestStaleStatus_OldEndWritesNoStatus(t *testing.T) {
	for _, tc := range ssEndings {
		for _, cancelled := range []bool{true, false} {
			name := tc.name + ", context live"
			if cancelled {
				name = tc.name + ", context cancelled by the stop"
			}
			t.Run(name, func(t *testing.T) {
				f := newSRFixture(t)
				f.ssFailOpeners()
				f.expectLookup(nil) // start A
				f.expectRunning()
				f.expectStatus("stopped") // stop A
				f.expectLookup(nil)       // start B
				f.expectRunning()

				f.c.startConnection(context.Background(), srConn, srTenant)
				a := f.entry()
				if a == nil {
					t.Fatal("start A left no entry")
				}
				f.c.handleStopCommand(srStopMsg())
				f.c.startConnection(context.Background(), srConn, srTenant)
				b := f.entry()
				if b == nil || b == a {
					t.Fatalf("start B entry = %p, want a new entry (A was %p)", b, a)
				}

				// The last write is B's "running"; anything A writes now lands here.
				f.ssAllMet(t, `after start B wrote "running"`)
				f.ssCatchAnyExec()

				ctx, cancel := context.WithCancel(context.Background())
				if cancelled {
					cancel()
				} else {
					defer cancel()
				}
				f.ssEnd(ctx, a, tc.cfg, tc.event)

				f.ssNoLateExec(t, "A's late end (status "+tc.status+")")
				if got := f.entry(); got != b {
					t.Errorf("entry after A's end = %p, want B's %p", got, b)
				}
			})
		}
	}
}

// Criterion 2: an end with no newer start writes its own final status and
// drops its own entry.
func TestStaleStatus_OwnEndWritesFinalStatus(t *testing.T) {
	for _, tc := range ssEndings {
		t.Run(tc.name, func(t *testing.T) {
			f := newSRFixture(t)
			f.ssFailOpeners()
			f.expectLookup(nil) // start A
			f.expectRunning()

			f.c.startConnection(context.Background(), srConn, srTenant)
			a := f.entry()
			if a == nil {
				t.Fatal("start A left no entry")
			}
			// A's own poller from the start keeps running until cancelled; once
			// the entry is gone, Stop in the fixture cleanup no longer reaches it.
			t.Cleanup(a.cancel)

			f.ssAllMet(t, `after start A wrote "running"`)
			f.expectStatus(tc.status) // A's own end

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.ssEnd(ctx, a, tc.cfg, tc.event)

			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("A's own end did not write %q: %v", tc.status, err)
			}
			if got := f.entry(); got != nil {
				t.Errorf("entry after A's own end = %p, want none", got)
			}
		})
	}
}
