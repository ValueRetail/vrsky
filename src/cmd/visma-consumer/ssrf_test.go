package main

import (
	"context"
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

	"github.com/ValueRetail/vrsky/pkg/oauthcc"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Tests for #661: base_url and token_url are tenant config, and the token
// request carries the client secret and the API request the bearer token. A
// connection whose URLs are not https or reach a private address is not
// started, and the clients refuse to dial one.

const publicBase = "https://salesorder.visma.net/api/v3"

// A connection whose base_url or token_url is refused is not started: no
// poller, and the fake API (on http://127.0.0.1, which the production policy
// refuses on two counts) receives nothing.
func TestStart_RefusesBadURLs(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  func(api string) string
		tokenURL func(api string) string
	}{
		{"base_url http to the api itself", func(api string) string { return api }, nil},
		{"base_url https loopback", func(api string) string { return strings.Replace(api, "http://", "https://", 1) }, nil},
		{"base_url metadata service", func(string) string { return "https://169.254.169.254/latest" }, nil},
		{"base_url azure wire server", func(string) string { return "https://168.63.129.16/" }, nil},
		{"base_url private network", func(string) string { return "https://10.0.0.5/v2" }, nil},
		{"base_url ipv6 loopback", func(string) string { return "https://[::1]:8443/v2" }, nil},
		{"base_url userinfo", func(string) string { return "https://u:p@salesorder.visma.net/api/v3" }, nil},
		{"token_url http to the api itself", nil, func(api string) string { return api + "/token" }},
		{"token_url https loopback", nil, func(api string) string { return strings.Replace(api, "http://", "https://", 1) + "/token" }},
		{"token_url metadata service", nil, func(string) string { return "https://169.254.169.254/token" }},
		{"token_url private network", nil, func(string) string { return "https://192.168.1.10/token" }},
		{"token_url ipv6 loopback", nil, func(string) string { return "https://[::1]/token" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSRFixture(t)
			f.c.allowPrivate = false // production policy
			api := between(f.nodes, `"base_url":"`, `"`)
			// Only the field under test is bad; the other one is a real public URL.
			base, token := publicBase, "https://connect.visma.com/connect/token"
			if tt.baseURL != nil {
				base = tt.baseURL(api)
			}
			if tt.tokenURL != nil {
				token = tt.tokenURL(api)
			}
			f.nodes = fmt.Sprintf(`[{"id":"c1","type":"consumer","config":{"type":"visma","visma":{"base_url":%q,"token_url":%q,"scope":"s","client_id":"id","client_secret":"sec","resource":"SalesOrders","poll_interval_seconds":3600}}}]`, base, token)
			f.expectLookup(nil)

			f.c.startConnection(context.Background(), srConn, srTenant)

			if n := f.activeCount(); n != 0 {
				t.Errorf("active pollers = %d, want 0: a refused URL must not start", n)
			}
			time.Sleep(50 * time.Millisecond)
			if n := f.polls.Load(); n != 0 {
				t.Errorf("the API received %d requests from a refused start, want none", n)
			}
		})
	}
}

func between(s, from, to string) string {
	_, rest, _ := strings.Cut(s, from)
	v, _, _ := strings.Cut(rest, to)
	return v
}

// The operator's dev flag lifts the check for a mock upstream (the fixture's
// http://127.0.0.1 API and token endpoint).
func TestStart_DevFlagAllowsMockUpstream(t *testing.T) {
	f := newSRFixture(t)
	f.c.allowPrivate = true
	f.expectLookup(nil)

	f.c.startConnection(context.Background(), srConn, srTenant)

	if n := f.activeCount(); n != 1 {
		t.Errorf("active pollers = %d, want 1 with ALLOW_PRIVATE_UPSTREAMS", n)
	}
}

