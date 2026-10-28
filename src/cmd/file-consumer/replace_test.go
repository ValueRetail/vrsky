package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// #420: a file already published and then replaced under the same name (an
// overwrite, or a rename over it) is a new file again and must be published
// once, with the new content, after it settles. An unchanged file is never
// published again, not across polls and not across a restart. The tests drive
// polls by hand on a fake clock (known_test.go) and set every mtime
// explicitly, so a coarse filesystem clock cannot make two versions look the
// same.

// replaceBaseMtime is the mtime of every original file. Replacements get later
// mtimes. All of them lie before the fake clock's start (2026-03-01), so no
// file looks like it was written in the future.
var replaceBaseMtime = time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)

// replaceRig is one connection watching one folder with a checkpoint store
// that outlives the watcher, so a test can stop and start it. Every watcher
// it starts publishes to the same probe, so counts span restarts.
type replaceRig struct {
	t     *testing.T
	dir   string
	store *lockedStore
	probe *recordingProbe
	w     *knownWatcher
}

// newReplaceRig starts the first watcher on an empty folder (a first start
// with nothing saved).
func newReplaceRig(t *testing.T) *replaceRig {
	t.Helper()
	r := &replaceRig{t: t, dir: t.TempDir(), store: newLockedStore(), probe: &recordingProbe{}}
	r.restart()
	return r
}

// restart drops the current watcher (it is never polled again) and starts a
// new one that loads the saved checkpoint, as a new pod would.
func (r *replaceRig) restart() {
	r.t.Helper()
	r.w = newKnownWatcher(r.t, r.dir, r.store, r.probe.publish)
}

func (r *replaceRig) poll() {
	r.t.Helper()
	r.w.poll(context.Background())
}

func (r *replaceRig) polls(n int) {
	r.t.Helper()
	for i := 0; i < n; i++ {
		r.poll()
	}
}

// pollAfter advances the watcher's clock by d and polls once; d below
// watchInterval is a poll that comes too soon.
func (r *replaceRig) pollAfter(d time.Duration) {
	r.t.Helper()
	r.w.clock.advance(d)
	r.w.c.pollOnce(context.Background(), r.w.ac)
}

func (r *replaceRig) path(name string) string { return filepath.Join(r.dir, name) }

// write creates or overwrites name in place (same inode) and sets its mtime.
func (r *replaceRig) write(name string, content []byte, mtime time.Time) {
	r.t.Helper()
	replaceWriteAt(r.t, r.path(name), content, mtime)
}

// renameOver writes content to name+".tmp", sets its mtime, then renames it
// over name, the way most tools replace a file atomically. No poll runs in
// between, so the watcher never sees the .tmp file.
func (r *replaceRig) renameOver(name string, content []byte, mtime time.Time) {
	r.t.Helper()
	tmp := r.path(name + ".tmp")
	replaceWriteAt(r.t, tmp, content, mtime)
	if err := os.Rename(tmp, r.path(name)); err != nil {
		r.t.Fatalf("rename %s over %s: %v", tmp, name, err)
	}
}

func replaceWriteAt(t *testing.T, path string, content []byte, mtime time.Time) {
	t.Helper()
	before, statErr := os.Stat(path)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !after.ModTime().Equal(mtime) {
		t.Fatalf("setup: %s mtime = %v, want %v (filesystem too coarse?)", path, after.ModTime(), mtime)
	}
	if statErr == nil && after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("setup: overwrite of %s kept mtime %v; the test needs a new one", path, before.ModTime())
	}
}

// requireCount fails unless exactly want envelopes were published for name.
func (r *replaceRig) requireCount(step, name string, want int) {
	r.t.Helper()
	if got := len(r.probe.forName(name)); got != want {
		r.t.Fatalf("%s: %q published %d times, want %d (%s)",
			step, name, got, want, describe(r.probe.published()))
	}
}

// requireTotal fails unless exactly want envelopes were published in all.
func (r *replaceRig) requireTotal(step string, want int) {
	r.t.Helper()
	if got := len(r.probe.published()); got != want {
		r.t.Fatalf("%s: %d envelopes published in total, want %d (%s)",
			step, got, want, describe(r.probe.published()))
	}
}

// requireNth checks the i-th (0-based) envelope published for name.
func (r *replaceRig) requireNth(name string, i int, content []byte) {
	r.t.Helper()
	envs := r.probe.forName(name)
	if i >= len(envs) {
		r.t.Fatalf("%q has %d envelopes, want at least %d (%s)", name, len(envs), i+1, describe(r.probe.published()))
	}
	assertWatchEnvelope(r.t, envs[i], name, content)
}

// requireNeverPayload fails if any envelope carried exactly content.
func (r *replaceRig) requireNeverPayload(step string, content []byte) {
	r.t.Helper()
	for _, env := range r.probe.published() {
		if bytes.Equal(env.Payload, content) {
			r.t.Fatalf("%s: published %q, which must never be published (%s)",
				step, content, describe(r.probe.published()))
		}
	}
}

