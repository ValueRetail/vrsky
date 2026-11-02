package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/doneset"
)

// Tests for #640: an object whose delete or move failed is done, so it stays
// in the bucket. The poller retries the after-action for done keys still
// listed, without publishing again, with a backoff per key.

const arInterval = 30 * time.Second

// flakyStore fails the first failDelete deletes and the first failCopy copies
// (negative: all of them), then behaves as the fake. It records when each
// attempt was made, by the test's clock.
type flakyStore struct {
	*fakeStore
	clock func() time.Time

	mu         sync.Mutex
	failDelete int
	failCopy   int
	deleteAt   []time.Time
	copyAt     []time.Time
}

func (f *flakyStore) Delete(ctx context.Context, key string) error {
	f.mu.Lock()
	f.deleteAt = append(f.deleteAt, f.clock())
	fail := f.failDelete != 0
	if f.failDelete > 0 {
		f.failDelete--
	}
	f.mu.Unlock()
	if fail {
		return errors.New("delete refused")
	}
	return f.fakeStore.Delete(ctx, key)
}

func (f *flakyStore) Copy(ctx context.Context, src, dst string) error {
	f.mu.Lock()
	f.copyAt = append(f.copyAt, f.clock())
	fail := f.failCopy != 0
	if f.failCopy > 0 {
		f.failCopy--
	}
	f.mu.Unlock()
	if fail {
		return errors.New("copy refused")
	}
	return f.fakeStore.Copy(ctx, src, dst)
}

func (f *flakyStore) attempts(kind string) []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if kind == "copy" {
		return append([]time.Time(nil), f.copyAt...)
	}
	return append([]time.Time(nil), f.deleteAt...)
}

func (f *flakyStore) set(failDelete, failCopy int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failDelete, f.failCopy = failDelete, failCopy
}

func (f *flakyStore) has(key string) bool {
	f.fakeStore.mu.Lock()
	defer f.fakeStore.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

// arRig is a pdRig whose consumer reads the bucket through fk and keeps a
// clock the test moves.
type arRig struct {
	*pdRig
	fk    *flakyStore
	clock time.Time
}

func newARRig(t *testing.T, action string, fk *flakyStore) *arRig {
	r := &arRig{pdRig: newPDRig(t, newPDStore(), fk.fakeStore), fk: fk, clock: pdT0.Add(time.Hour)}
	r.cfg.AfterAction = action
	r.cfg.MovePrefix = "done/"
	r.cfg.PollIntervalSeconds = int(arInterval / time.Second)
	fk.clock = func() time.Time { return r.clock }
	r.boot()
	return r
}

func (r *arRig) boot() {
	r.pdRig.boot()
	r.c.now = func() time.Time { return r.clock }
}

// tick is one poll interval later, then a poll.
func (r *arRig) tick() {
	r.clock = r.clock.Add(arInterval)
	r.c.pollOnce(context.Background(), r.conn, r.tenant, r.fk, &r.cfg, r.done, r.c.logger)
}

func (r *arRig) ticks(n int) {
	for i := 0; i < n; i++ {
		r.tick()
	}
}

func gaps(at []time.Time) []time.Duration {
	var out []time.Duration
	for i := 1; i < len(at); i++ {
		out = append(out, at[i].Sub(at[i-1]))
	}
	return out
}

func TestAfterRetry_FailsNTimesThenWorks(t *testing.T) {
	cases := []struct {
		name       string
		action     string
		failDelete int
		failCopy   int
	}{
		{"delete fails once", "delete", 1, 0},
		{"delete fails three times", "delete", 3, 0},
		{"move: copy fails once", "move", 0, 1},
		{"move: copy fails three times", "move", 0, 3},
		// The copy lands, the delete of the source fails: a partial move. The
		// retry copies over the same destination again, then deletes.
		{"move: delete fails twice after the copy", "move", 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fk := &flakyStore{fakeStore: &fakeStore{}}
			fk.set(tc.failDelete, tc.failCopy)
			pdPut(fk.fakeStore, "in/a.json", `{"a":1}`, 0)
			r := newARRig(t, tc.action, fk)

			r.ticks(120) // an hour

			requireCounts(t, r.log, "after the retries", map[string]int{"in/a.json": 1})
			if fk.has("in/a.json") {
				t.Fatal("in/a.json is still in the bucket after an hour of polls")
			}
			kind := "delete"
			failures := tc.failDelete
			if tc.failCopy > 0 {
				kind, failures = "copy", tc.failCopy
			}
			at := fk.attempts(kind)
			if len(at) != failures+1 {
				t.Fatalf("%d %s attempts, want %d (the failures and the one that worked)", len(at), kind, failures+1)
			}
			for i, g := range gaps(at) {
				want := doneset.RetryBase << i
				if g < want || g > want+arInterval {
					t.Errorf("gap before attempt %d was %s, want %s plus at most a poll", i+2, g, want)
				}
			}
			if tc.action == "move" {
				if !fk.has("done/a.json") {
					t.Error("done/a.json is not in the bucket after the move")
				}
			}
		})
	}
}

// A key that never works is tried on a backoff that stops growing at
// RetryMax, not at every poll, and never published again.
func TestAfterRetry_BackoffIsBounded(t *testing.T) {
	fk := &flakyStore{fakeStore: &fakeStore{}}
	fk.set(-1, 0)
	pdPut(fk.fakeStore, "in/a.json", `{"a":1}`, 0)
	r := newARRig(t, "delete", fk)

	r.ticks(24 * 120) // 24 hours
	requireCounts(t, r.log, "a day of failing deletes", map[string]int{"in/a.json": 1})
	at := fk.attempts("delete")
	// 1, 2, 4, 8, 16, 32 min, then hourly: about 6 + 23 tries, not 2 880.
	if len(at) < 20 || len(at) > 32 {
		t.Fatalf("%d delete attempts in 24 hours, want about 29", len(at))
	}
	for i, g := range gaps(at) {
		if g > doneset.RetryMax+arInterval {
			t.Errorf("gap before attempt %d was %s, more than RetryMax plus a poll", i+2, g)
		}
	}
	if !fk.has("in/a.json") {
		t.Fatal("the object went although every delete failed")
	}
}

