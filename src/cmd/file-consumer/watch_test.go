package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// recordingProbe keeps every envelope the watcher publishes, in order. The
// streaming tests' fileProbe keeps only the last one, which cannot tell "once"
// from "twice" (#374).
type recordingProbe struct {
	mu   sync.Mutex
	envs []*envelope.Envelope
}

func (p *recordingProbe) publish(_ context.Context, env *envelope.Envelope) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.envs = append(p.envs, env)
	return nil
}

func (p *recordingProbe) published() []*envelope.Envelope {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*envelope.Envelope(nil), p.envs...)
}

// forName returns the envelopes published for a given filename.
func (p *recordingProbe) forName(name string) []*envelope.Envelope {
	var out []*envelope.Envelope
	for _, env := range p.published() {
		if fn, _ := env.Metadata["filename"].(string); fn == name {
			out = append(out, env)
		}
	}
	return out
}

// watchInterval is the poll interval the fixture sets explicitly, so the tests
// do not depend on defaultPollInterval.
const watchInterval = 5 * time.Second

// watchFixture is a consumer wired to a recording probe plus one connection
// watching a temp dir. Everything goes inline (no payload store, large
// inlineMax) and db is nil, so the last_payload write is skipped. Polls happen
// only when the test calls poll() or pollAfter(); nothing runs in the
// background. The watcher's clock is a fake the fixture owns and only those two
// methods move it, so no test sleeps.
type watchFixture struct {
	t     *testing.T
	dir   string
	probe *recordingProbe
	c     *fileConsumer
	ac    *ActiveConnection
	clock time.Time
}

