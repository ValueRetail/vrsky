package main

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/doneset"
)

// Tests for #640: a file whose delete or move failed is done, so it stays in
// the folder. The poller retries the after-action for done names still listed,
// without publishing again, with a backoff per name.

const arInterval = 30 * time.Second

// flakySFTP fails the first failRemove removes and the first failRename
// renames, then behaves as the fake. It records when each attempt was made, by
// the rig's clock.
type flakySFTP struct {
	*fakeSFTP
	clock func() time.Time

	mu         sync.Mutex
	failRemove int
	failRename int
	removeAt   []time.Time
	renameAt   []time.Time
}

func (f *flakySFTP) Remove(p string) error {
	f.mu.Lock()
	f.removeAt = append(f.removeAt, f.clock())
	fail := f.failRemove != 0
	if f.failRemove > 0 {
		f.failRemove--
	}
	f.mu.Unlock()
	if fail {
		return errors.New("remove refused")
	}
	return f.fakeSFTP.Remove(p)
}

func (f *flakySFTP) Rename(oldPath, newPath string) error {
	f.mu.Lock()
	f.renameAt = append(f.renameAt, f.clock())
	fail := f.failRename != 0
	if f.failRename > 0 {
		f.failRename--
	}
	f.mu.Unlock()
	if fail {
		return errors.New("rename refused")
	}
	return f.fakeSFTP.Rename(oldPath, newPath)
}

func (f *flakySFTP) attempts(kind string) []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if kind == "move" {
		return append([]time.Time(nil), f.renameAt...)
	}
	return append([]time.Time(nil), f.removeAt...)
}

func (f *flakySFTP) has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fakeSFTP.mu.Lock()
	defer f.fakeSFTP.mu.Unlock()
	_, ok := f.files[name]
	return ok
}

func newARRig(t *testing.T, action string, fake *flakySFTP) *pdRig {
	r := newPDRig(t, newPDStore(), fake.fakeSFTP)
	r.cfg.AfterAction = action
	r.cfg.MoveDir = "/done"
	r.cfg.PollIntervalSeconds = int(arInterval / time.Second)
	r.clock = pdT0.Add(time.Hour)
	fake.clock = func() time.Time { return r.clock }
	r.fake = fake.fakeSFTP
	return r
}

// rigDial points the rig's consumer at conn instead of the plain fake. Called
// after boot.
func rigDial(r *pdRig, conn sftpConn) {
	r.c.dial = func(*SFTPConfig) (sftpConn, error) { return conn, nil }
}

// gaps are the times between consecutive attempts.
func gaps(at []time.Time) []time.Duration {
	var out []time.Duration
	for i := 1; i < len(at); i++ {
		out = append(out, at[i].Sub(at[i-1]))
	}
	return out
}

func TestAfterRetry_FailsNTimesThenWorks(t *testing.T) {
	cases := []struct {
		name     string
		action   string
		failures int
	}{
		{"delete fails once", "delete", 1},
		{"delete fails three times", "delete", 3},
		{"move fails once", "move", 1},
		{"move fails three times", "move", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fk := &flakySFTP{fakeSFTP: &fakeSFTP{}}
			if tc.action == "delete" {
				fk.failRemove = tc.failures
			} else {
				fk.failRename = tc.failures
			}
			pdPut(fk.fakeSFTP, "a.json", `{"a":1}`, 0)
			r := newARRig(t, tc.action, fk)
			r.boot()
			rigDial(r, fk)

			// An hour of polls, every 30 s once the file has settled.
			r.polls(2 + 120)

			requireCounts(t, r.log, "after the retries", map[string]int{"a.json": 1})
			if fk.has("a.json") {
				t.Fatalf("a.json is still in the folder after %d failed %ss and an hour of polls", tc.failures, tc.action)
			}
			at := fk.attempts(tc.action)
			if len(at) != tc.failures+1 {
				t.Fatalf("%d %s attempts, want %d (the failures and the one that worked)", len(at), tc.action, tc.failures+1)
			}
			// Each retry waits at least its backoff step and not much more
			// than a poll beyond it: 1 min, 2 min, 4 min...
			for i, g := range gaps(at) {
				want := doneset.RetryBase << i
				if g < want || g > want+arInterval+r.c.settleGap(&r.cfg) {
					t.Errorf("gap before attempt %d was %s, want %s plus at most a poll", i+2, g, want)
				}
			}
			if tc.action == "move" {
				if got := fk.moved["/in/a.json"]; got != "/done/a.json" {
					t.Errorf("moved to %q, want /done/a.json", got)
				}
			}
		})
	}
}

// A name that never works is tried on a backoff that stops growing at
// RetryMax, not at every poll, and never published again.
func TestAfterRetry_BackoffIsBounded(t *testing.T) {
	fk := &flakySFTP{fakeSFTP: &fakeSFTP{}, failRemove: -1}
	pdPut(fk.fakeSFTP, "a.json", `{"a":1}`, 0)
	r := newARRig(t, "delete", fk)
	r.boot()
	rigDial(r, fk)

	r.polls(2 + 24*120) // 24 hours
	requireCounts(t, r.log, "a day of failing deletes", map[string]int{"a.json": 1})
	at := fk.attempts("delete")
	// 1, 2, 4, 8, 16, 32 min, then hourly: about 6 + 23 tries, not 2 880.
	if len(at) < 20 || len(at) > 32 {
		t.Fatalf("%d delete attempts in 24 hours, want about 29", len(at))
	}
	for i, g := range gaps(at) {
		if g > doneset.RetryMax+arInterval+r.c.settleGap(&r.cfg) {
			t.Errorf("gap before attempt %d was %s, more than RetryMax plus a poll", i+2, g)
		}
	}
	if !fk.has("a.json") {
		t.Fatal("the file went although every delete failed")
	}
}

