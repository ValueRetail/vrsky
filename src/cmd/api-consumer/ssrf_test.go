package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Tests for #639: base_url and endpoint paths are tenant config and the node's
// credential goes with every request, so the consumer must not reach a private
// address, whether it is written as an IP, resolved from a name, or reached
// through a redirect. Public APIs over http stay reachable.

// tcpCounter listens on 127.0.0.1 and counts connections accepted. A client
// that dials is seen whether or not it then speaks HTTP.
func tcpCounter(t *testing.T) (port string, accepted *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	accepted = new(atomic.Int32)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	_, port, _ = net.SplitHostPort(l.Addr().String())
	return port, accepted
}

// Configure is where the production HTTP client is built. These tests run it
// with no client injected and look at what that client does, so replacing the
// safe client with a plain http.Client fails here and not only in prod.
func configuredConsumer(t *testing.T) *apiConsumer {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &apiConsumer{} // no httpClient: Configure must build it
	if err := c.Configure(context.Background(), &sdk.Resources{
		DB:     db,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return c
}

func TestConfigure_ClientRefusesLoopback(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "")
	c := configuredConsumer(t)
	if c.allowPrivate {
		t.Fatal("allowPrivate is on without the env var")
	}
	port, accepted := tcpCounter(t)
	resp, err := c.httpClient.Get("http://127.0.0.1:" + port + "/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the configured client connected to loopback")
	}
	if !errors.Is(err, safeurl.ErrBlockedAddress) {
		t.Errorf("error %v does not wrap ErrBlockedAddress", err)
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener saw %d connections, want 0", n)
	}
}

func TestConfigure_ClientReachesLoopbackWithDevFlag(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
	c := configuredConsumer(t)
	if !c.allowPrivate {
		t.Fatal("allowPrivate is off with the env var set")
	}
	port, accepted := tcpCounter(t)
	if resp, err := c.httpClient.Get("http://127.0.0.1:" + port + "/"); err == nil {
		_ = resp.Body.Close()
	}
	if n := accepted.Load(); n == 0 {
		t.Error("with ALLOW_PRIVATE_UPSTREAMS=true the client never reached the listener")
	}
}

// A consumer that was never configured still gets the guarded client.
func TestClient_UnconfiguredIsStillGuarded(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true") // the env must not reach a struct literal
	port, accepted := tcpCounter(t)
	c := &apiConsumer{}
	if resp, err := c.client().Get("http://127.0.0.1:" + port + "/"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("an unconfigured consumer connected to loopback")
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener saw %d connections, want 0", n)
	}
}

// Public APIs over plain http are legitimate. A documentation address (TEST-NET-1)
// is public as far as the guard is concerned: the dial gets past it and then
// fails for lack of a route or a timeout, which is not ErrBlockedAddress.
func TestClient_PublicHTTPGetsPastTheGuard(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "")
	c := configuredConsumer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	for _, u := range []string{"http://192.0.2.1/", "https://192.0.2.1/"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := c.client().Do(req)
		if err == nil {
			_ = resp.Body.Close()
			continue
		}
		if errors.Is(err, safeurl.ErrBlockedAddress) {
			t.Errorf("GET %s was refused by the address guard: %v", u, err)
		}
	}
}

func TestAPIConsumerConfig_CheckURLs(t *testing.T) {
	ep := func(paths ...string) []APIEndpoint {
		var out []APIEndpoint
		for _, p := range paths {
			out = append(out, APIEndpoint{Path: p})
		}
		return out
	}
	tests := []struct {
		name         string
		cfg          APIConsumerConfig
		allowPrivate bool
		ok           bool
	}{
		{"https public", APIConsumerConfig{BaseURL: "https://api.example.org", Endpoints: ep("/forecast")}, false, true},
		{"http public", APIConsumerConfig{BaseURL: "http://api.example.com", Endpoints: ep("/v1")}, false, true},
		{"full url in the path", APIConsumerConfig{BaseURL: "", Endpoints: ep("https://api.example.com/v1")}, false, true},
		{"hostname", APIConsumerConfig{BaseURL: "http://localhost:8080", Endpoints: ep("/v1")}, false, true}, // the dial refuses it
		{"loopback", APIConsumerConfig{BaseURL: "http://127.0.0.1:8080", Endpoints: ep("/v1")}, false, false},
		{"loopback v6", APIConsumerConfig{BaseURL: "http://[::1]:8080", Endpoints: ep("/v1")}, false, false},
		{"metadata", APIConsumerConfig{BaseURL: "http://169.254.169.254", Endpoints: ep("/latest/meta-data/")}, false, false},
		{"private 10", APIConsumerConfig{BaseURL: "https://10.0.0.5", Endpoints: ep("/")}, false, false},
		{"public base, private full url in an endpoint", APIConsumerConfig{BaseURL: "https://api.example.com", Endpoints: ep("/ok", "http://192.168.1.10/admin")}, false, false},
		{"no base, relative path", APIConsumerConfig{BaseURL: "", Endpoints: ep("/v1")}, false, false},
		{"not http", APIConsumerConfig{BaseURL: "ftp://api.example.com", Endpoints: ep("/v1")}, false, false},
		{"dev flag: loopback mock", APIConsumerConfig{BaseURL: "http://127.0.0.1:9000", Endpoints: ep("/v1")}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.checkURLs(tt.allowPrivate)
			if tt.ok && err != nil {
				t.Errorf("checkURLs = %v, want nil", err)
			}
			if !tt.ok && err == nil {
				t.Error("checkURLs = nil, want an error")
			}
		})
	}
}

