package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// #537: in a folder with more done files than the per-poll budget
// (knownStatsPerPoll), a poll stats only some of the done files that carry a
// stamp, in turns, so an idle poll of a large folder costs about the budget
// instead of one stat per file. New files, files seen changing, and done
// files with no stamp are still stat'ed every poll, and a replaced done file
// is still published once with its new content, only up to turns polls later.
// See plans/perf-537-fewer-stats.md, acceptance criteria 1 to 5.
//
// Each test drives one watcher by hand on a fake clock, as in known_test.go,
// with the budget set low so the folders stay small, and counts the stats per
// file name through fileConsumer.stat.

// turnsBaseMtime is the mtime of every original file; replacements get later
// ones, all before the fake clock's start.
var turnsBaseMtime = time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)

// turnRig is one watcher on one folder with a stat that counts per name and
// can be made to fail for chosen names.
type turnRig struct {
	t     *testing.T
	dir   string
	c     *fileConsumer
	ac    *ActiveConnection
	clock *fakeClock
	probe *recordingProbe

	mu    sync.Mutex
	stats map[string]int  // stats per base name since the last takeStats
	fail  map[string]bool // names whose stat reports the file missing
}

// turnName is the i-th original file in a rig's folder.
func turnName(i int) string { return fmt.Sprintf("order-2026-10-29-%04d.csv", i) }

func turnContent(name string) []byte { return []byte("original " + name) }

// newTurnRig fills a folder with n files, starts a watcher on it with the
// given budget (0 keeps the default) and no checkpoint store, as a first
// start does, and zeroes the stat counts. Each name in stampless is a done
// file with no stamp after the start: its stat fails while the set is
// loaded (hideFromStat).
func newTurnRig(t *testing.T, n, budget int, stampless ...string) *turnRig {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		writeAt(t, dir, turnName(i), turnContent(turnName(i)), turnsBaseMtime)
	}
	for _, name := range stampless {
		writeAt(t, dir, name, turnContent(name), turnsBaseMtime)
	}
	clock := newFakeClock()
	r := &turnRig{t: t, dir: dir, clock: clock, probe: &recordingProbe{}, stats: map[string]int{}, fail: map[string]bool{}}
	r.c = &fileConsumer{
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		publish:           r.probe.publish,
		inlineMax:         1 << 20,
		pollInterval:      watchInterval,
		now:               clock.now,
		knownStatsPerPoll: budget,
		eventSubs:         make(map[string][]chan FileEvent),
		stat:              r.stat,
	}
	r.ac = &ActiveConnection{ConnectionID: "conn-turns", TenantID: "tenant-turns", WatchDir: dir, NodeID: knownNode, Cancel: func() {}}
	var restore func()
	if len(stampless) > 0 {
		r.c.readDir, restore = hideFromStat(t, dir, stampless...)
	}
	if err := r.c.loadKnown(context.Background(), r.ac, false); err != nil {
		t.Fatalf("loadKnown: %v", err)
	}
	if restore != nil {
		restore()
		r.c.readDir = nil
	}
	if got := len(r.ac.knownFiles); got != n+len(stampless) {
		t.Fatalf("premise: %d done files after the start, want %d", got, n+len(stampless))
	}
	for _, name := range stampless {
		if _, stamped := r.ac.knownStamps[name]; stamped {
			t.Fatalf("premise: %s has a stamp after the start, want none", name)
		}
	}
	r.takeStats()
	return r
}

func (r *turnRig) stat(path string) (os.FileInfo, error) {
	name := filepath.Base(path)
	r.mu.Lock()
	r.stats[name]++
	failing := r.fail[name]
	r.mu.Unlock()
	if failing {
		return nil, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
	}
	return os.Stat(path)
}

func (r *turnRig) setFailing(name string, v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail[name] = v
}

// takeStats returns the stats per name since the last take.
func (r *turnRig) takeStats() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.stats
	r.stats = map[string]int{}
	return out
}

// poll advances the clock one interval, polls once and returns the stats that
// poll made.
func (r *turnRig) poll() map[string]int {
	r.t.Helper()
	r.takeStats()
	r.clock.advance(watchInterval)
	r.c.pollOnce(context.Background(), r.ac)
	return r.takeStats()
}

func (r *turnRig) polls(n int) {
	r.t.Helper()
	for i := 0; i < n; i++ {
		r.poll()
	}
}