// publishOriginal writes name and polls until the watcher publishes it.
func (r *replaceRig) publishOriginal(name string, content []byte) {
	r.t.Helper()
	before := len(r.probe.forName(name))
	r.write(name, content, replaceBaseMtime)
	r.polls(2)
	r.requireCount("original published", name, before+1)
	r.requireNth(name, before, content)
}

// Acceptance 1: an overwrite is published once, with the new content, only
// after it is stable: not on the poll that first sees the change and not on
// any poll less than one interval after that.
func TestReplace_OverwritePublishedOnceAfterStable(t *testing.T) {
	original := []byte(`{"order":1,"status":"new"}`)
	cases := []struct {
		name        string
		replacement []byte
	}{
		{"different size", []byte(`{"order":1,"status":"shipped","lines":3}`)},
		{"same size, new mtime", []byte(`{"order":1,"status":"due"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "same size, new mtime" && len(tc.replacement) != len(original) {
				t.Fatalf("setup: replacement is %d bytes, original %d", len(tc.replacement), len(original))
			}
			r := newReplaceRig(t)
			r.publishOriginal("order.json", original)
			r.polls(3)
			r.requireCount("unchanged after publish", "order.json", 1)

			r.write("order.json", tc.replacement, replaceBaseMtime.Add(time.Hour))

			r.poll() // first sighting of the change at t
			r.requireCount("first poll that sees the overwrite", "order.json", 1)
			r.pollAfter(time.Millisecond)
			r.requireCount("t+1ms", "order.json", 1)
			r.pollAfter(watchInterval - 2*time.Millisecond)
			r.requireCount("t+interval-1ms", "order.json", 1)
			r.pollAfter(time.Millisecond)
			r.requireCount("t+interval", "order.json", 2)
			r.requireNth("order.json", 1, tc.replacement)

			r.polls(5)
			r.requireCount("polls after the republish", "order.json", 2)
			r.requireTotal("end", 2)

			r.restart()
			r.polls(4)
			r.requireCount("after a restart", "order.json", 2)
		})
	}
}

// Acceptance 2: a file nobody touches is published once, ever: many polls,
// then several restarts that load the saved checkpoint.
func TestReplace_UnchangedFileNeverRepublished(t *testing.T) {
	r := newReplaceRig(t)
	a, b := []byte(`{"a":1}`), []byte(`{"b":22}`)
	r.publishOriginal("a.json", a)
	r.publishOriginal("b.json", b)

	r.polls(20)
	r.requireTotal("20 more polls", 2)

	for i := 0; i < 3; i++ {
		r.restart()
		r.polls(5)
		r.requireTotal("after a restart", 2)
	}
	r.requireNth("a.json", 0, a)
	r.requireNth("b.json", 0, b)
}

// saveOldFormat writes the checkpoint as the code before #420 saved it: only
// the folder and the names, written as raw JSON so a field added to knownState
// later cannot creep in.
func (r *replaceRig) saveOldFormat(names ...string) {
	r.t.Helper()
	old, err := json.Marshal(map[string]any{"watch_dir": r.dir, "known": names})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.store.Save(context.Background(), &checkpoint.Checkpoint{
		TenantID: knownTenant, ConnectionID: knownConn, NodeID: knownNode,
		LastProcessedAt: time.Now().UTC(), State: old,
	}); err != nil {
		r.t.Fatal(err)
	}
}

// newOldFormatRig puts files in a folder whose checkpoint was saved by the
// code before #420, then starts a watcher on it (the upgrade).
func newOldFormatRig(t *testing.T, files map[string][]byte, known ...string) *replaceRig {
	t.Helper()
	r := &replaceRig{t: t, dir: t.TempDir(), store: newLockedStore(), probe: &recordingProbe{}}
	for name, content := range files {
		r.write(name, content, replaceBaseMtime)
	}
	r.saveOldFormat(known...)
	r.restart()
	return r
}

// Acceptance 3: the upgrade loads an old checkpoint (names only). Files it
// lists that are still in the folder are not published again; a file it does
// not list is published once (the #391 behaviour for a file that arrived
// while the connection was stopped).
func TestReplace_OldFormatCheckpointPublishesNothingKnown(t *testing.T) {
	files := map[string][]byte{
		"a.json": []byte(`{"a":1}`),
		"b.json": []byte(`{"b":2}`),
		"c.json": []byte(`{"c":3}`),
	}
	r := newOldFormatRig(t, files, "a.json", "b.json", "gone.json")

	r.polls(6)
	r.requireCount("a.json, listed in the old checkpoint", "a.json", 0)
	r.requireCount("b.json, listed in the old checkpoint", "b.json", 0)
	r.requireCount("c.json, not listed", "c.json", 1)
	r.requireNth("c.json", 0, files["c.json"])
	r.requireTotal("after the upgrade", 1)

	// The checkpoint the upgraded watcher saved must keep it that way.
	r.restart()
	r.polls(4)
	r.requireTotal("after a second restart", 1)
}

// After the upgrade, a file the old checkpoint listed is tracked like any
// other: overwriting it is a new file. Otherwise every file published before
// the upgrade would keep the #420 bug.
func TestReplace_OldFormatCheckpointThenOverwrite(t *testing.T) {
	files := map[string][]byte{"a.json": []byte(`{"a":1}`)}
	r := newOldFormatRig(t, files, "a.json")
	r.polls(3)
	r.requireTotal("after the upgrade", 0)

	replacement := []byte(`{"a":2,"replaced":true}`)
	r.write("a.json", replacement, replaceBaseMtime.Add(time.Hour))
	r.poll()
	r.requireCount("first sighting of the overwrite", "a.json", 0)
	r.polls(4)
	r.requireCount("after it settled", "a.json", 1)
	r.requireNth("a.json", 0, replacement)
	r.requireNeverPayload("after the upgrade", files["a.json"])
}

// Acceptance 4: a rename over a published file (write x.tmp, rename it to x)
// is published once with the new content, after it settles.
func TestReplace_RenameOverPublishedOnce(t *testing.T) {
	original := []byte(`{"stock":100}`)
	cases := []struct {
		name        string
		replacement []byte
	}{
		{"different size", []byte(`{"stock":99,"sku":"A-1"}`)},
		{"same size, new mtime", []byte(`{"stock":250}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "same size, new mtime" && len(tc.replacement) != len(original) {
				t.Fatalf("setup: replacement is %d bytes, original %d", len(tc.replacement), len(original))
			}
			r := newReplaceRig(t)
			r.publishOriginal("x.json", original)
			r.polls(2)

			r.renameOver("x.json", tc.replacement, replaceBaseMtime.Add(time.Hour))
			r.poll()
			r.requireCount("first poll that sees the rename", "x.json", 1)
			r.poll()
			r.requireCount("one interval later", "x.json", 2)
			r.requireNth("x.json", 1, tc.replacement)

			r.polls(5)
			r.requireCount("later polls", "x.json", 2)
			r.requireCount("the temp file", "x.json.tmp", 0)
			r.requireTotal("end", 2)
		})
	}
}