// One Warn per key per backoff step, not one per poll.
func TestAfterRetry_LogsOncePerAttempt(t *testing.T) {
	fk := &flakyStore{fakeStore: &fakeStore{}}
	fk.set(-1, 0)
	pdPut(fk.fakeStore, "in/a.json", `{"a":1}`, 0)
	r := newARRig(t, "delete", fk)
	logs := pdWarnLogger(r.pdRig)

	r.ticks(120) // an hour
	n := len(fk.attempts("delete"))
	if got := strings.Count(logs.String(), "level=WARN msg=\"after-action failed"); got != n {
		t.Fatalf("%d after-action warnings for %d attempts, want one each", got, n)
	}
}

// after_action none leaves everything alone: no delete, no copy, no retry.
func TestAfterRetry_NoneIsUntouched(t *testing.T) {
	fk := &flakyStore{fakeStore: &fakeStore{}}
	pdPut(fk.fakeStore, "in/a.json", `{"a":1}`, 0)
	r := newARRig(t, "none", fk)

	r.ticks(240)
	requireCounts(t, r.log, "after_action none", map[string]int{"in/a.json": 1})
	if n := len(fk.attempts("delete")) + len(fk.attempts("copy")); n != 0 {
		t.Fatalf("%d after-actions with after_action none", n)
	}
	if !fk.has("in/a.json") {
		t.Fatal("in/a.json was removed with after_action none")
	}
}

// A key someone else removed is not tried again, and the same key added later
// is a new object: published, and its after-action is its own.
func TestAfterRetry_RemovedByOthersStopsRetrying(t *testing.T) {
	fk := &flakyStore{fakeStore: &fakeStore{}}
	fk.set(-1, 0)
	pdPut(fk.fakeStore, "in/a.json", `{"a":1}`, 0)
	r := newARRig(t, "delete", fk)

	r.ticks(8) // the first try and a retry or two fail
	before := len(fk.attempts("delete"))
	if before < 2 {
		t.Fatalf("%d attempts so far, want at least 2 before the object goes", before)
	}
	pdDel(fk.fakeStore, "in/a.json")
	r.ticks(240)
	if after := len(fk.attempts("delete")); after != before {
		t.Fatalf("%d delete attempts after the object was removed by someone else, had %d", after, before)
	}
	if r.done.Retrying() != 0 {
		t.Fatalf("%d keys still waiting for a retry after their object went", r.done.Retrying())
	}

	fk.set(0, 0)
	pdPut(fk.fakeStore, "in/a.json", `{"a":2}`, 5)
	r.ticks(1)
	requireCounts(t, r.log, "the key added again", map[string]int{"in/a.json": 2})
	if fk.has("in/a.json") {
		t.Fatal("the object added again was not deleted")
	}
}

// A restart forgets the backoff: objects left from before are retried on the
// first poll, once, and not published again.
func TestAfterRetry_RestartRetriesLeftovers(t *testing.T) {
	fk := &flakyStore{fakeStore: &fakeStore{}}
	fk.set(-1, 0)
	pdPut(fk.fakeStore, "in/a.json", `{"a":1}`, 0)
	r := newARRig(t, "delete", fk)
	r.ticks(4)
	if !fk.has("in/a.json") {
		t.Fatal("the object went although every delete failed")
	}

	fk.set(0, 0) // the bucket is fixed
	r.boot()
	r.tick()
	if fk.has("in/a.json") {
		t.Fatal("the leftover was not removed by the first poll after the restart")
	}
	requireCounts(t, r.log, "after the restart", map[string]int{"in/a.json": 1})
}

// Switching after_action from none to delete clears what the earlier runs
// left, over several polls (MaxRetriesPerPoll at a time), and only keys the
// pattern selects.
func TestAfterRetry_LeftoversAreBoundedAndPatternBound(t *testing.T) {
	const objects = doneset.MaxRetriesPerPoll + 5
	fk := &flakyStore{fakeStore: &fakeStore{}}
	for i := 0; i < objects; i++ {
		pdPut(fk.fakeStore, "in/f"+strconv.Itoa(100+i)+".csv", "x", 0)
	}
	pdPut(fk.fakeStore, "in/keep.json", `{"k":1}`, 0)
	r := newARRig(t, "none", fk)
	r.tick()
	if got := len(r.log.counts()); got != objects+1 {
		t.Fatalf("%d objects published, want %d", got, objects+1)
	}

	r.cfg.AfterAction = "delete"
	r.cfg.FilePattern = "*.csv"
	r.boot()
	r.tick()
	if n := len(fk.attempts("delete")); n != doneset.MaxRetriesPerPoll {
		t.Fatalf("%d deletes in the first poll, want %d", n, doneset.MaxRetriesPerPoll)
	}
	r.tick()
	if n := len(fk.attempts("delete")); n != objects {
		t.Fatalf("%d deletes after the second poll, want %d", n, objects)
	}
	if !fk.has("in/keep.json") {
		t.Fatal("in/keep.json does not match file_pattern but was deleted")
	}
	for _, n := range r.log.counts() {
		if n != 1 {
			t.Fatalf("an object was published %d times, want once: %s", n, pdFmt(r.log.counts()))
		}
	}
}