// A connection whose URL is refused is not started: no poller, no status write,
// and the fake API (on 127.0.0.1, which the production policy refuses) gets
// nothing.
func TestStart_RefusesPrivateURL(t *testing.T) {
	tests := []struct {
		name  string
		nodes func(apiURL string) string
	}{
		{"loopback base_url", func(api string) string { return apiNodes(api, "/items") }},
		{"metadata base_url", func(string) string { return apiNodes("http://169.254.169.254", "/latest/meta-data/") }},
		{"private base_url", func(string) string { return apiNodes("https://10.0.0.5", "/") }},
		{"ipv6 loopback", func(string) string { return apiNodes("http://[::1]:8443", "/") }},
		{"full url in an endpoint path", func(api string) string { return apiNodes("https://api.example.com", api+"/items") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSRFixture(t)
			f.c.allowPrivate = false // production policy
			apiURL := between(f.nodes, `"base_url":"`, `"`)
			f.nodes = tt.nodes(apiURL)
			f.expectLookup(nil)

			f.c.startConnection(context.Background(), srConn, srTenant)

			if f.entry() != nil {
				t.Error("a refused connection has an active poller")
			}
			time.Sleep(50 * time.Millisecond)
			if n := f.hits.Load(); n != 0 {
				t.Errorf("the API received %d requests from a refused start, want none", n)
			}
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db: %v (a refused start must not write a status)", err)
			}
		})
	}
}

func apiNodes(baseURL, path string) string {
	return fmt.Sprintf(`[{"id":"c1","type":"consumer","config":{"type":"api","api":{
		"base_url":%q,"poll_interval_seconds":3600,"endpoints":[{"path":%q}]}}}]`, baseURL, path)
}

func between(s, from, to string) string {
	_, rest, _ := strings.Cut(s, from)
	v, _, _ := strings.Cut(rest, to)
	return v
}

// A hostname passes the string check, so the guard that matters is the dial:
// "localhost" resolves to loopback and the production client must not connect.
func TestCallEndpoint_HostnameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
	port, accepted := tcpCounter(t)
	u := "http://localhost:" + port + "/items"

	cfg := &APIConsumerConfig{BaseURL: "http://localhost:" + port, Endpoints: []APIEndpoint{{Path: "/items"}}}
	if err := cfg.checkURLs(false); err != nil {
		t.Fatalf("a hostname is not refused at config time (only the dial can tell), got %v", err)
	}

	c := &apiConsumer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))} // allowPrivate false
	_, _, err := c.callEndpoint(context.Background(), c.client(), u, "t", APIEndpoint{Path: "/items", AuthType: "bearer", AuthValue: "secret"}, c.logger)
	if err == nil {
		t.Fatal("callEndpoint connected to a hostname that resolves to loopback")
	}
	if !errors.Is(err, safeurl.ErrBlockedAddress) {
		t.Errorf("error %v does not wrap ErrBlockedAddress", err)
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the client opened %d connections to loopback, want none", n)
	}
}

// A literal private IP in a full-URL endpoint is refused at dial too, in case
// the string check is ever bypassed.
func TestCallEndpoint_LiteralLoopbackIsRefusedAtDial(t *testing.T) {
	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer api.Close()
	c := &apiConsumer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, _, err := c.callEndpoint(context.Background(), c.client(), api.URL, "t", APIEndpoint{AuthType: "api_key", AuthValue: "secret"}, c.logger)
	if !errors.Is(err, safeurl.ErrBlockedAddress) {
		t.Fatalf("error %v does not wrap ErrBlockedAddress", err)
	}
	if hits.Load() != 0 {
		t.Errorf("the API received %d requests, want none", hits.Load())
	}
}