// A file replaced while the connection was stopped is published once after
// the next start; its unchanged neighbour is not.
func TestReplace_ReplacedWhileStoppedPublishedOnceAfterRestart(t *testing.T) {
	cases := []struct {
		name    string
		replace func(r *replaceRig, name string, content []byte, mtime time.Time)
	}{
		{"overwrite", (*replaceRig).write},
		{"rename over", (*replaceRig).renameOver},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newReplaceRig(t)
			r.publishOriginal("x.json", []byte(`{"v":1}`))
			r.publishOriginal("y.json", []byte(`{"y":1}`))
			r.polls(2)

			// Stopped: the watcher is not polled while the file changes.
			replacement := []byte(`{"v":2,"while":"stopped"}`)
			tc.replace(r, "x.json", replacement, replaceBaseMtime.Add(time.Hour))
			r.restart()

			r.polls(6)
			r.requireCount("x.json after the restart", "x.json", 2)
			r.requireNth("x.json", 1, replacement)
			r.requireCount("y.json, unchanged", "y.json", 1)
			r.requireTotal("end", 3)

			r.restart()
			r.polls(3)
			r.requireTotal("after another restart", 3)
		})
	}
}

// Two overwrites before the first one settles: only the final content is
// published, once.
func TestReplace_OverwrittenTwiceBeforeStablePublishedOnce(t *testing.T) {
	r := newReplaceRig(t)
	r.publishOriginal("p.json", []byte(`{"v":1}`))

	second := []byte(`{"v":2,"half":true}`)
	final := []byte(`{"v":3,"done":true,"lines":[1,2,3]}`)

	r.write("p.json", second, replaceBaseMtime.Add(time.Hour))
	r.poll() // sees the first overwrite
	r.requireCount("first sighting of v2", "p.json", 1)

	r.write("p.json", final, replaceBaseMtime.Add(2*time.Hour))
	r.poll() // v2 never stabilised; sees v3 for the first time
	r.requireCount("first sighting of v3", "p.json", 1)
	r.poll()
	r.requireCount("v3 settled", "p.json", 2)
	r.requireNth("p.json", 1, final)

	r.polls(5)
	r.requireCount("later polls", "p.json", 2)
	r.requireNeverPayload("end", second)
}

// Overwrites keep coming: each settled version is published once, in order.
func TestReplace_RepeatedOverwritesEachPublishedOnce(t *testing.T) {
	r := newReplaceRig(t)
	versions := [][]byte{[]byte(`{"v":1}`), []byte(`{"v":2,"x":1}`), []byte(`{"v":3}`), []byte(`{"v":4,"xyz":true}`)}
	r.publishOriginal("feed.json", versions[0])
	for i, v := range versions[1:] {
		r.write("feed.json", v, replaceBaseMtime.Add(time.Duration(i+1)*time.Hour))
		r.polls(4)
		r.requireCount("after a settled overwrite", "feed.json", i+2)
		r.requireNth("feed.json", i+1, v)
	}
	r.requireTotal("end", len(versions))
}