// newWatchFixture creates the watch dir, lets setup put files in it, then takes
// the baseline the way the start path does (listFiles before watching).
func newWatchFixture(t *testing.T, setup func(dir string)) *watchFixture {
	t.Helper()
	dir := t.TempDir()
	if setup != nil {
		setup(dir)
	}
	probe := &recordingProbe{}
	f := &watchFixture{t: t, dir: dir, probe: probe,
		clock: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	c := &fileConsumer{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		publish:      probe.publish,
		inlineMax:    1 << 20,
		pollInterval: watchInterval,
		now:          func() time.Time { return f.clock },
	}
	ac := &ActiveConnection{
		ConnectionID: "conn-watch",
		TenantID:     "tenant-x",
		WatchDir:     dir,
		Cancel:       func() {},
		knownFiles:   c.listFiles(dir),
	}
	f.c, f.ac = c, ac
	return f
}

// poll means "one poll interval has passed, then poll": the regular cadence of
// the watch loop.
func (f *watchFixture) poll() {
	f.t.Helper()
	f.pollAfter(watchInterval)
}

// pollAfter advances the watcher's clock by d and polls once. A d smaller than
// watchInterval models a poll that comes early, e.g. right behind a slow one.
func (f *watchFixture) pollAfter(d time.Duration) {
	f.t.Helper()
	f.clock = f.clock.Add(d)
	f.c.pollOnce(context.Background(), f.ac)
}

func (f *watchFixture) path(name string) string { return filepath.Join(f.dir, name) }

func (f *watchFixture) write(name string, content []byte) {
	f.t.Helper()
	if err := os.WriteFile(f.path(name), content, 0o600); err != nil {
		f.t.Fatalf("write %s: %v", name, err)
	}
}

func (f *watchFixture) appendTo(name string, content []byte) {
	f.t.Helper()
	fh, err := os.OpenFile(f.path(name), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		f.t.Fatalf("open %s for append: %v", name, err)
	}
	if _, err := fh.Write(content); err != nil {
		_ = fh.Close()
		f.t.Fatalf("append %s: %v", name, err)
	}
	if err := fh.Close(); err != nil {
		f.t.Fatalf("close %s: %v", name, err)
	}
}

// requireTotal fails unless exactly want envelopes were published in all.
func (f *watchFixture) requireTotal(step string, want int) {
	f.t.Helper()
	if got := len(f.probe.published()); got != want {
		f.t.Fatalf("%s: %d envelopes published in total, want %d (%s)",
			step, got, want, describe(f.probe.published()))
	}
}

// requirePublishedOnce fails unless exactly one envelope was published for
// name, with exactly content as payload and the watcher's Source/metadata.
func (f *watchFixture) requirePublishedOnce(step, name string, content []byte) {
	f.t.Helper()
	envs := f.probe.forName(name)
	if len(envs) != 1 {
		f.t.Fatalf("%s: %d envelopes for %q, want 1 (%s)",
			step, len(envs), name, describe(f.probe.published()))
	}
	assertWatchEnvelope(f.t, envs[0], name, content)
}

func assertWatchEnvelope(t *testing.T, env *envelope.Envelope, name string, content []byte) {
	t.Helper()
	if !bytes.Equal(env.Payload, content) {
		t.Errorf("%s payload = %q, want %q", name, env.Payload, content)
	}
	if env.Source != "watch:"+name {
		t.Errorf("%s Source = %q, want %q", name, env.Source, "watch:"+name)
	}
	if fn, _ := env.Metadata["filename"].(string); fn != name {
		t.Errorf("Metadata[filename] = %q, want %q", fn, name)
	}
}

func describe(envs []*envelope.Envelope) string {
	var b bytes.Buffer
	b.WriteString("published:")
	for _, env := range envs {
		b.WriteString(" ")
		b.WriteString(env.Source)
		b.WriteString("=")
		b.WriteString(string(env.Payload))
	}
	return b.String()
}

// A stable file is not published on the first poll that sees it (it might
// still be being written) and is published on the next one, once. Further
// polls must not publish it again (#374 acceptance 2).
func TestPollOnce_StableFilePublishedOnSecondPollOnly(t *testing.T) {
	f := newWatchFixture(t, nil)
	content := []byte(`{"order":42}`)
	f.write("order.json", content)

	f.poll()
	f.requireTotal("first poll that sees the file", 0)

	f.poll()
	f.requirePublishedOnce("second poll", "order.json", content)

	for i := 0; i < 3; i++ {
		f.poll()
	}
	f.requireTotal("after further polls", 1)
}

// The bug in #374: a file written in two parts with a poll in between was
// published with only the first part. It must be published exactly once, with
// the full content, after it stops changing, and never with only part 1.
func TestPollOnce_FileWrittenAcrossPollPublishedOnceComplete(t *testing.T) {
	f := newWatchFixture(t, nil)
	part1 := []byte(`{"lines":[1,2,3,`)
	part2 := []byte(`4,5,6]}`)
	full := append(append([]byte(nil), part1...), part2...)

	f.write("big.json", part1)
	f.poll()
	f.requireTotal("poll after part 1", 0)

	f.appendTo("big.json", part2) // size changes, so mtime granularity does not matter
	f.poll()
	f.requireTotal("first poll after part 2 (size just changed)", 0)

	f.poll()
	f.requirePublishedOnce("poll with size unchanged", "big.json", full)

	for i := 0; i < 3; i++ {
		f.poll()
	}
	f.requirePublishedOnce("after further polls", "big.json", full)
	for _, env := range f.probe.published() {
		if bytes.Equal(env.Payload, part1) {
			t.Fatalf("published the half-written file: %q", env.Payload)
		}
	}
}

// A file that grows on every poll is never published while it grows; once it
// stops it is published once with everything written (#374 acceptance 2).
func TestPollOnce_GrowingFileNotPublishedWhileGrowing(t *testing.T) {
	f := newWatchFixture(t, nil)
	var full []byte
	chunk := []byte("row,1,2,3\n")

	f.write("feed.csv", chunk)
	full = append(full, chunk...)
	for i := 0; i < 5; i++ {
		f.poll()
		f.requireTotal("while growing", 0)
		f.appendTo("feed.csv", chunk)
		full = append(full, chunk...)
	}

	f.poll() // first poll after the last append: size just changed
	f.requireTotal("first poll after growth stopped", 0)
	f.poll()
	f.requirePublishedOnce("second poll after growth stopped", "feed.csv", full)
	f.poll()
	f.requireTotal("after a further poll", 1)
}

// Dotfiles and common temp names are what uploaders write before renaming into
// place. They must never be published under the temp name, however many polls
// see them; after os.Rename to the final name the file is published exactly
// once, complete, under the final name (#374 acceptance 1; suffix match is
// case-insensitive).
func TestPollOnce_TempNamesSkippedUntilRenamed(t *testing.T) {
	cases := []struct {
		name  string
		temp  string
		final string
	}{
		{"dotfile", ".upload.json", "upload.json"},
		{"tmp suffix", "x.json.tmp", "x.json"},
		{"part suffix", "x.json.part", "x.json"},
		{"tilde prefix", "~x.json", "x.json"},
		{"upper-case tmp suffix", "X.JSON.TMP", "X.JSON"},
		{"upper-case part suffix", "data.csv.PART", "data.csv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWatchFixture(t, nil)
			part1 := []byte(`{"a":1,`)
			part2 := []byte(`"b":2}`)
			full := append(append([]byte(nil), part1...), part2...)

			f.write(tc.temp, part1)
			f.poll()
			f.appendTo(tc.temp, part2)
			for i := 0; i < 4; i++ { // stable for several polls: still a temp name
				f.poll()
			}
			if envs := f.probe.forName(tc.temp); len(envs) != 0 {
				t.Fatalf("temp name %q was published: %s", tc.temp, describe(envs))
			}
			f.requireTotal("before rename", 0)

			if err := os.Rename(f.path(tc.temp), f.path(tc.final)); err != nil {
				t.Fatalf("rename: %v", err)
			}
			f.poll()
			f.poll()
			f.requirePublishedOnce("after rename", tc.final, full)

			for i := 0; i < 3; i++ {
				f.poll()
			}
			f.requireTotal("after further polls", 1)
		})
	}
}

