package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/tenantpath"
)

// Tests for #455: the File watcher panel's event stream opens with a
// "connected" event. Its message used to be "Watching " plus the absolute
// watch folder, which showed the volume mount layout and the tenant id to
// whoever had the panel open. It must name the folder relative to the tenant's
// root instead, the way the #409 error events do.
//
// The tests start a real connection on a dirRig (dirread_test.go), serve the
// consumer's own handleEvents through an httptest.Server, read the first SSE
// "data:" line and then hang up.

// connectedEvent is the first SSE message handleEvents writes.
type connectedEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// readConnected opens the event stream for connID on srv, returns the first
// "data:" event and closes the stream. A timeout bounds the whole exchange so
// a handler that never writes fails the test instead of hanging it.
func readConnected(t *testing.T, srv *httptest.Server, connID string) connectedEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), restartWait)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events/"+connID, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /events/%s: %v", connID, err)
	}
	// Cancel first so the handler sees its request context end, then close.
	defer resp.Body.Close()
	defer cancel()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events/%s: status %d, want 200", connID, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type %q, want text/event-stream", ct)
	}

	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var ev connectedEvent
			if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &ev); err != nil {
				t.Fatalf("decode first event %q: %v", data, err)
			}
			return ev
		}
		if err != nil {
			t.Fatalf("stream ended before the first data line: %v", err)
		}
	}
}

// watchedFolder takes the folder name out of a "Watching <folder>" message.
// It accepts the name bare or Go-quoted, so the test pins what is shown, not
// how it is quoted.
func watchedFolder(msg string) (string, bool) {
	rest, ok := strings.CutPrefix(msg, "Watching")
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if unq, err := strconv.Unquote(rest); err == nil {
		return unq, true
	}
	return rest, true
}

func TestHandleEvents_ConnectedNamesFolderRelativeToTenantRoot(t *testing.T) {
	// dirTenant is "tenant-dirread", distinctive enough that finding it in the
	// message means the tenant's directory leaked. None of the expected
	// labels below contains it.
	cases := []struct {
		name string
		// path is the file node's configured path, given the rig's volume
		// root, since one case types the mounted root the way users do.
		path func(base string) string
		want string // folder the message must name
	}{
		{
			name: "nested relative path",
			path: func(string) string { return watchedPath },
			want: "orders/in",
		},
		{
			name: "single folder (the restart rigs' default)",
			path: func(string) string { return restartNodes },
			want: "inbox",
		},
		{
			name: "mounted root typed by the user is re-homed into the tenant",
			path: func(base string) string { return filepath.Join(base, "orders", "in") },
			want: "orders/in",
		},
		{
			name: "no path falls back to the connection id",
			path: func(string) string { return "" },
			want: dirConn,
		},
		{
			name: "tenant root itself",
			path: func(string) string { return "." },
			want: ".",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDirRig(t)
			rig.path = tc.path(rig.base)
			rig.realReads = true

			p := rig.newPod()
			// handleEvents subscribes, which needs the map; set it before the
			// watcher goroutine exists so nothing races on the field.
			p.c.eventSubs = make(map[string][]chan FileEvent)
			if !rig.startOn(p) {
				t.Fatalf("start: connection not active\nlogs:\n%s", rig.sink.dump())
			}
			ac := rig.active()

			root, err := tenantpath.Root(rig.base, rig.tenant)
			if err != nil {
				t.Fatalf("tenant root: %v", err)
			}
			// Make sure the case sets up the folder it claims to, so a
			// passing message is about the right folder.
			if wantDir := filepath.Join(root, filepath.FromSlash(tc.want)); ac.WatchDir != wantDir {
				t.Fatalf("watch folder %q, want %q", ac.WatchDir, wantDir)
			}

			srv := httptest.NewServer(rig.c.handleEvents())
			t.Cleanup(srv.Close)

			ev := readConnected(t, srv, rig.connID)
			if ev.Type != "connected" {
				t.Fatalf("first event type %q, want %q (message %q)", ev.Type, "connected", ev.Message)
			}

			got, ok := watchedFolder(ev.Message)
			if !ok {
				t.Fatalf("message %q does not start with \"Watching\"", ev.Message)
			}
			if got != tc.want {
				t.Errorf("message %q names folder %q, want %q (relative to the tenant root)", ev.Message, got, tc.want)
			}

			for _, leak := range []struct{ what, s string }{
				{"volume root", rig.base},
				{"tenant root", root},
				{"absolute watch folder", ac.WatchDir},
				{"tenant id", rig.tenant},
			} {
				if strings.Contains(ev.Message, leak.s) {
					t.Errorf("message %q contains the %s %q", ev.Message, leak.what, leak.s)
				}
			}
		})
	}
}