// turns is how many polls it takes to look at every done file once: the done
// set split by the budget, rounded up.
func (r *turnRig) turns() int {
	budget := r.c.knownStatsPerPoll
	if budget <= 0 {
		budget = defaultKnownStatsPerPoll
	}
	return max(1, (len(r.ac.knownFiles)+budget-1)/budget)
}

func (r *turnRig) path(name string) string { return filepath.Join(r.dir, name) }

// renameOver replaces name the way most tools do: write a temp file, then
// rename it over the name. No poll runs in between.
func (r *turnRig) renameOver(name string, content []byte, mtime time.Time) {
	r.t.Helper()
	tmp := r.path(name + ".tmp")
	replaceWriteAt(r.t, tmp, content, mtime)
	if err := os.Rename(tmp, r.path(name)); err != nil {
		r.t.Fatalf("rename %s over %s: %v", tmp, name, err)
	}
}

func (r *turnRig) requireCount(step, name string, want int) {
	r.t.Helper()
	if got := len(r.probe.forName(name)); got != want {
		r.t.Fatalf("%s: %q published %d times, want %d (%s)", step, name, got, want, describe(r.probe.published()))
	}
}

// --- 1, 2: idle polls ---------------------------------------------------------

// Criterion 1: with more done files than the budget, every window of turns
// consecutive idle polls stats each done file exactly once, and no poll stats
// anywhere near all of them. Criterion 2: at or under the budget, every file
// is stat'ed every poll, as before #537.
func TestStatTurns_IdlePolls(t *testing.T) {
	cases := []struct {
		name      string
		files     int
		budget    int
		wantTurns int
	}{
		{"one over the budget", 11, 10, 2},
		{"three turns", 50, 20, 3},
		{"just under ten turns", 95, 10, 10},
		{"ten turns", 100, 10, 10},
		{"at the budget", 10, 10, 1},
		{"under the budget", 7, 10, 1},
		{"default budget", 50, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTurnRig(t, tc.files, tc.budget)
			if got := r.turns(); got != tc.wantTurns {
				t.Fatalf("premise: %d turns, want %d", got, tc.wantTurns)
			}
			// Two full rotations, plus a few polls so the windows checked
			// below don't all start on the first turn.
			const extra = 3
			per := make([]map[string]int, 2*tc.wantTurns+extra)
			for i := range per {
				per[i] = r.poll()
			}
			if got := len(r.probe.published()); got != 0 {
				t.Fatalf("idle polls published %d envelopes, want none (%s)", got, describe(r.probe.published()))
			}

			if tc.wantTurns == 1 {
				for i, stats := range per {
					for f := 0; f < tc.files; f++ {
						if got := stats[turnName(f)]; got != 1 {
							t.Fatalf("poll %d stat'ed %s %d times, want 1 (one turn: every file every poll)", i+1, turnName(f), got)
						}
					}
					if len(stats) != tc.files {
						t.Fatalf("poll %d stat'ed %d names, want %d", i+1, len(stats), tc.files)
					}
				}
				return
			}

			// Every window of wantTurns consecutive polls, whatever poll it
			// starts on, stats each file exactly once.
			for start := 0; start+tc.wantTurns <= len(per); start++ {
				seen := map[string]int{}
				for _, stats := range per[start : start+tc.wantTurns] {
					for name, n := range stats {
						seen[name] += n
					}
				}
				for f := 0; f < tc.files; f++ {
					if got := seen[turnName(f)]; got != 1 {
						t.Fatalf("polls %d to %d stat'ed %s %d times, want exactly once", start+1, start+tc.wantTurns, turnName(f), got)
					}
				}
				if len(seen) != tc.files {
					t.Fatalf("polls %d to %d stat'ed %d names, want %d", start+1, start+tc.wantTurns, len(seen), tc.files)
				}
			}
			// Each poll takes about files/turns of them: never all, and never
			// more than twice the fair share (the turns come from a hash of
			// the name, so they are not exactly even).
			fair := (tc.files + tc.wantTurns - 1) / tc.wantTurns
			for i, stats := range per {
				total := 0
				for _, n := range stats {
					total += n
				}
				if total > 2*fair || total >= tc.files {
					t.Errorf("poll %d made %d stats, want about %d (at most %d) of %d files", i+1, total, fair, 2*fair, tc.files)
				}
			}
		})
	}
}

