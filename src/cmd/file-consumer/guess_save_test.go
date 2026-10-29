package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Tests for #536: a start that could not read its checkpoint takes every file
// in the folder as done (a guess), and that guess must never be written over
// the saved set while it can't be read. Once it can be read, the guess is
// corrected from it before anything is written. See
// plans/issue-536-guess-save.md, acceptance criteria 1 to 5 (6 is in
// guess_save_pg_test.go).
//
// Pods are the adoptPod of adopt_stamp_test.go, each on its own fake clock and
// driven by hand. Reads of both checkpoint rows fail while failReads is on;
// writes keep working, so a write while reads fail is what the bug does.

// failReads makes every read of the file node's two checkpoint rows fail, or
// work again.
func failReads(st *adoptStore, fail bool) {
	st.setFailing(knownNode, fail)
	st.setFailing(changesNode, fail)
}

// rowStamp is what a start reading the store would find for name: the
// snapshot, with the changes row on it applied when its base is the
// snapshot's gen. named is false when the name is not saved as done; stamped
// is false when it is saved with no stamp.
func rowStamp(t *testing.T, store checkpoint.Store, tenant, conn, name string) (st wireStamp, named, stamped bool) {
	t.Helper()
	raw := rawRow(t, store, tenant, conn, knownNode)
	if raw == nil {
		return wireStamp{}, false, false
	}
	snap := decodeSnapshot(t, raw)
	named = containsName(snap.Known, name)
	st, stamped = snap.Files[name]
	named = named || stamped
	if snap.Gen == "" {
		return st, named, stamped
	}
	chRaw := rawRow(t, store, tenant, conn, changesNode)
	if chRaw == nil {
		return st, named, stamped
	}
	ch := decodeChanges(t, chRaw)
	if ch.Base != snap.Gen || ch.WatchDir != snap.WatchDir {
		return st, named, stamped
	}
	if f, ok := ch.Files[name]; ok {
		return f, true, true
	}
	if containsName(ch.Known, name) {
		return wireStamp{}, true, false
	}
	if containsName(ch.Gone, name) {
		return wireStamp{}, false, false
	}
	return st, named, stamped
}

func toWire(st fileStamp) wireStamp {
	return wireStamp{Size: st.size, MTime: st.mtimeSec, MTimeNsec: st.mtimeNsec}
}

// rowsNow returns both checkpoint rows as they are stored.
func rowsNow(t *testing.T, st *adoptStore, tenant, conn string) (snap, changes []byte) {
	t.Helper()
	return rawRow(t, st.Store, tenant, conn, knownNode), rawRow(t, st.Store, tenant, conn, changesNode)
}

// startGuessing starts a pod whose read of the checkpoint fails, so it takes
// every file in the folder as done. Reads stay failing.
func startGuessing(t *testing.T, dir string, st *adoptStore, tenant, conn string) *adoptPod {
	t.Helper()
	failReads(st, true)
	return startAdoptPod(t, dir, st, tenant, conn)
}

// --- AC 1: nothing is written while reads fail ------------------------------

var (
	yName    = "y.csv"
	yContent = []byte("id\ny landed while stopped\n")
)

