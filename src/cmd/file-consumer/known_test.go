package main

import (
	"context"
	"encoding/json"
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
	if err := c.loadKnown(context.Background(), ac, false); err != nil {
		t.Fatalf("loadKnown: %v", err)
	}
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

// failingReadStore fails every read until readable is set, and counts saves.
type failingReadStore struct {
	*lockedStore
	saves    atomic.Int32
	readable atomic.Bool
}

func (s *failingReadStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	if s.readable.Load() {
		return s.lockedStore.Get(ctx, tenantID, connectionID, nodeID)
	}
	return nil, errors.New("database unavailable")
}

func (s *failingReadStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	s.saves.Add(1)
	return s.lockedStore.Save(ctx, cp)
}

// A start whose checkpoint read fails falls back to "everything present is
// done" for this run, but must not save that: it would overwrite the saved
// history, and another watcher re-reading it could skip a file it should
// publish. Nothing is saved until the saved set can be read (#536); then the
// first save writes the set, with the guess checked against what was saved.
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
	// The publish made the set dirty, but the saved set still can't be read,
	// so the guess is not written over it (#536).
	if got := store.saves.Load(); got != 0 {
		t.Fatalf("%d checkpoint row writes while reads fail, want 0", got)
	}

	// Reads work again. Nothing is saved for this folder, so the guess is the
	// first-start rule and stands: one save, a snapshot and the empty changes
	// row on it (#426), and old.json is still not published.
	store.readable.Store(true)
	clock.advance(time.Hour)
	f.c.pollOnce(context.Background(), ac)
	if got := store.saves.Load(); got != 2 {
		t.Errorf("%d checkpoint row writes once reads work, want 2 (one snapshot, its changes row)", got)
	}
	if got := len(f.probe.forName("old.json")); got != 0 {
		t.Errorf("old.json published %d times after reads came back, want 0", got)
	}
}

// writeAt writes content to dir/name and sets its mtime.
func writeAt(t *testing.T, dir, name string, content []byte, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// Both pods of a rolling deploy see a published file replaced. The one that
// publishes the new version first saves its stamp; the other must find that
// stamp on the file and not publish it again (#420).
func TestKnown_SecondWatcherSkipsAReplacementTheFirstPublished(t *testing.T) {
	dir := t.TempDir()
	store := newLockedStore()
	oldProbe, newProbe := &recordingProbe{}, &recordingProbe{}
	v1, v2 := []byte(`{"order":1}`), []byte(`{"order":1,"amended":true}`)
	mtime := time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)

	oldPod := newKnownWatcher(t, dir, store, oldProbe.publish)
	writeAt(t, dir, "order.json", v1, mtime)
	ctx := context.Background()
	oldPod.poll(ctx)
	oldPod.poll(ctx)
	newPod := newKnownWatcher(t, dir, store, newProbe.publish) // loads v1 as done

	writeAt(t, dir, "order.json", v2, mtime.Add(time.Hour))
	oldPod.poll(ctx)
	newPod.poll(ctx)
	oldPod.poll(ctx)
	if got := oldProbe.forName("order.json"); len(got) != 2 {
		t.Fatalf("old pod published order.json %d times, want 2 (%s)", len(got), describe(got))
	}
	assertWatchEnvelope(t, oldProbe.forName("order.json")[1], "order.json", v2)
	for i := 0; i < 3; i++ {
		newPod.poll(ctx)
	}
	if got := newProbe.published(); len(got) != 0 {
		t.Fatalf("new pod published %d envelopes, want 0 (%s)", len(got), describe(got))
	}

	// A third version is still published, by the pod that sees it settle.
	v3 := []byte(`{"order":1,"amended":2}`)
	writeAt(t, dir, "order.json", v3, mtime.Add(2*time.Hour))
	newPod.poll(ctx)
	newPod.poll(ctx)
	if got := newProbe.forName("order.json"); len(got) != 1 {
		t.Fatalf("new pod published the third version %d times, want 1 (%s)", len(got), describe(got))
	}
	assertWatchEnvelope(t, newProbe.forName("order.json")[0], "order.json", v3)
}

// The first start after the upgrade reads a name-only checkpoint (before
// #420) and adopts the files' stamps. It must save them even in a quiet
// folder: a file then replaced while stopped (the cluster parked overnight)
// is published at the next start, not adopted again.
func TestKnown_OldFormatRowGetsStampsAtFirstStart(t *testing.T) {
	dir := t.TempDir()
	store := newLockedStore()
	mtime := time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)
	writeAt(t, dir, "orders.csv", []byte("id\n1\n"), mtime)
	old, err := json.Marshal(map[string]any{"watch_dir": dir, "known": []string{"orders.csv"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), &checkpoint.Checkpoint{
		TenantID: knownTenant, ConnectionID: knownConn, NodeID: knownNode, State: old,
	}); err != nil {
		t.Fatal(err)
	}

	probe := &recordingProbe{}
	ctx := context.Background()
	first := newKnownWatcher(t, dir, store, probe.publish)
	for i := 0; i < 5; i++ {
		first.poll(ctx)
	}
	if got := probe.published(); len(got) != 0 {
		t.Fatalf("published %d envelopes from the old-format start, want 0 (%s)", len(got), describe(got))
	}
	cp, err := store.Get(ctx, knownTenant, knownConn, knownNode)
	if err != nil || cp == nil {
		t.Fatalf("read checkpoint: %v, %v", cp, err)
	}
	var st knownState
	if err := json.Unmarshal(cp.State, &st); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Files["orders.csv"]; !ok {
		t.Fatalf("checkpoint after the first start has no stamp for orders.csv: %s", cp.State)
	}

	// Stopped; the nightly export overwrites the file; started again.
	v2 := []byte("id\n1\n2\n")
	writeAt(t, dir, "orders.csv", v2, mtime.Add(24*time.Hour))
	next := newKnownWatcher(t, dir, store, probe.publish)
	for i := 0; i < 5; i++ {
		next.poll(ctx)
	}
	envs := probe.forName("orders.csv")
	if len(envs) != 1 {
		t.Fatalf("orders.csv published %d times after being replaced while stopped, want 1 (%s)", len(envs), describe(probe.published()))
	}
	assertWatchEnvelope(t, envs[0], "orders.csv", v2)
}

