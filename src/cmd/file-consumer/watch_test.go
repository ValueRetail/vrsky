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

// watchFixture is a consumer wired to a recording probe plus one connection
// watching a temp dir. Everything goes inline (no payload store, large
// inlineMax) and db is nil, so the last_payload write is skipped. Polls happen
// only when the test calls poll(); nothing runs in the background.
type watchFixture struct {
	t     *testing.T
	dir   string
	probe *recordingProbe
	c     *fileConsumer
	ac    *ActiveConnection
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
	c := &fileConsumer{
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		publish:   probe.publish,
		inlineMax: 1 << 20,
	}
	ac := &ActiveConnection{
		ConnectionID: "conn-watch",
		TenantID:     "tenant-x",
		WatchDir:     dir,
		Cancel:       func() {},
		knownFiles:   c.listFiles(dir),
	}
	return &watchFixture{t: t, dir: dir, probe: probe, c: c, ac: ac}
}

func (f *watchFixture) poll() {
	f.t.Helper()
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
