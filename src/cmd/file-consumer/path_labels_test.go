package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/tenantpath"
)

// Tests for #499: two more file-consumer messages showed the path on the
// volume (/data/input/<tenant-id>/…) to whoever had the workspace open.
//
//   - The "error" event a watched file sends when it can't be published. It
//     carried the Go error, which names the file's absolute path (os.ReadFile)
//     or the tenant's NATS subject (a failed publish).
//   - The sample-data endpoint's "error" text, which named the absolute folder
//     ("directory does not exist: …", "no readable files in …") or carried the
//     os.ReadDir / os.Open text.
//
// Both must name the folder relative to the tenant root, as folderLabel does
// for the "connected" event (#455), keep only the operating system's reason,
// and leave the full Go error to the log.
//
// The watched-file tests drive the real pollOnce on a dirRig (dirread_test.go)
// with real folder reads: a file is published on the second sighting, one
// poll interval after the first. The sample tests call the real
// handleSampleData through httptest.

// pathLabelTenant is distinctive, so finding it in a message means the
// tenant's directory (or subject) leaked. No folder or file name below
// contains it.
const pathLabelTenant = "tenant-pl499-zq"

// leak is a string a user-facing message must not contain.
type leak struct{ what, s string }

func requireNoLeaks(t *testing.T, msg string, leaks []leak) {
	t.Helper()
	for _, l := range leaks {
		if l.s != "" && strings.Contains(msg, l.s) {
			t.Errorf("message %q contains the %s %q", msg, l.what, l.s)
		}
	}
}

// namesToken reports whether msg names tok as a whole word: quoted, or
// bounded by spaces and punctuation. It pins what is shown, not how it is
// quoted, and keeps "." from matching the dot in a file name.
func namesToken(msg, tok string) bool {
	re := regexp.MustCompile(`(^|[\s"'\x60(])` + regexp.QuoteMeta(tok) + `($|[\s"'\x60):;,.])`)
	return re.MatchString(msg)
}

// skipIfRoot skips a case that needs a permission check to fail.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not stop reads")
	}
}