// The sample-data endpoint calls the tenant's URLs too.
func TestSampleData_RefusesBadURLs(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"access_token":"t","expires_in":3600}`)
	}))
	defer srv.Close()

	tests := []struct {
		name, baseURL, tokenURL string
	}{
		{"base_url http", srv.URL, "https://connect.visma.com/connect/token"},
		{"base_url metadata", "https://169.254.169.254/", "https://connect.visma.com/connect/token"},
		{"token_url http", publicBase, srv.URL + "/token"},
		{"token_url metadata", publicBase, "https://169.254.169.254/token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &vismaConsumer{httpClient: http.DefaultClient} // allowPrivate false
			body := fmt.Sprintf(`{"client_id":"id","client_secret":"sec","base_url":%q,"token_url":%q,"scope":"s","resource":"SalesOrders"}`, tt.baseURL, tt.tokenURL)
			w := httptest.NewRecorder()
			c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body)))

			got := w.Body.String()
			if !strings.Contains(got, `"ok":false`) || !(strings.Contains(got, "must use https") || strings.Contains(got, "private")) {
				t.Errorf("want ok=false naming the reason, got %s", got)
			}
			if strings.Contains(tt.name, "token_url") && !strings.Contains(got, "token_url") {
				t.Errorf("the error should name token_url, got %s", got)
			}
			if hits.Load() != 0 {
				t.Errorf("the server received %d requests, want none", hits.Load())
			}
		})
	}
}

// A token_url whose host resolves to loopback passes the string check. The
// sample-data token request must go through the safe client, and the answer
// must not tell the tenant which address the name resolved to: that would be
// a DNS oracle for in-cluster names.
func TestSampleData_HostnameResolvingToLoopbackHidesTheAddress(t *testing.T) {
	port, accepted := tcpCounter(t)
	c := &vismaConsumer{
		httpClient: safeurl.NewClient(5*time.Second, false), // what Configure builds
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	body := fmt.Sprintf(`{"client_id":"id","client_secret":"sec","base_url":%q,"token_url":"https://localhost:%s/token","scope":"s","resource":"SalesOrders"}`, publicBase, port)
	w := httptest.NewRecorder()
	c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body)))

	got := w.Body.String()
	if n := accepted.Load(); n != 0 {
		t.Errorf("the token request opened %d connections to loopback, want none", n)
	}
	if !strings.Contains(got, `"ok":false`) || !strings.Contains(got, "target address not allowed") {
		t.Errorf(`want ok=false with "target address not allowed", got %s`, got)
	}
	for _, leak := range []string{"127.0.0.1", "::1", "dial tcp"} {
		if strings.Contains(got, leak) {
			t.Errorf("the answer leaks the resolved address (%q): %s", leak, got)
		}
	}
}

// Configure is where the production client is built. These tests run it with
// no client injected and look at what that client does, so a plain
// http.Client fails here and not only in prod.
func configuredConsumer(t *testing.T) *vismaConsumer {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &vismaConsumer{} // no httpClient: Configure must build it
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

// A hostname passes the string check; the dial refuses it when it resolves to
// loopback. This covers the API request.
func TestGet_HostnameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"t","expires_in":3600}`)
	}))
	defer tokenSrv.Close()
	port, accepted := tcpCounter(t)

	c, _, _ := newTestConsumer()
	c.httpClient = safeurl.NewClient(5*time.Second, false) // what Configure builds
	cfg := &VismaConfig{BaseURL: "https://localhost:" + port + "/v2", Scope: "s", ClientID: "c", ClientSecret: "x", Resource: "customer"}
	// A token source that is allowed to reach the test token server, so the
	// failure below is the API request's.
	tok := oauthcc.New(tokenSrv.URL, "c", "x", "s").WithHTTPClient(http.DefaultClient)
	if _, err := c.get(context.Background(), cfg, tok); err == nil {
		t.Fatal("get connected to a hostname that resolves to loopback")
	}
	if accepted.Load() != 0 {
		t.Errorf("the client opened %d connections to loopback, want none", accepted.Load())
	}
}

// Same for the token request, which carries the client secret: a token_url
// whose host resolves to loopback is never dialled by the poller's client.
func TestPoller_TokenRequestUsesTheSafeClient(t *testing.T) {
	f := newSRFixture(t)
	f.c.allowPrivate = false
	f.c.httpClient = safeurl.NewClient(5*time.Second, false) // what Configure builds
	port, accepted := tcpCounter(t)
	f.nodes = fmt.Sprintf(`[{"id":"c1","type":"consumer","config":{"type":"visma","visma":{"base_url":%q,"token_url":"https://localhost:%s/token","scope":"s","client_id":"id","client_secret":"sec","resource":"SalesOrders","poll_interval_seconds":3600}}}]`, publicBase, port)
	f.expectLookup(nil)

	f.c.startConnection(context.Background(), srConn, srTenant)
	if f.activeCount() != 1 {
		t.Fatal("a hostname token_url passes the string check and should start")
	}
	time.Sleep(200 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Errorf("the token request opened %d connections to loopback, want none", n)
	}
}

// tcpCounter listens on 127.0.0.1 and counts connections accepted. A TLS
// handshake against it fails, so the count (not a handler) is what shows
// whether the client dialled.
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