func TestGuessSave_NothingWrittenWhileReadsFail(t *testing.T) {
	nName := "n.json"
	publishN := func(t *testing.T, a *adoptPod, dir string) {
		t.Helper()
		writeAt(t, dir, nName, nJSON, adoptMTime.Add(5*time.Hour))
		a.polls(3)
		if got := len(a.probe.forName(nName)); got != 1 {
			t.Fatalf("premise: n.json published %d times while reads fail, want 1 (%s)", got, describe(a.probe.published()))
		}
	}
	flush := func(a *adoptPod) { a.c.flushKnown(context.Background(), a.ac, a.c.logger) }
	cases := []struct {
		name string
		// act runs while reads fail.
		act func(t *testing.T, a *adoptPod, dir string)
		// thenSave is what makes the next save once reads work; nil when
		// the watcher is gone or nothing is due.
		thenSave func(a *adoptPod)
		// wantN: n.json was published and must be in the snapshot once
		// it is written.
		wantN bool
	}{
		{
			name:     "after a publish",
			act:      publishN,
			thenSave: func(a *adoptPod) { a.poll() },
			wantN:    true,
		},
		{
			name: "after a deletion-only poll",
			act: func(t *testing.T, a *adoptPod, dir string) {
				if err := os.Remove(filepath.Join(dir, fillerName(0))); err != nil {
					t.Fatal(err)
				}
				a.poll()
				if a.ac.knownFiles[fillerName(0)] {
					t.Fatal("premise: the deletion was not seen")
				}
			},
			thenSave: func(a *adoptPod) { a.poll() },
		},
		{
			name: "at the stop flush, after a publish",
			act: func(t *testing.T, a *adoptPod, dir string) {
				publishN(t, a, dir)
				flush(a)
			},
			thenSave: flush,
			wantN:    true,
		},
		{
			name: "at the stop flush, nothing changed",
			act: func(_ *testing.T, a *adoptPod, _ string) {
				a.polls(2)
				flush(a)
			},
		},
		{
			name: "stopping the watcher, after a publish",
			act: func(t *testing.T, a *adoptPod, dir string) {
				publishN(t, a, dir)
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() {
					defer close(done)
					a.c.watchDirectory(ctx, a.ac)
				}()
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("watcher did not stop")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFillers(t, dir, 4)
			put(t, dir, xName, xV0)
			st := newAdoptStore(newLockedStore())
			// A previous run saved the folder as it was, then stopped.
			prev := startAdoptPod(t, dir, st, knownTenant, knownConn)
			prev.c.flushKnown(context.Background(), prev.ac, prev.c.logger)
			// y.csv lands while nobody watches.
			writeAt(t, dir, yName, yContent, adoptMTime.Add(3*time.Hour))
			snapBefore, chBefore := rowsNow(t, st, knownTenant, knownConn)
			if snapBefore == nil {
				t.Fatal("premise: no snapshot saved by the previous run")
			}
			st.take()

			a := startGuessing(t, dir, st, knownTenant, knownConn)
			if !a.ac.knownFiles[yName] {
				t.Fatal("premise: the start whose read failed should take y.csv as done")
			}
			tc.act(t, a, dir)

			saves, _ := st.take()
			if saves[knownNode] != 0 || saves[changesNode] != 0 {
				t.Fatalf("checkpoint rows written while reads fail: %v, want none", saves)
			}
			snapNow, chNow := rowsNow(t, st, knownTenant, knownConn)
			if !bytes.Equal(snapNow, snapBefore) || !bytes.Equal(chNow, chBefore) {
				t.Fatalf("checkpoint rows changed while reads fail:\nsnapshot  %s\n     was  %s\nchanges   %s\n     was  %s",
					snapNow, snapBefore, chNow, chBefore)
			}
			if got := len(a.probe.forName(yName)); got != 0 {
				t.Fatalf("y.csv published %d times while reads fail, want 0: the guess takes it as done until it can check", got)
			}

			if tc.thenSave == nil {
				return
			}
			failReads(st, false)
			tc.thenSave(a)
			saves, _ = st.take()
			if saves[knownNode] != 1 {
				t.Fatalf("once reads work the save should write one snapshot: saves %v", saves)
			}
			snap := decodeSnapshot(t, rawRow(t, st.Store, knownTenant, knownConn, knownNode))
			if snap.WatchDir != dir {
				t.Fatalf("snapshot for %q, want %q", snap.WatchDir, dir)
			}
			if containsName(snap.Known, yName) {
				t.Fatalf("y.csv saved as done once reads work, but nobody published it: %+v", snap)
			}
			if tc.wantN && !containsName(snap.Known, nName) {
				t.Fatalf("n.json, published while reads failed, missing from the snapshot: %+v", snap)
			}
		})
	}
}

// --- AC 2: the replacement of a file another pod published ------------------

// guessTwoPodCase: B published x.csv at S and saved it, x.csv was replaced
// (S'), A starts with reads failing, then reads work again.
type guessTwoPodCase struct {
	name string
	// bSnapshot: B's save of S is a snapshot (B flushed); otherwise it is a
	// change in the changes row on B's snapshot (#426).
	bSnapshot bool
	// trigger is the change A sees after its start: "delete" (a filler
	// goes, a poll that only saw a deletion) or "publish" (a new file A
	// publishes).
	trigger string
	// failing and working are the pods that poll, in turn, while reads fail
	// and after they work again.
	failing, working string
}

