package main

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Tests for #456: after start A, stop, start B, the status in the database is
// B's "running". A's poller ending late must not write its own final status on
// top of it. A poller that ends while its entry is still its own still writes
// its final status and drops its entry.
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

// ssOneShot is an SFTP config whose poller runs once and ends.
func ssOneShot() *SFTPConfig {
	return &SFTPConfig{
		Host: "sftp", Username: "u", Password: "p", RemoteDir: "/in",
		AfterAction: "delete", PollIntervalSeconds: 0, // 0 = run once
	}
}

// Criterion 1: A's one-shot end after stop + start B writes no status.
func TestStaleStatus_OldOneShotEndWritesNoStatus(t *testing.T) {
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
			if tc.cancelled {
				cancel()
			} else {
				defer cancel()
			}
			f.c.runPoller(ctx, a, srConn, srTenant, ssOneShot())

			f.ssNoLateExec(t, "A's late one-shot end")
			if got := f.entry(); got != b {
				t.Errorf("entry after A's one-shot end = %p, want B's %p", got, b)
			}
		})
	}
}

// Criterion 2: a one-shot end with no newer start writes "stopped" and drops
// its own entry.
func TestStaleStatus_OwnOneShotEndWritesStopped(t *testing.T) {
	f := newSRFixture(t)
	f.expectLookup(nil) // start A
	f.expectRunning()

	f.c.startConnection(context.Background(), srConn, srTenant)
	a := f.entry()
	if a == nil {
		t.Fatal("start A left no entry")
	}
	// A's own poller from the start keeps running until cancelled; once the
	// entry is gone, Stop in the fixture cleanup no longer reaches it.
	t.Cleanup(a.cancel)

	f.ssAllMet(t, `after start A wrote "running"`)
	f.expectStatus("stopped") // A's own one-shot end

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.c.runPoller(ctx, a, srConn, srTenant, ssOneShot())

	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf(`A's own one-shot end did not write "stopped": %v`, err)
	}
	if got := f.entry(); got != nil {
		t.Errorf("entry after A's own one-shot end = %p, want none", got)
	}
}
