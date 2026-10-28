package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Two behaviours of #391 the restart tests do not reach: two watchers for one
// connection at once (the rolling-deploy overlap), and a publish cut off by a
// stop. Polls are driven by hand on a fake clock, as in watch_test.go.

const knownConn, knownTenant, knownNode = "conn-known", "tenant-known", "c1"

// knownWatcher is one consumer (one pod) with its own watcher for the shared
// connection, folder and checkpoint store.
type knownWatcher struct {
	c     *fileConsumer
	ac    *ActiveConnection
	clock *fakeClock
}

func newKnownWatcher(t *testing.T, dir string, store *lockedStore, publish sdk.PublishFunc) *knownWatcher {
	t.Helper()
	clock := newFakeClock()
	c := &fileConsumer{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		publish:      publish,
		inlineMax:    1 << 20,
		pollInterval: watchInterval,
		now:          clock.now,
		checkpoints:  store,
	}
	ac := &ActiveConnection{ConnectionID: knownConn, TenantID: knownTenant, WatchDir: dir, NodeID: knownNode, Cancel: func() {}}
	c.loadKnown(context.Background(), ac)
	c.saveKnown(context.Background(), ac, c.logger)
	return &knownWatcher{c: c, ac: ac, clock: clock}
}

func (w *knownWatcher) poll(ctx context.Context) {
	w.clock.advance(watchInterval)
	w.c.pollOnce(ctx, w.ac)
}

// The old and the new pod both watch the folder for a few seconds in a
// rolling deploy. A file the old pod publishes after the new one loaded its
// done set must not be published by the new one too.
func TestKnown_SecondWatcherSkipsWhatTheFirstPublished(t *testing.T) {
	dir := t.TempDir()
	store := newLockedStore()
	oldProbe, newProbe := &recordingProbe{}, &recordingProbe{}

	oldPod := newKnownWatcher(t, dir, store, oldProbe.publish) // first start
	newPod := newKnownWatcher(t, dir, store, newProbe.publish) // loads the saved (empty) set

	content := []byte(`{"order":1}`)
	if err := os.WriteFile(filepath.Join(dir, "order.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Both see it once; the old pod publishes it on its second look.
	oldPod.poll(ctx)
	newPod.poll(ctx)
	oldPod.poll(ctx)
	if got := len(oldProbe.forName("order.json")); got != 1 {
		t.Fatalf("old pod published order.json %d times, want 1", got)
	}
	for i := 0; i < 3; i++ {
		newPod.poll(ctx)
	}
	if got := newProbe.published(); len(got) != 0 {
		t.Fatalf("new pod published %d envelopes, want 0 (%s)", len(got), describe(got))
	}
	assertWatchEnvelope(t, oldProbe.forName("order.json")[0], "order.json", content)
}

// A stop that cancels the watcher while a file is being published must not
// record that file as done: the next start publishes it.
func TestKnown_PublishCutOffByStopIsPublishedByNextStart(t *testing.T) {
	dir := t.TempDir()
	store := newLockedStore()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	cutOff := func(ctx context.Context, _ *envelope.Envelope) error {
		attempts++
		cancel() // the stop lands mid-publish
		return ctx.Err()
	}
	first := newKnownWatcher(t, dir, store, cutOff)

	files := map[string][]byte{"a.json": []byte(`{"a":1}`), "b.json": []byte(`{"b":2}`)}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first.poll(ctx)
	first.poll(ctx)
	if attempts != 1 {
		t.Fatalf("%d publish attempts after the stop, want 1 (the poll must stop with the watcher)", attempts)
	}

	probe := &recordingProbe{}
	next := newKnownWatcher(t, dir, store, probe.publish)
	bg := context.Background()
	for i := 0; i < 3; i++ {
		next.poll(bg)
	}
	for name, content := range files {
		envs := probe.forName(name)
		if len(envs) != 1 {
			t.Fatalf("%s published %d times after the restart, want 1 (%s)", name, len(envs), describe(probe.published()))
		}
		assertWatchEnvelope(t, envs[0], name, content)
	}
}

// failingReadStore fails every read and counts saves.
type failingReadStore struct {
	*lockedStore
	saves atomic.Int32
}

func (s *failingReadStore) Get(context.Context, string, string, string) (*checkpoint.Checkpoint, error) {
	return nil, errors.New("database unavailable")
}

func (s *failingReadStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	s.saves.Add(1)
	return s.lockedStore.Save(ctx, cp)
}

// A start whose checkpoint read fails falls back to "everything present is
// done" for this run, but must not save that: it would overwrite the saved
// history, and another watcher re-reading it could skip a file it should
// publish. The first real change saves.
func TestKnown_FailedReadDoesNotOverwriteSavedSet(t *testing.T) {
	f := newRaceFixture(t)
	if err := os.MkdirAll(f.inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.inbox, "old.json"), []byte(`{"old":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &failingReadStore{lockedStore: newLockedStore()}
	clock := newFakeClock()
	f.c.checkpoints = store
	f.c.now = clock.now
	f.c.pollInterval = time.Hour // the background watcher never polls; the test does

	f.c.startConnection(context.Background(), raceConnID, raceTenant)
	ac := f.c.getActiveConnection(raceConnID)
	if ac == nil {
		t.Fatal("connection not active after startConnection")
	}
	if got := store.saves.Load(); got != 0 {
		t.Fatalf("%d checkpoint saves at start after a failed read, want 0", got)
	}

	content := []byte(`{"new":1}`)
	if err := os.WriteFile(filepath.Join(f.inbox, "new.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		clock.advance(time.Hour)
		f.c.pollOnce(context.Background(), ac)
	}
	if got := len(f.probe.forName("old.json")); got != 0 {
		t.Errorf("old.json published %d times, want 0 (the fallback treats it as done)", got)
	}
	envs := f.probe.forName("new.json")
	if len(envs) != 1 {
		t.Fatalf("new.json published %d times, want 1 (%s)", len(envs), describe(f.probe.published()))
	}
	assertWatchEnvelope(t, envs[0], "new.json", content)
	if got := store.saves.Load(); got != 1 {
		t.Errorf("%d checkpoint saves after the publish, want 1", got)
	}
}