func guessTwoPodCases() []guessTwoPodCase {
	var cases []guessTwoPodCase
	for _, row := range []struct {
		label    string
		snapshot bool
	}{{"S in the changes row", false}, {"S in the snapshot", true}} {
		for _, trigger := range []string{"delete", "publish"} {
			for _, working := range []string{"AABB", "ABAB", "BABA", "BBAA"} {
				cases = append(cases, guessTwoPodCase{
					name:      row.label + ", " + trigger + ", A polls while reads fail, then " + working,
					bSnapshot: row.snapshot, trigger: trigger, failing: "AA", working: working,
				})
			}
		}
		cases = append(cases,
			guessTwoPodCase{
				name:      row.label + ", delete, both poll while reads fail",
				bSnapshot: row.snapshot, trigger: "delete", failing: "ABAB", working: "ABAB",
			},
			guessTwoPodCase{
				name:      row.label + ", delete, nobody polls while reads fail, then ABAB",
				bSnapshot: row.snapshot, trigger: "delete", failing: "", working: "ABAB",
			},
			guessTwoPodCase{
				name:      row.label + ", delete, nobody polls while reads fail, then AABB",
				bSnapshot: row.snapshot, trigger: "delete", failing: "", working: "AABB",
			},
		)
	}
	return cases
}

func runGuessTwoPods(t *testing.T, store checkpoint.Store, tenant, conn string, tc guessTwoPodCase) {
	t.Helper()
	dir := t.TempDir()
	st := newAdoptStore(store)
	writeFillers(t, dir, 4)
	put(t, dir, xName, xV0)

	// B: first start, publishes v1 and saves its stamp S.
	b := startAdoptPod(t, dir, st, tenant, conn)
	put(t, dir, xName, xV1)
	s1 := toWire(stampNow(t, dir, xName))
	b.polls(2)
	if got := published(xName, xV1.content, b); got != 1 {
		t.Fatalf("premise: B published v1 %d times, want 1 (%s)", got, publishedAll(b))
	}
	if tc.bSnapshot {
		b.c.flushKnown(context.Background(), b.ac, b.c.logger)
	}
	if got, _, stamped := rowStamp(t, st.Store, tenant, conn, xName); !stamped || got != s1 {
		t.Fatalf("premise: saved stamp of x.csv = %+v (stamped %v), want B's S %+v", got, stamped, s1)
	}

	// x.csv is replaced (S'), then A starts and can't read the checkpoint.
	put(t, dir, xName, xV2)
	s2 := toWire(stampNow(t, dir, xName))
	a := startGuessing(t, dir, st, tenant, conn)
	if got := toWire(a.ac.knownStamps[xName]); !a.ac.knownFiles[xName] || got != s2 {
		t.Fatalf("premise: A's guess should have x.csv done at S' %+v, has %+v (done %v)", s2, got, a.ac.knownFiles[xName])
	}
	switch tc.trigger {
	case "delete":
		if err := os.Remove(filepath.Join(dir, fillerName(0))); err != nil {
			t.Fatal(err)
		}
	case "publish":
		writeAt(t, dir, "n.json", nJSON, adoptMTime.Add(5*time.Hour))
	}

	pods := map[byte]*adoptPod{'A': a, 'B': b}
	step := 0
	check := func(phase string, who byte) {
		t.Helper()
		step++
		got, _, stamped := rowStamp(t, st.Store, tenant, conn, xName)
		if stamped && got == s2 && published(xName, xV2.content, a, b) == 0 {
			t.Fatalf("%s, step %d (pod %c polled): x.csv saved at S' %+v before anyone published S' (%s)",
				phase, step, who, s2, publishedAll(a, b))
		}
	}
	run := func(phase, order string) {
		for i := 0; i < len(order); i++ {
			pods[order[i]].poll()
			check(phase, order[i])
		}
	}
	run("reads fail", tc.failing)
	failReads(st, false)
	run("reads work", tc.working)
	run("reads work, more polls", "ABABAB")

	if got := published(xName, xV2.content, a, b); got != 1 {
		t.Fatalf("the replacement S' was published %d times by the two pods, want 1 (%s)", got, publishedAll(a, b))
	}
	if got := published(xName, xV1.content, a, b); got != 1 {
		t.Fatalf("the original S was published %d times in all, want 1 (by B, before the replacement) (%s)", got, publishedAll(a, b))
	}
	if got := len(a.probe.forName(xName)) + len(b.probe.forName(xName)); got != 2 {
		t.Fatalf("x.csv published %d times in all, want 2 (S by B, then S') (%s)", got, publishedAll(a, b))
	}
	for i := 0; i < 4; i++ {
		if got := len(a.probe.forName(fillerName(i))) + len(b.probe.forName(fillerName(i))); got != 0 {
			t.Fatalf("%s published %d times; it was never changed (%s)", fillerName(i), got, publishedAll(a, b))
		}
	}
	if got, _, stamped := rowStamp(t, st.Store, tenant, conn, xName); !stamped || got != s2 {
		t.Fatalf("at the end x.csv is saved at %+v (stamped %v), want S' %+v", got, stamped, s2)
	}
}