// lockUntilCleanup sets path's mode to 000 and puts back mode when the test
// ends, so t.TempDir can remove it.
func lockUntilCleanup(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("chmod 000 %s: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, mode) })
}

// --- the watched file's error event (pollOnce) -------------------------------

func TestPathLabels_WatchedFileErrorEvent(t *testing.T) {
	const name = "order-77.json"
	content := []byte(`{"order":77}`)
	// bigContent is over streamInlineMax, so a streaming worker sends it from
	// disk through publishStream instead of reading it into memory.
	const streamInlineMax = 64
	bigContent := []byte(`{"order":77,"pad":"` + strings.Repeat("x", 4*streamInlineMax) + `"}`)

	type ingestCase struct {
		name  string
		path  string // the file node's configured path
		label string // the folder the message must name
		// prepare makes the file fail once it is published; it gets the rig
		// and the file's absolute path.
		prepare func(t *testing.T, d *dirRig, file string)
		// publishErr, when set, is what the publish func fails with.
		publishErr func(d *dirRig, file string) error
		// stream makes the worker stream: inlineMax is streamInlineMax, the
		// file is bigContent and publishStream fails with streamErr (or
		// succeeds when streamErr is nil).
		stream    bool
		streamErr func(d *dirRig, file string) error
		// reason must appear in the message; empty when none may be shown.
		reason []string
		// forbid are more strings the message must not contain.
		forbid []leak
		// logged must appear in the logged error.
		logged func(d *dirRig, file string) string
	}
	cases := []ingestCase{
		{
			name:  "unreadable file, nested folder",
			path:  watchedPath,
			label: "orders/in",
			prepare: func(t *testing.T, _ *dirRig, file string) {
				skipIfRoot(t)
				lockUntilCleanup(t, file, 0o600)
			},
			reason: []string{"permission denied"},
			logged: func(_ *dirRig, file string) string { return file },
		},
		{
			name:  "unreadable file, tenant root",
			path:  ".",
			label: ".",
			prepare: func(t *testing.T, _ *dirRig, file string) {
				skipIfRoot(t)
				lockUntilCleanup(t, file, 0o600)
			},
			reason: []string{"permission denied"},
			logged: func(_ *dirRig, file string) string { return file },
		},
		{
			name:  "publish fails, nested folder",
			path:  watchedPath,
			label: "orders/in",
			publishErr: func(d *dirRig, file string) error {
				return fmt.Errorf("js.Publish vrsky.%s.%s.data: nats: timeout (file %s)", d.tenant, d.connID, file)
			},
			logged: func(d *dirRig, _ string) string { return "js.Publish vrsky." + d.tenant + "." },
		},
		{
			name:  "publish fails, tenant root",
			path:  ".",
			label: ".",
			publishErr: func(d *dirRig, file string) error {
				return fmt.Errorf("js.Publish vrsky.%s.%s.data: nats: timeout (file %s)", d.tenant, d.connID, file)
			},
			logged: func(d *dirRig, _ string) string { return "js.Publish vrsky." + d.tenant + "." },
		},
		{
			name:  "over the size limit on a worker that cannot stream",
			path:  watchedPath,
			label: "orders/in",
			prepare: func(t *testing.T, _ *dirRig, file string) {
				// Sparse where the filesystem allows it: nothing is read,
				// the size check comes first.
				if err := os.Truncate(file, maxWatchFileBytes+1); err != nil {
					t.Fatalf("grow %s: %v", file, err)
				}
			},
			reason: []string{"limit", strconv.Itoa(maxWatchFileBytes)},
			logged: func(_ *dirRig, _ string) string { return "limit" },
		},
	}

	// The streaming path (ADR 0001), in a nested folder and in the tenant
	// root. Its errors come from os.Open ("open: %w") and from the SDK's
	// payload store, which names the spill key ("stream payload %q: %w").
	spillKey := func(d *dirRig) string { return "spill/" + d.tenant + "/" + d.connID + "/x" }
	for _, at := range []struct{ where, path, label string }{
		{"nested folder", watchedPath, "orders/in"},
		{"tenant root", ".", "."},
	} {
		cases = append(cases,
			ingestCase{
				name:  "streaming, unreadable file, " + at.where,
				path:  at.path,
				label: at.label,
				prepare: func(t *testing.T, _ *dirRig, file string) {
					skipIfRoot(t)
					lockUntilCleanup(t, file, 0o600)
				},
				stream: true,
				reason: []string{"permission denied"},
				// Only the streaming branch's os.Open wraps it this way, so
				// the case fails if the file was read without streaming.
				logged: func(_ *dirRig, file string) string { return "open: open " + file },
			},
			ingestCase{
				name:   "streaming, payload store fails, " + at.where,
				path:   at.path,
				label:  at.label,
				stream: true,
				streamErr: func(d *dirRig, _ string) error {
					return fmt.Errorf("stream payload %q: %w", spillKey(d), errors.New("store unavailable"))
				},
				reason: []string{"reason is in the file-consumer log"},
				forbid: []leak{{"spill key", "spill/"}, {"SDK error", "stream payload"}, {"store error", "store unavailable"}},
				logged: func(d *dirRig, _ string) string { return spillKey(d) },
			},
			ingestCase{
				name:   "streaming, file read fails mid-stream, " + at.where,
				path:   at.path,
				label:  at.label,
				stream: true,
				streamErr: func(d *dirRig, file string) error {
					return fmt.Errorf("stream payload %q: %w", spillKey(d),
						&fs.PathError{Op: "read", Path: file, Err: syscall.EIO})
				},
				reason: []string{"Could not read", syscall.EIO.Error()},
				forbid: []leak{{"spill key", "spill/"}, {"SDK error", "stream payload"}},
				logged: func(_ *dirRig, file string) string { return file },
			},
		)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDirRig(t)
			d.tenant = pathLabelTenant
			d.path = tc.path
			d.realReads = true
			if err := os.MkdirAll(d.dir(), 0o755); err != nil {
				t.Fatal(err)
			}

			p := d.newPod()
			p.c.eventSubs = make(map[string][]chan FileEvent)
			if tc.publishErr != nil {
				perr := tc.publishErr(d, d.file(name))
				p.c.publish = func(context.Context, *envelope.Envelope) error { return perr }
			}
			data := content
			if tc.stream {
				data = bigContent
				var serr error
				if tc.streamErr != nil {
					serr = tc.streamErr(d, d.file(name))
				}
				p.c.inlineMax = streamInlineMax
				p.c.publishStream = func(_ context.Context, _ *envelope.Envelope, body io.Reader) error {
					if serr != nil {
						return serr
					}
					_, err := io.Copy(io.Discard, body)
					return err
				}
			}
			events, unsub := p.c.subscribeEvents(d.connID)
			t.Cleanup(unsub)
			if !d.startOn(p) {
				t.Fatalf("start: connection not active\nlogs:\n%s", d.sink.dump())
			}
			ac := d.active()

			root, err := tenantpath.Root(d.base, d.tenant)
			if err != nil {
				t.Fatalf("tenant root: %v", err)
			}
			if want := filepath.Join(root, filepath.FromSlash(tc.label)); ac.WatchDir != want {
				t.Fatalf("watch folder %q, want %q", ac.WatchDir, want)
			}

			d.write(name, data)
			if tc.prepare != nil {
				tc.prepare(t, d, d.file(name))
			}
			d.polls(2) // sighted, then published
			d.requireTotal("after the publish attempt", 0)

			errs := ofType(drainEvents(events), "error")
			if len(errs) != 1 {
				t.Fatalf("%d error events, want 1: %+v\nlogs:\n%s", len(errs), errs, d.sink.dump())
			}
			ev := errs[0]
			if ev.Filename != name {
				t.Errorf("error event filename %q, want %q", ev.Filename, name)
			}
			msg := ev.Message
			if !namesToken(msg, name) {
				t.Errorf("message %q does not name the file %q", msg, name)
			}
			if !namesToken(msg, tc.label) {
				t.Errorf("message %q does not name the folder %q (relative to the tenant root)", msg, tc.label)
			}
			for _, r := range tc.reason {
				if !strings.Contains(msg, r) {
					t.Errorf("message %q does not give %q", msg, r)
				}
			}
			leaks := []leak{
				{"volume root", d.base},
				{"tenant root", root},
				{"absolute watch folder", ac.WatchDir},
				{"tenant id", d.tenant},
			}
			if tc.publishErr != nil {
				// None of the publish error's text: it carries the subject.
				leaks = append(leaks, leak{"publish error", "js.Publish"}, leak{"NATS error", "nats:"})
			}
			leaks = append(leaks, tc.forbid...)
			requireNoLeaks(t, msg, leaks)

			// The full error is in the log.
			want := tc.logged(d, d.file(name))
			found := false
			d.sink.mu.Lock()
			for _, r := range d.sink.recs {
				if r.msg == "Failed to ingest detected file" && r.level >= slog.LevelError &&
					r.attrs["file"] == name && strings.Contains(r.attrs["error"], want) {
					found = true
				}
			}
			d.sink.mu.Unlock()
			if !found {
				t.Errorf("no error log with the full error (containing %q)\nlogs:\n%s", want, d.sink.dump())
			}
		})
	}
}