// One Warn per name per backoff step, not one per poll.
func TestAfterRetry_LogsOncePerAttempt(t *testing.T) {
	fk := &flakySFTP{fakeSFTP: &fakeSFTP{}, failRemove: -1}
	pdPut(fk.fakeSFTP, "a.json", `{"a":1}`, 0)
	r := newARRig(t, "delete", fk)
	r.boot()
	rigDial(r, fk)
	logs := pdWarnLogger(r)

	r.polls(2 + 120) // an hour
	n := len(fk.attempts("delete"))
	if got := strings.Count(logs.String(), "level=WARN msg=\"after-action failed"); got != n {
		t.Fatalf("%d after-action warnings for %d attempts, want one each", got, n)
	}
}

// after_action none leaves everything alone: no remove, no rename, no retry.
func TestAfterRetry_NoneIsUntouched(t *testing.T) {
	fk := &flakySFTP{fakeSFTP: &fakeSFTP{}}
	pdPut(fk.fakeSFTP, "a.json", `{"a":1}`, 0)
	r := newARRig(t, "none", fk)
	r.boot()
	rigDial(r, fk)

	r.polls(2 + 240)
	requireCounts(t, r.log, "after_action none", map[string]int{"a.json": 1})
	if n := len(fk.attempts("delete")) + len(fk.attempts("move")); n != 0 {
		t.Fatalf("%d after-actions with after_action none", n)
	}
	if !fk.has("a.json") {
		t.Fatal("a.json was removed with after_action none")
	}
}

// A name someone else removed is not tried again, and the same name added
// later is a new file: published, and its after-action is its own.
func TestAfterRetry_RemovedByOthersStopsRetrying(t *testing.T) {
	fk := &flakySFTP{fakeSFTP: &fakeSFTP{}, failRemove: -1}
	pdPut(fk.fakeSFTP, "a.json", `{"a":1}`, 0)
	r := newARRig(t, "delete", fk)
	r.boot()
	rigDial(r, fk)

	r.polls(2 + 8) // the first try and a retry or two fail
	before := len(fk.attempts("delete"))
	if before < 2 {
		t.Fatalf("%d attempts so far, want at least 2 before the file goes", before)
	}
	pdDel(fk.fakeSFTP, "a.json")
	r.polls(240)
	if after := len(fk.attempts("delete")); after != before {
		t.Fatalf("%d delete attempts after the file was removed by someone else, had %d", after, before)
	}
	if r.done.Retrying() != 0 {
		t.Fatalf("%d names still waiting for a retry after their file went", r.done.Retrying())
	}

	// Same name again: a new file, published again, with a fresh backoff.
	fk.mu.Lock()
	fk.failRemove = 0
	fk.mu.Unlock()
	pdPut(fk.fakeSFTP, "a.json", `{"a":2}`, 5)
	r.polls(2)
	requireCounts(t, r.log, "the name added again", map[string]int{"a.json": 2})
	if fk.has("a.json") {
		t.Fatal("the file added again was not deleted")
	}
}

// A restart forgets the backoff: files left from before are retried on the
// first poll, once, and not published again.
func TestAfterRetry_RestartRetriesLeftovers(t *testing.T) {
	fk := &flakySFTP{fakeSFTP: &fakeSFTP{}, failRemove: -1}
	pdPut(fk.fakeSFTP, "a.json", `{"a":1}`, 0)
	r := newARRig(t, "delete", fk)
	r.boot()
	rigDial(r, fk)
	r.polls(2 + 4)
	if !fk.has("a.json") {
		t.Fatal("the file went although every delete failed")
	}

	fk.mu.Lock()
	fk.failRemove = 0 // the server is fixed
	fk.mu.Unlock()
	r.boot()
	rigDial(r, fk)
	r.poll()
	if fk.has("a.json") {
		t.Fatal("the leftover was not removed by the first poll after the restart")
	}
	requireCounts(t, r.log, "after the restart", map[string]int{"a.json": 1})
}

// Switching after_action from none to delete clears what the earlier runs left,
// over several polls (MaxRetriesPerPoll at a time), and only files the pattern
// selects.
func TestAfterRetry_LeftoversAreBoundedAndPatternBound(t *testing.T) {
	const files = doneset.MaxRetriesPerPoll + 5
	fk := &flakySFTP{fakeSFTP: &fakeSFTP{}}
	for i := 0; i < files; i++ {
		pdPut(fk.fakeSFTP, "f"+strconv.Itoa(100+i)+".csv", "x", 0)
	}
	pdPut(fk.fakeSFTP, "keep.json", `{"k":1}`, 0)
	r := newARRig(t, "none", fk)
	r.boot()
	rigDial(r, fk)
	r.polls(2)
	if got := len(r.log.counts()); got != files+1 {
		t.Fatalf("%d files published, want %d", got, files+1)
	}

	// Now delete, for csv files only. keep.json is done but not selected.
	r.cfg.AfterAction = "delete"
	r.cfg.FilePattern = "*.csv"
	r.boot()
	rigDial(r, fk)
	r.poll()
	if n := len(fk.attempts("delete")); n != doneset.MaxRetriesPerPoll {
		t.Fatalf("%d deletes in the first poll, want %d", n, doneset.MaxRetriesPerPoll)
	}
	r.poll()
	if n := len(fk.attempts("delete")); n != files {
		t.Fatalf("%d deletes after the second poll, want %d", n, files)
	}
	if !fk.has("keep.json") {
		t.Fatal("keep.json does not match file_pattern but was deleted")
	}
	for _, n := range r.log.counts() {
		if n != 1 {
			t.Fatalf("a file was published %d times, want once: %s", n, pdFmt(r.log.counts()))
		}
	}
}