func TestGuessSave_ReplacementOfAnotherPodsFilePublishedOnce(t *testing.T) {
	for _, tc := range guessTwoPodCases() {
		t.Run(tc.name, func(t *testing.T) {
			runGuessTwoPods(t, newLockedStore(), knownTenant, knownConn, tc)
		})
	}
}

// --- AC 3: a file that landed while stopped ---------------------------------

func TestGuessSave_FileThatLandedWhileStoppedPublishedOnce(t *testing.T) {
	cases := []struct {
		name string
		// withB: B has been watching since before y.csv landed and keeps
		// polling. Otherwise the previous watcher stopped and A is alone.
		withB bool
		// deleteWhileFailing: a filler goes after A's start, so A has a
		// change to save while reads fail.
		deleteWhileFailing bool
		failing, working   string
	}{
		{name: "alone, nothing else changes", failing: "AAA", working: "AAAA"},
		{name: "alone, a deletion while reads fail", deleteWhileFailing: true, failing: "AAA", working: "AAAA"},
		{name: "with B, A polls while reads fail, then AABB", withB: true, deleteWhileFailing: true, failing: "AA", working: "AABB"},
		{name: "with B, A polls while reads fail, then ABAB", withB: true, deleteWhileFailing: true, failing: "AA", working: "ABAB"},
		{name: "with B, A polls while reads fail, then BBAA", withB: true, deleteWhileFailing: true, failing: "AA", working: "BBAA"},
		{name: "with B, B polls while reads fail", withB: true, deleteWhileFailing: true, failing: "BB", working: "AABB"},
		{name: "with B, both poll while reads fail", withB: true, deleteWhileFailing: true, failing: "ABAB", working: "ABAB"},
		{name: "with B, nothing else changes", withB: true, failing: "AA", working: "ABAB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFillers(t, dir, 4)
			st := newAdoptStore(newLockedStore())
			prev := startAdoptPod(t, dir, st, knownTenant, knownConn)
			pods := map[byte]*adoptPod{}
			all := []*adoptPod{}
			if tc.withB {
				pods['B'] = prev
				all = append(all, prev)
			} else {
				prev.c.flushKnown(context.Background(), prev.ac, prev.c.logger)
			}
			writeAt(t, dir, yName, yContent, adoptMTime.Add(3*time.Hour))

			a := startGuessing(t, dir, st, knownTenant, knownConn)
			pods['A'] = a
			all = append(all, a)
			if !a.ac.knownFiles[yName] {
				t.Fatal("premise: the start whose read failed should take y.csv as done")
			}
			if tc.deleteWhileFailing {
				if err := os.Remove(filepath.Join(dir, fillerName(0))); err != nil {
					t.Fatal(err)
				}
			}
			run := func(order string) {
				for i := 0; i < len(order); i++ {
					pods[order[i]].poll()
				}
			}
			run(tc.failing)
			failReads(st, false)
			run(tc.working)
			for i := 0; i < 3; i++ {
				for _, p := range all {
					p.poll()
				}
			}

			if got := published(yName, yContent, all...); got != 1 {
				t.Fatalf("y.csv published %d times, want 1 (%s)", got, publishedAll(all...))
			}
			total := 0
			for _, p := range all {
				total += len(p.probe.published())
			}
			if total != 1 {
				t.Fatalf("%d envelopes in all, want 1: only y.csv (%s)", total, publishedAll(all...))
			}
			if _, named, _ := rowStamp(t, st.Store, knownTenant, knownConn, yName); !named {
				t.Fatal("y.csv published but not saved as done")
			}
		})
	}
}

