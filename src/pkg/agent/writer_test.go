package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	es, _ := os.ReadDir(dir)
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func TestWrite_WritesVerifiedFile(t *testing.T) {
	c := testConfig(t)
	path, err := WriteFile(c, "outbox", "orders.json", "d-1", sum(`{"a":1}`), strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"a":1}` {
		t.Errorf("content = %q", b)
	}
	if names := listDir(t, c.Directories["outbox"].Path); len(names) != 1 {
		t.Errorf("folder holds %v, want only the file (no temp left behind)", names)
	}
	// A second delivery of the same name replaces it.
	if _, err := WriteFile(c, "outbox", "orders.json", "d-2", sum("v2"), strings.NewReader("v2")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "v2" {
		t.Errorf("replacement content = %q", b)
	}
}

// Nothing VRSky sends can write outside a configured write folder.
func TestWrite_RejectsTraversalAndUnknownDirectory(t *testing.T) {
	c := testConfig(t)
	base := filepath.Dir(c.Directories["outbox"].Path)
	cases := []struct{ dir, name, id string }{
		{"outbox", "../escaped.txt", "d-1"},
		{"outbox", `..\escaped.txt`, "d-1"},
		{"outbox", "sub/escaped.txt", "d-1"},
		{"outbox", "CON", "d-1"},
		{"inbox", "ok.txt", "d-1"},   // a read folder
		{"secret", "ok.txt", "d-1"},  // not in the config
		{"outbox", "ok.txt", "../x"}, // delivery id used in the temp name
	}
	for _, c2 := range cases {
		if _, err := WriteFile(c, c2.dir, c2.name, c2.id, "", strings.NewReader("x")); err == nil {
			t.Errorf("WriteFile(%q, %q, id %q) succeeded", c2.dir, c2.name, c2.id)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "escaped.txt")); err == nil {
		t.Fatal("a file was written outside the folder")
	}
	if names := listDir(t, c.Directories["outbox"].Path); len(names) != 0 {
		t.Errorf("rejected writes left %v behind", names)
	}
}

func TestWrite_ChecksumMismatchLeavesNothing(t *testing.T) {
	c := testConfig(t)
	_, err := WriteFile(c, "outbox", "orders.json", "d-1", sum("expected"), strings.NewReader("corrupted"))
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want a checksum error", err)
	}
	if names := listDir(t, c.Directories["outbox"].Path); len(names) != 0 {
		t.Errorf("a corrupt delivery left %v behind", names)
	}
}

// failingReader delivers some bytes then fails, as a dropped connection does.
type failingReader struct{ sent bool }

func (f *failingReader) Read(p []byte) (int, error) {
	if !f.sent {
		f.sent = true
		return copy(p, "partial data"), nil
	}
	return 0, errors.New("connection reset")
}

// While a file is arriving it exists only under a temp name that watchers and
// readers skip, and a failed transfer leaves nothing.
func TestWrite_AtomicNoPartialVisible(t *testing.T) {
	c := testConfig(t)
	out := c.Directories["outbox"].Path

	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := WriteFile(c, "outbox", "big.csv", "d-9", sum("aaaa"+"bbbb"), pr)
		done <- err
	}()
	_, _ = pw.Write([]byte("aaaa")) // half the file is in
	if _, err := os.Stat(filepath.Join(out, "big.csv")); err == nil {
		t.Fatal("the file is visible under its real name before it is complete")
	}
	if names := listDir(t, out); len(names) != 1 || !strings.HasPrefix(names[0], partPrefix) || !ignored(names[0]) {
		t.Fatalf("while writing, folder holds %v; want one temp file a watcher ignores", names)
	}
	_, _ = pw.Write([]byte("bbbb"))
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if names := listDir(t, out); len(names) != 1 || names[0] != "big.csv" {
		t.Fatalf("after writing, folder holds %v", names)
	}

	if _, err := WriteFile(c, "outbox", "cut.csv", "d-10", "", &failingReader{}); err == nil {
		t.Fatal("a dropped transfer reported success")
	}
	if names := listDir(t, out); len(names) != 1 {
		t.Errorf("a dropped transfer left %v behind", names)
	}
}