// The rotation moves only on polls that read the folder. A poll whose listing
// fails is skipped entirely (#398) and must not use up a turn, or the files
// of that turn would wait a whole extra rotation.
func TestStatTurns_FailedReadDoesNotUseATurn(t *testing.T) {
	r := newTurnRig(t, 40, 10)
	turns := r.turns()
	seen := map[string]int{}
	for i := 0; i < turns; i++ {
		if i == 1 {
			r.c.readDir = func(string) ([]os.DirEntry, error) { return nil, fs.ErrPermission }
			if stats := r.poll(); len(stats) != 0 {
				t.Fatalf("a poll whose listing failed made %d stats, want none", len(stats))
			}
			r.c.readDir = nil
		}
		for name, n := range r.poll() {
			seen[name] += n
		}
	}
	for f := 0; f < 40; f++ {
		if got := seen[turnName(f)]; got != 1 {
			t.Fatalf("%d good polls around a failed one stat'ed %s %d times, want once", turns, turnName(f), got)
		}
	}
}

// --- 3: names that are stat'ed every poll ------------------------------------

// Criterion 3: new files are stat'ed every poll until they are published, and
// are published on the same poll in a large folder as in a small one.
func TestStatTurns_NewFilesAreStatedEveryPoll(t *testing.T) {
	newNames := make([]string, 12)
	for i := range newNames {
		newNames[i] = fmt.Sprintf("new-%02d.csv", i)
	}
	publishedOn := func(r *turnRig) int {
		t.Helper()
		for _, name := range newNames {
			writeAt(t, r.dir, name, []byte("new "+name), turnsBaseMtime.Add(time.Hour))
		}
		for p := 1; p <= 20; p++ {
			stats := r.poll()
			done := 0
			for _, name := range newNames {
				switch n := len(r.probe.forName(name)); n {
				case 0:
				case 1:
					done++
				default:
					t.Fatalf("poll %d: %s published %d times", p, name, n)
				}
			}
			if done != 0 && done != len(newNames) {
				t.Fatalf("poll %d published %d of %d new files; want them all on the same poll", p, done, len(newNames))
			}
			// Every poll up to and including the one that publishes them
			// looks at each of them once.
			for _, name := range newNames {
				if stats[name] != 1 {
					t.Fatalf("poll %d stat'ed new file %s %d times, want 1", p, name, stats[name])
				}
			}
			if done == len(newNames) {
				return p
			}
		}
		t.Fatalf("new files not all published after 20 polls (%s)", describe(r.probe.published()))
		return 0
	}

	small := newTurnRig(t, 8, 10)
	large := newTurnRig(t, 100, 10)
	if small.turns() != 1 || large.turns() != 10 {
		t.Fatalf("premise: turns %d and %d, want 1 and 10", small.turns(), large.turns())
	}
	// Start the large folder somewhere in its rotation.
	large.polls(4)

	smallPoll, largePoll := publishedOn(small), publishedOn(large)
	if smallPoll != 2 {
		t.Errorf("small folder published new files on poll %d, want 2 (seen, then stable)", smallPoll)
	}
	if largePoll != smallPoll {
		t.Errorf("large folder published new files on poll %d, small folder on poll %d; want the same", largePoll, smallPoll)
	}
	for _, name := range newNames {
		assertWatchEnvelope(t, large.probe.forName(name)[0], name, []byte("new "+name))
	}
}

// Criterion 3: a done file that was seen changed is stat'ed every poll while
// it keeps changing, and published once, with its last content, on the poll
// after it stops.
func TestStatTurns_PendingFileIsStatedEveryPoll(t *testing.T) {
	r := newTurnRig(t, 100, 10)
	turns := r.turns()
	target := turnName(37)

	// Replace it and poll until a poll looks at it: then it is seen changed.
	writeAt(t, r.dir, target, []byte("v1"), turnsBaseMtime.Add(time.Hour))
	noticed := false
	for p := 0; p < turns && !noticed; p++ {
		noticed = r.poll()[target] == 1
	}
	if !noticed {
		t.Fatalf("premise: %s not stat'ed in %d polls after it was replaced", target, turns)
	}
	r.requireCount("seen changed", target, 0)

	// Keep it changing for more than a whole rotation: it must be looked at
	// every poll, whatever the turn, and not go out half-written.
	for p := 1; p <= 2*turns; p++ {
		content := []byte(fmt.Sprintf("v%d", p+1))
		writeAt(t, r.dir, target, content, turnsBaseMtime.Add(time.Hour+time.Duration(p)*time.Minute))
		if got := r.poll()[target]; got != 1 {
			t.Fatalf("poll %d while it changes stat'ed %s %d times, want 1", p, target, got)
		}
		r.requireCount(fmt.Sprintf("changing, poll %d", p), target, 0)
	}
	last := []byte(fmt.Sprintf("v%d", 2*turns+1))

	// Settled: the next poll publishes it, once.
	if got := r.poll()[target]; got != 1 {
		t.Fatalf("poll after it settled stat'ed %s %d times, want 1", target, got)
	}
	r.requireCount("settled", target, 1)
	assertWatchEnvelope(t, r.probe.forName(target)[0], target, last)
	r.polls(2 * turns)
	r.requireCount("after it settled", target, 1)
	if got := len(r.probe.published()); got != 1 {
		t.Fatalf("%d envelopes in all, want 1 (%s)", got, describe(r.probe.published()))
	}
}