// Redirects: a hop on the same host is followed; a hop to another host, the
// metadata service or a hostname is not, and the credential never leaves.
func TestCallEndpoint_Redirects(t *testing.T) {
	var otherHits atomic.Int32
	var otherKey atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		otherKey.Store(r.Header.Get("X-API-Key"))
	}))
	defer other.Close()
	_, otherPort, _ := strings.Cut(strings.TrimPrefix(other.URL, "http://"), ":")

	var gotKey atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
		case "/new":
			gotKey.Store(r.Header.Get("X-API-Key"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/other-host":
			http.Redirect(w, r, "http://localhost:"+otherPort+"/", http.StatusFound)
		case "/metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/iam/", http.StatusFound)
		case "/private":
			http.Redirect(w, r, "http://10.0.0.5/admin", http.StatusFound)
		}
	}))
	defer srv.Close()

	// The dev flag lets the test server itself be reached; the redirect policy
	// does not depend on it.
	c := &apiConsumer{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), allowPrivate: true}
	ep := APIEndpoint{AuthType: "api_key", AuthValue: "secret-key"}

	body, _, err := c.callEndpoint(context.Background(), c.client(), srv.URL+"/old", "t", ep, c.logger)
	if err != nil {
		t.Fatalf("same-host redirect: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q", body)
	}
	if k, _ := gotKey.Load().(string); k != "secret-key" {
		t.Errorf("the credential after a same-host redirect = %q, want it kept", k)
	}

	for _, path := range []string{"/other-host", "/metadata", "/private"} {
		t.Run(strings.TrimPrefix(path, "/"), func(t *testing.T) {
			_, _, err := c.callEndpoint(context.Background(), c.client(), srv.URL+path, "t", ep, c.logger)
			if err == nil {
				t.Fatal("a redirect off the host was followed")
			}
			if !strings.Contains(err.Error(), "redirect") {
				t.Errorf("error %v does not say it was a redirect", err)
			}
		})
	}
	if otherHits.Load() != 0 {
		t.Errorf("the other host received %d requests (X-API-Key %v), want none", otherHits.Load(), otherKey.Load())
	}
}

// The sample-data endpoint fetches a URL the caller typed, so it refuses the
// same things the poller does.
func TestSampleData_RefusesPrivateTargets(t *testing.T) {
	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer api.Close()
	apiPath := strings.TrimPrefix(api.URL, "http://")

	tests := []struct {
		name string
		body string
		want string
	}{
		{"loopback base_url (dial)", fmt.Sprintf(`{"base_url":%q,"path":"/x"}`, api.URL), "private"},
		{"loopback hostname (dial)", fmt.Sprintf(`{"base_url":"http://localhost:%s","path":"/x"}`, apiPath[strings.LastIndex(apiPath, ":")+1:]), "private"},
		{"metadata literal", `{"base_url":"http://169.254.169.254","path":"/latest/meta-data/"}`, "private"},
		{"private literal in the path", `{"base_url":"https://api.example.com","path":"http://10.0.0.5/admin"}`, "private"},
		{"no host", `{"base_url":"","path":"/x"}`, "not an absolute URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &apiConsumer{} // allowPrivate false
			w := httptest.NewRecorder()
			c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(tt.body)))
			out := w.Body.String()
			if !strings.Contains(out, `"ok":false`) || !strings.Contains(out, tt.want) {
				t.Errorf("want ok=false mentioning %q, got %s", tt.want, out)
			}
		})
	}
	if hits.Load() != 0 {
		t.Errorf("the API received %d requests, want none", hits.Load())
	}
}

// With the operator's dev flag the sample-data endpoint reaches a local mock.
func TestSampleData_DevFlagReachesLocalMock(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer api.Close()
	c := &apiConsumer{allowPrivate: true}
	w := httptest.NewRecorder()
	c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/",
		strings.NewReader(fmt.Sprintf(`{"base_url":%q,"path":"/x"}`, api.URL))))
	if !strings.Contains(w.Body.String(), `"ok":true`) || !strings.Contains(w.Body.String(), "world") {
		t.Errorf("got %s", w.Body.String())
	}
}
