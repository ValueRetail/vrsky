package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
)

// Tests for #689: a node URL written as user:secret@host carries a credential.
// The request has to use it, but nothing the producer logs, puts in the
// activity feed or returns as an error may show it.

// basicAuthRecorder is a test server that records the credentials it was sent
// and answers with status.
func basicAuthRecorder(t *testing.T, status int) (srv *httptest.Server, creds func() (user, pass string, ok bool)) {
	t.Helper()
	var mu sync.Mutex
	var user, pass string
	var ok bool
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		user, pass, ok = r.BasicAuth()
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() (string, string, bool) {
		mu.Lock()
		defer mu.Unlock()
		return user, pass, ok
	}
}

// closedPort returns host:port of a port nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestSend_URLUserinfoNeverShown(t *testing.T) {
	const secret = "secret"
	tests := []struct {
		name         string
		status       int    // 0: no server, url is built from target
		target       string // for status 0: host:port
		rawURL       string // used as is when set
		userinfo     string // written before the host
		authType     string
		allowPrivate bool
		wantErr      bool
		wantAuth     bool // the server must have seen user:secret
	}{
		{name: "success", status: 200, userinfo: "user:secret@", allowPrivate: true, wantAuth: true},
		{name: "success, user name is the secret", status: 200, userinfo: "secret@", allowPrivate: true},
		{name: "4xx", status: 400, userinfo: "user:secret@", allowPrivate: true, wantAuth: true},
		{name: "401 is retried", status: 401, userinfo: "user:secret@", allowPrivate: true, wantErr: true, wantAuth: true},
		{name: "403 is retried", status: 403, userinfo: "user:secret@", allowPrivate: true, wantErr: true, wantAuth: true},
		{name: "500", status: 500, userinfo: "user:secret@", allowPrivate: true, wantErr: true, wantAuth: true},
		{name: "oauth without a grant warns", status: 200, userinfo: "user:secret@", authType: "oauth", allowPrivate: true, wantAuth: true},
		{name: "connection refused", target: closedPort(t), userinfo: "user:secret@", allowPrivate: true, wantErr: true},
		{name: "connection refused, user name is the secret", target: closedPort(t), userinfo: "secret@", allowPrivate: true, wantErr: true},
		{name: "private address refused", target: "10.0.0.5:80", userinfo: "user:secret@", wantErr: true},
		{name: "loopback refused at dial", target: "localhost:80", userinfo: "user:secret@", wantErr: true},
		{name: "unparseable url", target: "host:80/%zz", userinfo: "user:secret@", allowPrivate: true, wantErr: true},
		{name: "no scheme, user:secret@host", rawURL: "user:secret@example.com/hook", allowPrivate: true, wantErr: true},
		{name: "https: without //", rawURL: "https:user:secret@example.com/hook", allowPrivate: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			p := &httpProducer{
				logger:       slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
				allowPrivate: tt.allowPrivate,
				eventSubs:    map[string][]chan HTTPEvent{},
				recentEvents: map[string][]HTTPEvent{},
			}
			target := tt.target
			var creds func() (string, string, bool)
			if tt.status != 0 {
				var srv *httptest.Server
				srv, creds = basicAuthRecorder(t, tt.status)
				target = strings.TrimPrefix(srv.URL, "http://")
			}
			u := "http://" + tt.userinfo + target + "/hook?x=1"
			if tt.rawURL != "" {
				u = tt.rawURL
			}

			err := p.sendHTTPRequest(context.Background(), "conn-1",
				&HTTPConfig{URL: u, Method: "POST", AuthType: tt.authType}, testEnv(), nil)

			if tt.wantErr != (err != nil) {
				t.Fatalf("error = %v, want error: %v", err, tt.wantErr)
			}
			events, jerr := json.Marshal(p.getRecentEvents("conn-1"))
			if jerr != nil {
				t.Fatal(jerr)
			}
			var everything strings.Builder
			everything.WriteString(logs.String())
			everything.Write(events)
			if err != nil {
				everything.WriteString(err.Error())
			}
			if strings.Contains(everything.String(), secret) {
				t.Errorf("the secret leaked.\nerror: %v\nevents: %s\nlogs: %s", err, events, logs.String())
			}
			if len(p.getRecentEvents("conn-1")) == 0 {
				t.Error("no events emitted; the check above proved nothing")
			}

			if creds != nil {
				user, pass, ok := creds()
				if tt.wantAuth && (!ok || pass != secret || user != "user") {
					t.Errorf("server saw basic auth %q:%q (ok=%v), want user:secret", user, pass, ok)
				}
				if !tt.wantAuth && (!ok || user != secret) {
					t.Errorf("server saw basic auth user %q (ok=%v), want the real user name", user, ok)
				}
			}
		})
	}
}

// The activity feed still says where the message went, with the credential
// replaced, so an operator can tell the nodes apart.
func TestSend_EventShowsRedactedURL(t *testing.T) {
	srv, _ := basicAuthRecorder(t, 200)
	p := &httpProducer{logger: quietLogger(), allowPrivate: true,
		eventSubs: map[string][]chan HTTPEvent{}, recentEvents: map[string][]HTTPEvent{}}
	host := strings.TrimPrefix(srv.URL, "http://")
	if err := p.sendHTTPRequest(context.Background(), "conn-1",
		&HTTPConfig{URL: "http://user:secret@" + host + "/hook", Method: "POST"}, testEnv(), nil); err != nil {
		t.Fatal(err)
	}
	var sent string
	for _, ev := range p.getRecentEvents("conn-1") {
		if ev.Type == "sent" {
			sent = ev.Message
		}
	}
	if want := "POST http://xxxxx@" + host + "/hook → 200"; sent != want {
		t.Errorf("sent event = %q, want %q", sent, want)
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"http://host/x", "http://host/x"},
		{"http://user:secret@host/x?a=1", "http://xxxxx@host/x?a=1"},
		{"https://token@host:8443/x", "https://xxxxx@host:8443/x"},
		{"http://host:80/%zz", "[invalid URL]"},
		{"://user:secret@host", "[invalid URL]"},
		// Without "//" url.Parse puts userinfo in Opaque or Path, not u.User.
		{"user:secret@host/x", "[invalid URL]"},
		{"https:user:secret@host/x", "[invalid URL]"},
		{"secret@host/x", "[invalid URL]"},
		// Parsed as host "user", port 123, path "/secret@host".
		{"http://user:123/secret@host", "[invalid URL]"},
		{"https://host/users/a@b", "[invalid URL]"},
		{"http://host/x?email=a@b", "http://host/x?email=a@b"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := safeurl.RedactURL(tt.in); got != tt.want {
			t.Errorf("RedactURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
