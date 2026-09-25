package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newTestWatcher(t *testing.T, g *fakeGateway, c *Config, conns map[string]string) *dirWatcher {
	t.Helper()
	client, _ := NewClient(g.srv.URL, g.credential)
	st, err := LoadState(c.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	w := newDirWatcher("inbox", c.Directories["inbox"], 10*time.Millisecond, client, st, quiet)
	w.setConnections(conns)
	return w
}

// makeOld backdates a file so it is past the "not modified within the scan
// interval" guard.
func makeOld(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// A file is taken only once it has stopped changing: the first scan that
// sees it only notes it; a later scan with the same size and time takes it.
func TestWatcher_WaitsForStableSize(t *testing.T) {
	g := newFakeGateway(t)
	c := testConfig(t)
	w := newTestWatcher(t, g, c, map[string]string{"conn-1": agentproto.AfterMove})
	f := filepath.Join(c.Directories["inbox"].Path, "sale.xml")

	_ = os.WriteFile(f, []byte("<sale>1"), 0o644)
	makeOld(t, f)
	w.scan(context.Background())
	if up, _, _ := g.snapshot(); len(up) != 0 {
		t.Fatal("uploaded on first sight, before knowing the file had stopped changing")
	}

	_ = os.WriteFile(f, []byte("<sale>1</sale>"), 0o644) // still being written
	makeOld(t, f)
	w.scan(context.Background())
	if up, _, _ := g.snapshot(); len(up) != 0 {
		t.Fatal("uploaded a file whose size changed since the last scan")
	}

	w.scan(context.Background()) // unchanged now
	up, _, _ := g.snapshot()
	if len(up) != 1 || up[0].Body != "<sale>1</sale>" || up[0].Filename != "sale.xml" || up[0].ConnectionID != "conn-1" || up[0].Directory != "inbox" {
		t.Fatalf("uploads = %+v", up)
	}
}

func TestWatcher_MoveAfterUpload(t *testing.T) {
	g := newFakeGateway(t)
	c := testConfig(t)
	in := c.Directories["inbox"].Path
	w := newTestWatcher(t, g, c, map[string]string{"conn-1": agentproto.AfterMove})
	f := filepath.Join(in, "a.json")
	_ = os.WriteFile(f, []byte(`{}`), 0o644)
	makeOld(t, f)
	// Files the watcher must never take.
	for _, skip := range []string{".hidden", partPrefix + "probe-123", "writing.part", "copy.tmp"} {
		_ = os.WriteFile(filepath.Join(in, skip), []byte("x"), 0o644)
		makeOld(t, filepath.Join(in, skip))
	}
	w.scan(context.Background())
	w.scan(context.Background())

	if _, err := os.Stat(filepath.Join(in, "processed", "a.json")); err != nil {
		t.Fatalf("not moved into processed/: %v", err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("original still in the read folder")
	}
	up, _, _ := g.snapshot()
	if len(up) != 1 {
		t.Fatalf("uploaded %d files, want only a.json: %+v", len(up), up)
	}

	// Same name again: the earlier copy in processed/ is not overwritten.
	_ = os.WriteFile(f, []byte(`{"v":2}`), 0o644)
	makeOld(t, f)
	w.scan(context.Background())
	w.scan(context.Background())
	if es, _ := os.ReadDir(filepath.Join(in, "processed")); len(es) != 2 {
		t.Fatalf("processed/ holds %d files, want both copies", len(es))
	}
}

// Deleted only when every watching pipeline asked for delete; otherwise kept.
func TestWatcher_DeleteOnlyWhenEveryPipelineSaysSo(t *testing.T) {
	g := newFakeGateway(t)
	c := testConfig(t)
	in := c.Directories["inbox"].Path
	w := newTestWatcher(t, g, c, map[string]string{"c1": agentproto.AfterDelete, "c2": agentproto.AfterMove})
	f := filepath.Join(in, "b.csv")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	makeOld(t, f)
	w.scan(context.Background())
	w.scan(context.Background())
	if up, _, _ := g.snapshot(); len(up) != 2 {
		t.Fatalf("want one upload per watching pipeline, got %+v", up)
	}
	if _, err := os.Stat(filepath.Join(in, "processed", "b.csv")); err != nil {
		t.Fatal("mixed move/delete should keep the file in processed/")
	}

	w.setConnections(map[string]string{"c1": agentproto.AfterDelete})
	_ = os.WriteFile(f, []byte("y"), 0o644)
	makeOld(t, f)
	w.scan(context.Background())
	w.scan(context.Background())
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("not deleted")
	}
	if es, _ := os.ReadDir(filepath.Join(in, "processed")); len(es) != 1 {
		t.Fatalf("processed/ holds %d files; the deleted one must not have been kept too", len(es))
	}
}

// Uploaded, but could not be cleared (here: processed/ cannot be created).
// A restarted watcher sharing the state file must not upload it again.
func TestWatcher_DoesNotReingestAfterRestart(t *testing.T) {
	g := newFakeGateway(t)
	c := testConfig(t)
	in := c.Directories["inbox"].Path
	_ = os.WriteFile(filepath.Join(in, "processed"), []byte("not a folder"), 0o644) // blocks the move
	f := filepath.Join(in, "c.json")
	_ = os.WriteFile(f, []byte(`{"c":1}`), 0o644)
	makeOld(t, f)

	// The blocker is itself a file in the inbox, and the watcher may take it
	// too once it is older than the scan interval (slow CI). Count only c.json.
	uploadsOfC := func() int {
		up, _, _ := g.snapshot()
		n := 0
		for _, u := range up {
			if u.Filename == "c.json" {
				n++
			}
		}
		return n
	}

	w := newTestWatcher(t, g, c, map[string]string{"conn-1": agentproto.AfterMove})
	w.scan(context.Background())
	w.scan(context.Background())
	if n := uploadsOfC(); n != 1 {
		t.Fatalf("setup: %d uploads", n)
	}

	restarted := newTestWatcher(t, g, c, map[string]string{"conn-1": agentproto.AfterMove})
	restarted.scan(context.Background())
	restarted.scan(context.Background())
	restarted.scan(context.Background())
	if n := uploadsOfC(); n != 1 {
		t.Fatalf("after a restart the file was uploaded again: %d uploads", n)
	}

	_ = os.Remove(filepath.Join(in, "processed")) // the obstacle clears
	restarted.scan(context.Background())
	if _, err := os.Stat(filepath.Join(in, "processed", "c.json")); err != nil {
		t.Fatalf("the file was not cleared once possible: %v", err)
	}
	if n := uploadsOfC(); n != 1 {
		t.Fatalf("clearing it uploaded it again: %d uploads", n)
	}
}

// Every retry of one file carries the same upload ID, so VRSky drops a
// duplicate when a retry follows an upload whose answer was lost.
func TestWatcher_RetriesKeepTheUploadID(t *testing.T) {
	g := newFakeGateway(t)
	c := testConfig(t)
	f := filepath.Join(c.Directories["inbox"].Path, "d.json")
	_ = os.WriteFile(f, []byte(`{}`), 0o644)
	makeOld(t, f)
	w := newTestWatcher(t, g, c, map[string]string{"conn-1": agentproto.AfterMove})
	st := w.state

	g.set(func(g *fakeGateway) { g.uploadErr, g.uploadCode = http.StatusServiceUnavailable, "unavailable" })
	w.scan(context.Background())
	w.scan(context.Background())
	key := "conn-1|inbox|d.json"
	var firstID string
	for k, r := range st.data {
		if strings.HasPrefix(k, key) {
			firstID = r.UploadID
		}
	}
	if firstID == "" {
		t.Fatal("no upload ID recorded for the failed attempt")
	}

	g.set(func(g *fakeGateway) { g.uploadErr = 0 })
	w.scan(context.Background())
	up, _, _ := g.snapshot()
	if len(up) != 1 || up[0].UploadID != firstID {
		t.Fatalf("retry used upload ID %q, first attempt %q", up[0].UploadID, firstID)
	}
}

// A file VRSky will never accept goes to rejected/ with a note, instead of
// being retried every few seconds forever.
func TestWatcher_PermanentRefusalMovesToRejected(t *testing.T) {
	g := newFakeGateway(t)
	c := testConfig(t)
	in := c.Directories["inbox"].Path
	f := filepath.Join(in, "huge.bin")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	makeOld(t, f)
	g.set(func(g *fakeGateway) {
		g.uploadErr, g.uploadCode = http.StatusRequestEntityTooLarge, agentproto.ErrTooLarge
	})
	w := newTestWatcher(t, g, c, map[string]string{"conn-1": agentproto.AfterMove})
	w.scan(context.Background())
	w.scan(context.Background())
	if _, err := os.Stat(filepath.Join(in, "rejected", "huge.bin")); err != nil {
		t.Fatalf("not moved to rejected/: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(in, "rejected", "huge.bin.reason.txt")); !strings.Contains(string(b), "413") {
		t.Errorf("reason note = %q", b)
	}
}