// Criterion 3: a done file with no stamp (its stat failed at the start) is
// stat'ed every poll until a stat succeeds and it takes its stamp, whatever
// the turn; it is not published for that.
func TestStatTurns_StamplessFileIsStatedEveryPoll(t *testing.T) {
	stampless := make([]string, 10)
	for i := range stampless {
		stampless[i] = fmt.Sprintf("nostamp-%02d.csv", i)
	}
	r := newTurnRig(t, 90, 10, stampless...)
	turns := r.turns()
	if turns != 10 {
		t.Fatalf("premise: %d turns, want 10", turns)
	}
	// The stat keeps failing for a whole rotation and more, so they stay
	// done with no stamp.
	for _, name := range stampless {
		r.setFailing(name, true)
	}
	for p := 1; p <= turns+2; p++ {
		stats := r.poll()
		for _, name := range stampless {
			if stats[name] != 1 {
				t.Fatalf("poll %d stat'ed stampless %s %d times, want 1", p, name, stats[name])
			}
			if !r.ac.knownFiles[name] {
				t.Fatalf("poll %d: stampless %s left the done set", p, name)
			}
		}
	}
	// The stat works again: one poll takes every stamp, publishes nothing.
	for _, name := range stampless {
		r.setFailing(name, false)
	}
	stats := r.poll()
	for _, name := range stampless {
		if stats[name] != 1 {
			t.Fatalf("first good poll stat'ed %s %d times, want 1", name, stats[name])
		}
		if _, stamped := r.ac.knownStamps[name]; !stamped {
			t.Fatalf("%s has no stamp after a good stat", name)
		}
	}
	// From then on they are ordinary done files, looked at in turns.
	seen := map[string]int{}
	for p := 0; p < turns; p++ {
		for name, n := range r.poll() {
			seen[name] += n
		}
	}
	for _, name := range stampless {
		if seen[name] != 1 {
			t.Errorf("a rotation after they took their stamps stat'ed %s %d times, want once", name, seen[name])
		}
	}
	if got := len(r.probe.published()); got != 0 {
		t.Fatalf("published %d envelopes, want none (%s)", got, describe(r.probe.published()))
	}
}

// --- 4: a replacement in a large folder ---------------------------------------

// Criterion 4: a done file replaced in a large folder is published exactly
// once, with the new content, within turns+1 polls of the replacement, on the
// poll right after the first one that looked at it, wherever in the rotation
// the replacement lands and whichever turn the name is in.
func TestStatTurns_ReplacementPublishedOnce(t *testing.T) {
	const files, budget = 100, 10
	// One name from each turn, so every turn is covered.
	byTurn := map[int]string{}
	for i := 0; i < files; i++ {
		name := turnName(i)
		if _, ok := byTurn[statTurn(name, files/budget)]; !ok {
			byTurn[statTurn(name, files/budget)] = name
		}
	}
	if len(byTurn) != files/budget {
		t.Fatalf("premise: names cover %d of %d turns", len(byTurn), files/budget)
	}
	turnKeys := make([]int, 0, len(byTurn))
	for k := range byTurn {
		turnKeys = append(turnKeys, k)
	}
	sort.Ints(turnKeys)

	for _, how := range []string{"overwrite", "rename over"} {
		for _, k := range turnKeys {
			target := byTurn[k]
			for _, idle := range []int{0, 1, 4, 9, 10, 13} {
				t.Run(fmt.Sprintf("%s/turn %d/after %d idle polls", how, k, idle), func(t *testing.T) {
					r := newTurnRig(t, files, budget)
					turns := r.turns()
					r.polls(idle)
					content := []byte(fmt.Sprintf("replaced %s after %d polls", target, idle))
					mtime := turnsBaseMtime.Add(time.Hour)
					if how == "overwrite" {
						replaceWriteAt(t, r.path(target), content, mtime)
					} else {
						r.renameOver(target, content, mtime)
					}

					lookedAt, publishedOn := 0, 0
					for p := 1; p <= turns+1; p++ {
						stats := r.poll()
						if lookedAt == 0 && stats[target] > 0 {
							lookedAt = p
						}
						if len(r.probe.forName(target)) > 0 {
							publishedOn = p
							break
						}
					}
					if publishedOn == 0 {
						t.Fatalf("not published within %d polls of the replacement (first looked at on poll %d)", turns+1, lookedAt)
					}
					if publishedOn != lookedAt+1 {
						t.Errorf("published on poll %d, first looked at on poll %d; want the poll right after", publishedOn, lookedAt)
					}
					r.polls(2 * turns)
					r.requireCount("two rotations later", target, 1)
					assertWatchEnvelope(t, r.probe.forName(target)[0], target, content)
					if got := len(r.probe.published()); got != 1 {
						t.Fatalf("%d envelopes in all, want 1 (%s)", got, describe(r.probe.published()))
					}
				})
			}
		}
	}
}