// --- AC 4: no checkpoint for the folder keeps the first-start rule ----------

func TestGuessSave_NoCheckpointKeepsTheFirstStartRule(t *testing.T) {
	cases := []struct {
		name string
		// seed runs before A's start; nil leaves the store empty.
		seed func(t *testing.T, st checkpoint.Store)
		// trigger runs after A's start, while reads fail: "" (nothing),
		// "delete" or "publish".
		trigger string
	}{
		{name: "no checkpoint rows"},
		{name: "no checkpoint rows, a deletion while reads fail", trigger: "delete"},
		{name: "no checkpoint rows, a publish while reads fail", trigger: "publish"},
		{
			name: "a checkpoint for another folder",
			seed: func(t *testing.T, st checkpoint.Store) {
				seedRow(t, st, knownNode, knownState{WatchDir: "/some/other/folder", Gen: "g-other", Known: []string{"other.csv"}})
				seedRow(t, st, changesNode, changesState{WatchDir: "/some/other/folder", Base: "g-other"})
			},
			trigger: "publish",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFillers(t, dir, 4)
			put(t, dir, xName, xV0)
			writeAt(t, dir, yName, yContent, adoptMTime.Add(3*time.Hour))
			st := newAdoptStore(newLockedStore())
			if tc.seed != nil {
				tc.seed(t, st.Store)
			}
			a := startGuessing(t, dir, st, knownTenant, knownConn)
			present := []string{xName, yName}
			for i := 0; i < 4; i++ {
				present = append(present, fillerName(i))
			}
			wantSaved := append([]string(nil), present...)
			switch tc.trigger {
			case "delete":
				if err := os.Remove(filepath.Join(dir, fillerName(0))); err != nil {
					t.Fatal(err)
				}
				wantSaved = []string{xName, yName, fillerName(1), fillerName(2), fillerName(3)}
			case "publish":
				writeAt(t, dir, "n.json", nJSON, adoptMTime.Add(5*time.Hour))
				wantSaved = append(append([]string(nil), present...), "n.json")
			}
			a.polls(3)
			failReads(st, false)
			a.polls(4)

			for _, name := range present {
				if got := len(a.probe.forName(name)); got != 0 {
					t.Fatalf("%s, present at the start, published %d times; want 0 (%s)", name, got, describe(a.probe.published()))
				}
			}
			wantPublished := 0
			if tc.trigger == "publish" {
				wantPublished = 1
				if got := len(a.probe.forName("n.json")); got != 1 {
					t.Fatalf("n.json published %d times, want 1 (%s)", got, describe(a.probe.published()))
				}
			}
			if got := len(a.probe.published()); got != wantPublished {
				t.Fatalf("%d envelopes, want %d (%s)", got, wantPublished, describe(a.probe.published()))
			}
			snap := decodeSnapshot(t, rawRow(t, st.Store, knownTenant, knownConn, knownNode))
			if snap.WatchDir != dir {
				t.Fatalf("no snapshot for the folder once reads work: %+v", snap)
			}
			for _, name := range wantSaved {
				if !containsName(snap.Known, name) {
					t.Fatalf("%s missing from the snapshot once reads work: %+v", name, snap)
				}
			}

			// A later start that can read publishes none of them either.
			next := startAdoptPod(t, dir, st, knownTenant, knownConn)
			next.polls(4)
			requireNothingPublished(t, "a start after the snapshot", next)
		})
	}
}

// --- AC 5: what A published after its start stays done ---------------------