// --- the sample-data endpoint (handleSampleData) -----------------------------

// sampleResponse is what handleSampleData writes.
type sampleResponse struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error"`
	Filename string `json:"filename"`
}

func TestPathLabels_SampleDataErrors(t *testing.T) {
	writeFile := func(t *testing.T, path string, content string, mtime time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	older := time.Now().Add(-time.Hour)
	newer := time.Now()

	cases := []struct {
		name   string
		path   string // the requested path
		post   bool   // send it as a POST JSON body instead of a GET query
		label  string // the folder the message must name
		file   string // the file it must name, if any
		reason []string
		// anyReason: at least one of these must appear.
		anyReason []string
		// prepare lays out the tenant's files; folder is the absolute folder.
		prepare func(t *testing.T, folder string)
		wantOK  bool
	}{
		{
			name:      "missing folder",
			path:      "orders/missing",
			label:     "orders/missing",
			anyReason: []string{"does not exist", "no such file or directory"},
			prepare: func(t *testing.T, folder string) {
				if err := os.MkdirAll(filepath.Dir(folder), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:      "missing folder, POST",
			path:      "orders/missing",
			post:      true,
			label:     "orders/missing",
			anyReason: []string{"does not exist", "no such file or directory"},
		},
		{
			name:  "empty folder (hidden files and sub-folders only)",
			path:  "orders/in",
			label: "orders/in",
			prepare: func(t *testing.T, folder string) {
				writeFile(t, filepath.Join(folder, ".hidden.json"), `{"h":1}`, newer)
				if err := os.MkdirAll(filepath.Join(folder, "archive"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "empty tenant root",
			path:  ".",
			label: ".",
			prepare: func(t *testing.T, folder string) {
				writeFile(t, filepath.Join(folder, ".keep"), "", newer)
			},
		},
		{
			name:   "unreadable folder",
			path:   "orders/in",
			label:  "orders/in",
			reason: []string{"permission denied"},
			prepare: func(t *testing.T, folder string) {
				skipIfRoot(t)
				writeFile(t, filepath.Join(folder, "a.json"), `{"a":1}`, newer)
				lockUntilCleanup(t, folder, 0o755)
			},
		},
		{
			name:   "unreadable newest file",
			path:   "orders/in",
			label:  "orders/in",
			file:   "newest.json",
			reason: []string{"permission denied"},
			prepare: func(t *testing.T, folder string) {
				skipIfRoot(t)
				writeFile(t, filepath.Join(folder, "old.json"), `{"old":1}`, older)
				writeFile(t, filepath.Join(folder, "newest.json"), `{"new":1}`, newer)
				lockUntilCleanup(t, filepath.Join(folder, "newest.json"), 0o600)
			},
		},
		{
			name:   "unreadable newest file, tenant root, POST",
			path:   ".",
			post:   true,
			label:  ".",
			file:   "newest.json",
			reason: []string{"permission denied"},
			prepare: func(t *testing.T, folder string) {
				skipIfRoot(t)
				writeFile(t, filepath.Join(folder, "newest.json"), `{"new":1}`, newer)
				lockUntilCleanup(t, filepath.Join(folder, "newest.json"), 0o600)
			},
		},
		{
			name:   "readable file (sanity)",
			path:   "orders/in",
			label:  "orders/in",
			file:   "newest.json",
			wantOK: true,
			prepare: func(t *testing.T, folder string) {
				writeFile(t, filepath.Join(folder, "old.json"), `{"old":1}`, older)
				writeFile(t, filepath.Join(folder, "newest.json"), `{"new":1}`, newer)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// isSamplePathAllowed compares the symlink-resolved folder with the
			// base, so the base must be resolved too (on macOS t.TempDir is
			// under /var, a link to /private/var).
			raw := t.TempDir()
			base, err := filepath.EvalSymlinks(raw)
			if err != nil {
				t.Fatal(err)
			}
			root, err := tenantpath.Root(base, pathLabelTenant)
			if err != nil {
				t.Fatalf("tenant root: %v", err)
			}
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			folder := filepath.Join(root, filepath.FromSlash(tc.label))
			if tc.prepare != nil {
				tc.prepare(t, folder)
			}

			sink := &logSink{}
			c := &fileConsumer{
				baseDir: base,
				logger:  slog.New(sinkHandler{sink: sink, counter: &msgCounter{}}),
			}
			srv := httptest.NewServer(c.handleSampleData())
			t.Cleanup(srv.Close)

			var resp *http.Response
			if tc.post {
				body, merr := json.Marshal(map[string]string{"path": tc.path, "tenant_id": pathLabelTenant})
				if merr != nil {
					t.Fatal(merr)
				}
				resp, err = srv.Client().Post(srv.URL, "application/json", bytes.NewReader(body))
			} else {
				q := url.Values{"path": {tc.path}, "tenant_id": {pathLabelTenant}}
				resp, err = srv.Client().Get(srv.URL + "?" + q.Encode())
			}
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			var got sampleResponse
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			if tc.wantOK {
				if !got.OK || got.Filename != tc.file {
					t.Fatalf("response %+v, want ok with filename %q", got, tc.file)
				}
				return
			}
			if got.OK {
				t.Fatalf("response %+v, want an error", got)
			}
			msg := got.Error
			if msg == "path not allowed" || msg == "path is required" {
				t.Fatalf("request refused before the read (%q): the case does not test the read", msg)
			}
			if !namesToken(msg, tc.label) {
				t.Errorf("error %q does not name the folder %q (relative to the tenant root)", msg, tc.label)
			}
			if tc.file != "" && !namesToken(msg, tc.file) {
				t.Errorf("error %q does not name the file %q", msg, tc.file)
			}
			for _, r := range tc.reason {
				if !strings.Contains(msg, r) {
					t.Errorf("error %q does not give %q", msg, r)
				}
			}
			if len(tc.anyReason) > 0 {
				ok := false
				for _, r := range tc.anyReason {
					ok = ok || strings.Contains(msg, r)
				}
				if !ok {
					t.Errorf("error %q gives none of %q", msg, tc.anyReason)
				}
			}
			requireNoLeaks(t, msg, []leak{
				{"volume root", base},
				{"volume root as given", raw},
				{"tenant root", root},
				{"absolute folder", folder},
				{"tenant id", pathLabelTenant},
			})

			// The full error, path and all, is in the log.
			found := false
			sink.mu.Lock()
			for _, r := range sink.recs {
				if r.msg == "Sample data request failed" && r.attrs["error"] != "" && r.attrs["path"] == folder {
					found = true
				}
			}
			sink.mu.Unlock()
			if !found {
				t.Errorf("no log of the failed sample read for %s\nlogs:\n%s", folder, sink.dump())
			}
		})
	}
}