// Criterion 4, many at once: replace a file from every turn at the same time.
// Each goes out once, with its new content, within turns+1 polls, and the
// rest of the folder stays quiet.
func TestStatTurns_ManyReplacementsPublishedOnce(t *testing.T) {
	r := newTurnRig(t, 100, 10)
	turns := r.turns()
	r.polls(3)
	var targets []string
	for i := 0; i < 100; i += 7 {
		targets = append(targets, turnName(i))
	}
	for i, name := range targets {
		replaceWriteAt(t, r.path(name), []byte("new "+name), turnsBaseMtime.Add(time.Duration(i+1)*time.Minute))
	}
	r.polls(turns + 1)
	for _, name := range targets {
		r.requireCount("turns+1 polls after the replacements", name, 1)
		assertWatchEnvelope(t, r.probe.forName(name)[0], name, []byte("new "+name))
	}
	r.polls(2 * turns)
	if got := len(r.probe.published()); got != len(targets) {
		t.Fatalf("%d envelopes in all, want %d (%s)", got, len(targets), describe(r.probe.published()))
	}
}

// --- 5: deletions in a large folder --------------------------------------------

// Criterion 5: files deleted from a large folder leave the done set, with a
// "deleted" event each, on the next poll, whichever turn they are in. A name
// that comes back is a new file again and is published.
func TestStatTurns_DeletionsNoticedOnNextPoll(t *testing.T) {
	r := newTurnRig(t, 100, 10)
	r.polls(2)
	events, unsub := r.c.subscribeEvents(r.ac.ConnectionID)
	defer unsub()

	var gone []string
	for i := 0; i < 100; i += 9 {
		gone = append(gone, turnName(i))
	}
	for _, name := range gone {
		if err := os.Remove(r.path(name)); err != nil {
			t.Fatal(err)
		}
	}
	r.poll()

	deleted := map[string]bool{}
	for len(events) > 0 {
		ev := <-events
		if ev.Type != "deleted" {
			t.Fatalf("unexpected %q event for %s", ev.Type, ev.Filename)
		}
		deleted[ev.Filename] = true
	}
	for _, name := range gone {
		if !deleted[name] {
			t.Errorf("no deleted event for %s on the poll after it was removed", name)
		}
		if r.ac.knownFiles[name] {
			t.Errorf("%s still in the done set on the poll after it was removed", name)
		}
		if _, stamped := r.ac.knownStamps[name]; stamped {
			t.Errorf("%s still has a stamp on the poll after it was removed", name)
		}
	}
	if len(deleted) != len(gone) {
		t.Errorf("%d deleted events, want %d", len(deleted), len(gone))
	}
	if got := len(r.ac.knownFiles); got != 100-len(gone) {
		t.Errorf("%d done files after the deletions, want %d", got, 100-len(gone))
	}

	// One comes back with the same content and mtime it had: a new file,
	// published like any other new file.
	back := gone[0]
	writeAt(t, r.dir, back, turnContent(back), turnsBaseMtime)
	r.polls(2)
	r.requireCount("came back", back, 1)
	assertWatchEnvelope(t, r.probe.forName(back)[0], back, turnContent(back))
	if got := len(r.probe.published()); got != 1 {
		t.Fatalf("%d envelopes in all, want 1 (%s)", got, describe(r.probe.published()))
	}
}