// A cut-off publish of a replacement puts the old stamp back, rather than
// dropping the name: if the old version is then restored while stopped,
// the next start finds the stamp it published and sends nothing.
func TestKnown_ReplacementCutOffKeepsTheOldStamp(t *testing.T) {
	dir := t.TempDir()
	store := newLockedStore()
	v1, v2 := []byte(`{"v":1}`), []byte(`{"v":22}`)
	mtime := time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)

	probe := &recordingProbe{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cutOff := false
	publish := func(pctx context.Context, env *envelope.Envelope) error {
		if cutOff {
			cancel()
			return pctx.Err()
		}
		return probe.publish(pctx, env)
	}
	first := newKnownWatcher(t, dir, store, publish)
	writeAt(t, dir, "x.json", v1, mtime)
	first.poll(ctx)
	first.poll(ctx)
	writeAt(t, dir, "x.json", v2, mtime.Add(time.Hour))
	cutOff = true
	first.poll(ctx)
	first.poll(ctx)

	writeAt(t, dir, "x.json", v1, mtime) // the old version back, as published
	next := newKnownWatcher(t, dir, store, probe.publish)
	for i := 0; i < 3; i++ {
		next.poll(context.Background())
	}
	if envs := probe.forName("x.json"); len(envs) != 1 {
		t.Fatalf("x.json published %d times in all, want 1 (%s)", len(envs), describe(probe.published()))
	}
}

// The other pod published a version this one never saw, and the files have
// changed again since. Its saved stamp no longer matches the files, so this
// pod must not take it as done or drop what it has pending: both files go
// out on the poll they settle, not polls later.
func TestKnown_StaleSavedStampDoesNotDelayANewerVersion(t *testing.T) {
	dir := t.TempDir()
	store := newLockedStore()
	probeA, probeB := &recordingProbe{}, &recordingProbe{}
	mtime := time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)
	names := []string{"a.json", "b.json"}
	ctx := context.Background()

	podA := newKnownWatcher(t, dir, store, probeA.publish)
	for _, n := range names {
		writeAt(t, dir, n, []byte(`{"v":1}`), mtime)
	}
	podA.poll(ctx)
	podA.poll(ctx)
	podB := newKnownWatcher(t, dir, store, probeB.publish) // knows v1

	for _, n := range names {
		writeAt(t, dir, n, []byte(`{"v":2}`), mtime.Add(time.Hour))
	}
	podA.poll(ctx)
	podA.poll(ctx) // publishes v2 and saves it; B never sees v2

	v3 := []byte(`{"v":333}`)
	for _, n := range names {
		writeAt(t, dir, n, v3, mtime.Add(2*time.Hour))
	}
	podB.poll(ctx) // v3 seen once
	podB.poll(ctx) // v3 settled: both published now
	for _, n := range names {
		envs := probeB.forName(n)
		if len(envs) != 1 {
			t.Fatalf("pod B published %s %d times on the poll v3 settled, want 1 (%s)", n, len(envs), describe(probeB.published()))
		}
		assertWatchEnvelope(t, envs[0], n, v3)
	}
}

// A stop that cuts off the publish of a replaced file must leave the old
// stamp saved, so the next start still sees the change and publishes it.
func TestKnown_ReplacementCutOffByStopIsPublishedByNextStart(t *testing.T) {
	dir := t.TempDir()
	store := newLockedStore()
	v1, v2 := []byte(`{"v":1}`), []byte(`{"v":2}`)
	mtime := time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)

	probe := &recordingProbe{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cutOff := false
	publish := func(pctx context.Context, env *envelope.Envelope) error {
		if cutOff {
			cancel() // the stop lands mid-publish
			return pctx.Err()
		}
		return probe.publish(pctx, env)
	}
	first := newKnownWatcher(t, dir, store, publish)
	writeAt(t, dir, "x.json", v1, mtime)
	first.poll(ctx)
	first.poll(ctx)
	if got := len(probe.forName("x.json")); got != 1 {
		t.Fatalf("x.json published %d times before the replacement, want 1", got)
	}

	writeAt(t, dir, "x.json", v2, mtime.Add(time.Hour))
	cutOff = true
	first.poll(ctx)
	first.poll(ctx)

	next := newKnownWatcher(t, dir, store, probe.publish)
	bg := context.Background()
	for i := 0; i < 3; i++ {
		next.poll(bg)
	}
	envs := probe.forName("x.json")
	if len(envs) != 2 {
		t.Fatalf("x.json published %d times in all, want 2 (%s)", len(envs), describe(probe.published()))
	}
	assertWatchEnvelope(t, envs[1], "x.json", v2)
}