// Files that were in the directory when the connection started (the baseline,
// #366) are never published, however many polls run, while a file added after
// start still is (#374 acceptance 3).
func TestPollOnce_BaselineFilesNeverPublished(t *testing.T) {
	f := newWatchFixture(t, func(dir string) {
		for _, name := range []string{"old.json", "old.csv", "legacy.xml"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("pre-existing "+name), 0o600); err != nil {
				t.Fatalf("seed %s: %v", name, err)
			}
		}
	})
	for i := 0; i < 5; i++ {
		f.poll()
	}
	f.requireTotal("baseline only", 0)

	fresh := []byte(`{"new":true}`)
	f.write("new.json", fresh)
	f.poll()
	f.poll()
	f.poll()
	f.requirePublishedOnce("new file next to the baseline", "new.json", fresh)
	f.requireTotal("baseline still unpublished", 1)
}

// Current behaviour kept by #374: a file deleted and later re-created under
// the same name is a new file and is published again, with its new content.
func TestPollOnce_DeletedAndRecreatedFilePublishedAgain(t *testing.T) {
	f := newWatchFixture(t, nil)
	first := []byte(`{"v":1}`)
	second := []byte(`{"v":2,"more":"data"}`)

	f.write("again.json", first)
	f.poll()
	f.poll()
	f.requirePublishedOnce("first version", "again.json", first)

	if err := os.Remove(f.path("again.json")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	f.poll()
	f.requireTotal("after delete", 1)

	f.write("again.json", second)
	f.poll()
	f.requireTotal("first poll after re-create", 1)
	f.poll()
	envs := f.probe.forName("again.json")
	if len(envs) != 2 {
		t.Fatalf("%d envelopes for again.json, want 2 (%s)", len(envs), describe(envs))
	}
	assertWatchEnvelope(t, envs[1], "again.json", second)

	f.poll()
	f.requireTotal("after a further poll", 2)
}

// Pins the mtime half of the stability check (#374 review): a rewrite that
// keeps the size the same (an in-place overwrite, a fixed-width record) must
// restart the wait. Only the mtime moves here, by a full hour so filesystem
// timestamp granularity cannot hide it; a size-only check publishes on the
// second poll and fails this test.
func TestPollOnce_MtimeChangeRestartsWait(t *testing.T) {
	f := newWatchFixture(t, nil)
	content := []byte(`{"fixed":"width"}`)
	f.write("same-size.json", content)

	f.poll()
	f.requireTotal("first poll that sees the file", 0)

	info, err := os.Stat(f.path("same-size.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	moved := info.ModTime().Add(time.Hour)
	if err := os.Chtimes(f.path("same-size.json"), moved, moved); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	after, err := os.Stat(f.path("same-size.json"))
	if err != nil {
		t.Fatalf("stat after chtimes: %v", err)
	}
	if after.Size() != info.Size() || after.ModTime().Equal(info.ModTime()) {
		t.Fatalf("setup: want same size and a new mtime, got size %d->%d mtime %v->%v",
			info.Size(), after.Size(), info.ModTime(), after.ModTime())
	}

	f.poll()
	f.requireTotal("poll after the mtime moved (size unchanged)", 0)

	f.poll()
	f.requirePublishedOnce("poll with size and mtime unchanged", "same-size.json", content)

	f.poll()
	f.requireTotal("after a further poll", 1)
}

// Pins the slow-poll regression from the #374 review: a poll that came right
// behind a slow one saw a just-sighted file again a few ms later, took it as
// stable and published it truncated. Stability needs a full poll interval on
// the watcher's clock since the first sighting. A too-soon poll must neither
// publish nor reset that first sighting, and a write in between must still
// restart the wait from the poll that sees it.
func TestPollOnce_TooSoonPollDoesNotPublish(t *testing.T) {
	part1 := []byte(`{"rows":[1,2,`)
	part2 := []byte(`3,4]}`)
	full := append(append([]byte(nil), part1...), part2...)

	type step struct {
		desc      string
		advance   time.Duration // clock step before this poll
		appendNow bool          // append part2 before this poll
		wantTotal int
	}
	cases := []struct {
		name    string
		content []byte // what must finally be published
		steps   []step
	}{
		{
			name:    "poll a few ms behind the first sighting",
			content: part1,
			steps: []step{
				{desc: "first sighting at t", advance: watchInterval, wantTotal: 0},
				{desc: "t+5ms, unchanged", advance: 5 * time.Millisecond, wantTotal: 0},
				{desc: "t+interval+5ms, unchanged", advance: watchInterval, wantTotal: 1},
			},
		},
		{
			name:    "first sighting kept: exactly t+interval publishes",
			content: part1,
			steps: []step{
				{desc: "first sighting at t", advance: watchInterval, wantTotal: 0},
				{desc: "t+10ms, unchanged", advance: 10 * time.Millisecond, wantTotal: 0},
				{desc: "t+interval, unchanged", advance: watchInterval - 10*time.Millisecond, wantTotal: 1},
			},
		},
		{
			name:    "several too-soon polls in a row",
			content: part1,
			steps: []step{
				{desc: "first sighting at t", advance: watchInterval, wantTotal: 0},
				{desc: "t+1ms", advance: time.Millisecond, wantTotal: 0},
				{desc: "t+2ms", advance: time.Millisecond, wantTotal: 0},
				{desc: "t+interval-1ms", advance: watchInterval - 3*time.Millisecond, wantTotal: 0},
				{desc: "t+interval", advance: time.Millisecond, wantTotal: 1},
			},
		},
		{
			name:    "write seen by the too-soon poll restarts the wait from it",
			content: full,
			steps: []step{
				{desc: "first sighting of part 1 at t", advance: watchInterval, wantTotal: 0},
				{desc: "t+10ms sees part 2", advance: 10 * time.Millisecond, appendNow: true, wantTotal: 0},
				{desc: "t+interval: only interval-10ms since the change", advance: watchInterval - 10*time.Millisecond, wantTotal: 0},
				{desc: "t+10ms+interval", advance: 10 * time.Millisecond, wantTotal: 1},
			},
		},
		{
			name:    "write after a too-soon poll restarts the wait",
			content: full,
			steps: []step{
				{desc: "first sighting of part 1 at t", advance: watchInterval, wantTotal: 0},
				{desc: "t+10ms, unchanged", advance: 10 * time.Millisecond, wantTotal: 0},
				{desc: "t+interval sees part 2", advance: watchInterval - 10*time.Millisecond, appendNow: true, wantTotal: 0},
				{desc: "t+interval+10ms, too soon after the change", advance: 10 * time.Millisecond, wantTotal: 0},
				{desc: "t+2*interval", advance: watchInterval - 10*time.Millisecond, wantTotal: 1},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWatchFixture(t, nil)
			f.write("slow.json", part1)
			for _, s := range tc.steps {
				if s.appendNow {
					f.appendTo("slow.json", part2)
				}
				f.pollAfter(s.advance)
				f.requireTotal(s.desc, s.wantTotal)
			}
			f.requirePublishedOnce("end", "slow.json", tc.content)
			if !bytes.Equal(tc.content, part1) {
				for _, env := range f.probe.published() {
					if bytes.Equal(env.Payload, part1) {
						t.Fatalf("published the half-written file: %q", env.Payload)
					}
				}
			}

			f.poll()
			f.requireTotal("after a further poll", 1)
		})
	}
}