func TestGuessSave_WhatAPublishedStaysDone(t *testing.T) {
	cases := []struct {
		name string
		// savedX: the previous run saved x.csv (at v0). Otherwise x.csv
		// landed while stopped, after that run saved.
		savedX bool
		// replacedWhileStopped: x.csv was v1 at A's start (the previous run
		// saved v0), and A then sees v2. Otherwise v0 at the start, v1
		// after.
		replacedWhileStopped bool
		// newFile: n.json lands after A's start instead of x.csv changing.
		newFile bool
	}{
		{name: "a new file published while reads fail", savedX: true, newFile: true},
		{name: "a guessed name saved at the same stamp, replaced after the start", savedX: true},
		{name: "a guessed name saved at an older stamp, replaced again after the start", savedX: true, replacedWhileStopped: true},
		{name: "a guessed name the saved set lacks, replaced after the start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFillers(t, dir, 4)
			if tc.savedX {
				put(t, dir, xName, xV0)
			}
			st := newAdoptStore(newLockedStore())
			prev := startAdoptPod(t, dir, st, knownTenant, knownConn)
			prev.c.flushKnown(context.Background(), prev.ac, prev.c.logger)
			atStart, after := xV0, xV1
			if tc.replacedWhileStopped {
				atStart, after = xV1, xV2
			}
			if !tc.savedX || tc.replacedWhileStopped {
				put(t, dir, xName, atStart)
			}

			a := startGuessing(t, dir, st, knownTenant, knownConn)
			name, content := xName, after.content
			if tc.newFile {
				name, content = "n.json", nJSON
				writeAt(t, dir, name, content, adoptMTime.Add(5*time.Hour))
			} else {
				put(t, dir, xName, after)
			}
			pubStamp := toWire(stampNow(t, dir, name))
			a.polls(3)
			if got := len(a.probe.forName(name)); got != 1 {
				t.Fatalf("premise: %s published %d times while reads fail, want 1 (%s)", name, got, describe(a.probe.published()))
			}

			failReads(st, false)
			a.polls(5)
			if got := published(name, content, a); got != 1 {
				t.Fatalf("%s as A published it: %d times in all, want 1: it stays done after the guess is settled (%s)",
					name, got, describe(a.probe.published()))
			}
			if got := len(a.probe.published()); got != 1 {
				t.Fatalf("%d envelopes, want 1 (%s)", got, describe(a.probe.published()))
			}
			if got, _, stamped := rowStamp(t, st.Store, knownTenant, knownConn, name); !stamped || got != pubStamp {
				t.Fatalf("%s saved at %+v (stamped %v), want the published %+v", name, got, stamped, pubStamp)
			}

			// A later start publishes nothing again.
			next := startAdoptPod(t, dir, st, knownTenant, knownConn)
			next.polls(4)
			requireNothingPublished(t, "a start after the settled save", next)
		})
	}
}

// A stop that cuts off the publish of a guessed name puts the guess back
// with the rest of its old entry, so the settle at the stop still corrects
// it. Here B published x.csv at v1, x.csv became v2 before A started (A's
// guess), and v3 after. A's publish of v3 is cut off; if the guessed v2
// stamp were then saved as real, putting v2 back would make the next start
// take it as done, and v2 was never published.
func TestGuessSave_CutPublishKeepsTheGuess(t *testing.T) {
	dir := t.TempDir()
	st := newAdoptStore(newLockedStore())
	b := startAdoptPod(t, dir, st, knownTenant, knownConn)
	put(t, dir, xName, xV1)
	b.polls(2)
	if got := published(xName, xV1.content, b); got != 1 {
		t.Fatalf("premise: B published v1 %d times, want 1", got)
	}

	put(t, dir, xName, xV2)
	a := startGuessing(t, dir, st, knownTenant, knownConn)
	xV3 := version{[]byte("id\nv3, cut off\n"), adoptMTime.Add(3 * time.Hour)}
	put(t, dir, xName, xV3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.c.publish = func(pctx context.Context, _ *envelope.Envelope) error {
		cancel() // the stop lands mid-publish
		return pctx.Err()
	}
	a.clock.advance(watchInterval)
	a.c.pollOnce(ctx, a.ac) // v3 seen once, reads still failing
	failReads(st, false)
	a.clock.advance(watchInterval)
	a.c.pollOnce(ctx, a.ac) // v3 settles; its publish is cut off
	a.c.flushKnown(ctx, a.ac, a.c.logger)

	put(t, dir, xName, xV2)
	next := startAdoptPod(t, dir, st, knownTenant, knownConn)
	next.polls(3)
	if got := published(xName, xV2.content, next); got != 1 {
		t.Fatalf("v2 published %d times after the restart, want 1 (%s)", got, describe(next.probe.published()))
	}
}
