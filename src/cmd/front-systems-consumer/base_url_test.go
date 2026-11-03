package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Tests for #677: base_url is tenant config and every Front Systems call
// carries both keys, so the consumer must not call a base_url that is not
// https or that reaches a private, loopback or link-local address.

func TestCheckBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		allowPrivate bool
		ok           bool
	}{
		{"public apim host", "https://fs-partner.azure-api.net", false, true},
		{"public host with a path and trailing slash", "https://fs-partner.azure-api.net/frontsystems/", false, true},
		{"empty", "", false, false},
		{"bare host", "fs-partner.azure-api.net", false, false},
		{"http", "http://fs-partner.azure-api.net", false, false},
		{"loopback", "https://127.0.0.1:8443", false, false},
		{"loopback v6", "https://[::1]/", false, false},
		{"private 10/8", "https://10.0.0.5/", false, false},
		{"private 192.168/16", "https://192.168.1.1/", false, false},
		{"cloud metadata", "https://169.254.169.254/latest/meta-data/", false, false},
		{"azure wire server", "https://168.63.129.16/", false, false},
		{"userinfo", "https://u:hunter2@fs-partner.azure-api.net", false, false},
		{"query", "https://fs-partner.azure-api.net/?x=1", false, false},
		{"fragment", "https://fs-partner.azure-api.net/#x", false, false},
		{"dev flag: http mock", "http://fs-mock:8080", true, true},
		{"dev flag: loopback mock", "http://127.0.0.1:9000", true, true},
		{"dev flag still wants a scheme", "fs-mock:8080", true, false},
		{"dev flag still refuses userinfo", "http://u:hunter2@127.0.0.1:9000", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkBaseURL(tt.raw, tt.allowPrivate)
			if tt.ok && err != nil {
				t.Errorf("checkBaseURL(%q) = %v, want nil", tt.raw, err)
			}
			if !tt.ok {
				if err == nil {
					t.Fatalf("checkBaseURL(%q) = nil, want an error", tt.raw)
				}
				if !errors.Is(err, errBadBaseURL) {
					t.Errorf("checkBaseURL(%q) = %v, want it to wrap errBadBaseURL (permanent)", tt.raw, err)
				}
				if strings.Contains(err.Error(), "hunter2") {
					t.Errorf("checkBaseURL(%q) = %v, the password is in the error", tt.raw, err)
				}
			}
		})
	}
}

// A connection whose base_url is refused is not started and not retried:
// its row goes to "error" with a reason naming base_url, events get 410, and
// the fake Front Systems (on http://127.0.0.1, which the production policy
// refuses) receives nothing.
func TestStart_RefusesBadBaseURL(t *testing.T) {
	tests := []struct {
		name string
		base func(fakeURL string) string
	}{
		{"http to the fake itself", func(fake string) string { return fake }},
		{"https loopback", func(fake string) string { return strings.Replace(fake, "http://", "https://", 1) }},
		{"loopback v6", func(string) string { return "https://[::1]:8443" }},
		{"private network", func(string) string { return "https://10.0.0.5" }},
		{"cloud metadata", func(string) string { return "https://169.254.169.254/latest" }},
		{"azure wire server", func(string) string { return "https://168.63.129.16" }},
		{"userinfo", func(string) string { return "https://u:hunter2@fs-partner.azure-api.net" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRetryFixture(t, 5*time.Millisecond)
			f.c.allowPrivate = false // production policy
			u := urConn{id: "conn-fs-badbase", tenant: urTenantA}
			f.owners.set(u.id, u.tenant)
			f.mock.ExpectQuery(configLookupSQL).
				WithArgs(u.id, u.tenant).
				WillReturnRows(configRows(u.nodes(tt.base(f.fs.srv.URL))))
			expectMarkError(f.mock, u, "base_url")

			fgAwait(t, goDone(func() { f.start(u) }), "the refused start")
			if n := retryLoopsFor(f.c); n != 0 {
				t.Errorf("retry loops after a refused base_url = %d, want 0", n)
			}
			if _, ok := pendingTenant(f.c, u.id); ok {
				t.Error("start still pending after a refused base_url")
			}
			if _, ok := activeTenant(f.c, u.id); ok {
				t.Error("connection active with a refused base_url")
			}
			if code := postEvent(f.c, u.id, `{}`); code != http.StatusGone {
				t.Errorf("event: status %d, want 410", code)
			}
			f.quiet()
			if calls := f.fs.logCopy(); len(calls) != 0 {
				t.Errorf("the fake Front Systems received %d requests, want none: %+v", len(calls), calls)
			}
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db: %v", err)
			}
		})
	}
}

// A host name that resolves to loopback passes the string check and is
// refused by the client's dial. That is permanent too: the name resolves the
// same way on the next attempt, so it is not retried for six hours.
func TestStart_HostnameResolvingToLoopbackIsPermanent(t *testing.T) {
	f := newRetryFixture(t, 5*time.Millisecond)
	f.c.allowPrivate = false
	f.c.httpClient = safeurl.NewClient(5*time.Second, false)
	port, accepted := tcpCounter(t)
	u := urConn{id: "conn-fs-localhost", tenant: urTenantA}
	f.owners.set(u.id, u.tenant)
	f.mock.ExpectQuery(configLookupSQL).
		WithArgs(u.id, u.tenant).
		WillReturnRows(configRows(u.nodes("https://localhost:" + port)))
	reason := &capturedReason{}
	f.mock.ExpectExec(markErrorSQL).WithArgs(reason, u.id, u.tenant, cfgVersion).
		WillReturnResult(sqlmock.NewResult(0, 1))

	fgAwait(t, goDone(func() { f.start(u) }), "the refused start")
	if n := retryLoopsFor(f.c); n != 0 {
		t.Errorf("retry loops after a dial refusal = %d, want 0", n)
	}
	if _, ok := pendingTenant(f.c, u.id); ok {
		t.Error("start still pending (retrying) after a dial refusal; want permanent")
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener saw %d connections, want 0", n)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
	// last_error is shown to the tenant: it names the cause, not the internal
	// address the host resolved to.
	got := reason.get()
	if !strings.Contains(got, "private") {
		t.Errorf("last_error %q, want it to say the address is private", got)
	}
	for _, leak := range []string{"127.0.0.1", "::1", port} {
		if strings.Contains(got, leak) {
			t.Errorf("last_error %q shows %q to the tenant", got, leak)
		}
	}
}

// capturedReason matches any string last_error and keeps it, so a test can
// look at all of it rather than match a substring.
type capturedReason struct {
	mu sync.Mutex
	s  string
}

func (c *capturedReason) Match(v driver.Value) bool {
	s, ok := v.(string)
	if ok {
		c.mu.Lock()
		c.s = s
		c.mu.Unlock()
	}
	return ok
}

func (c *capturedReason) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.s
}

// A public https base_url, the only kind Front Systems hands out, is called
// with the production policy: the start registers the webhooks there.
func TestStart_PublicBaseURLIsCalled(t *testing.T) {
	f := newRetryFixture(t, time.Hour)
	f.c.allowPrivate = false
	rt := newRewriteTransport(t, f.fs.srv.URL)
	f.c.httpClient = &http.Client{Transport: rt, Timeout: 5 * time.Second}
	const base = "https://fs-partner.azure-api.net"
	u := urConn{id: "conn-fs-public", tenant: urTenantA}
	f.owners.set(u.id, u.tenant)
	f.mock.ExpectQuery(configLookupSQL).
		WithArgs(u.id, u.tenant).
		WillReturnRows(configRows(u.nodes(base)))

	f.start(u)

	if tenant, ok := activeTenant(f.c, u.id); !ok || tenant != u.tenant {
		t.Fatalf("active = (%q, %v), want (%q, true)", tenant, ok, u.tenant)
	}
	if n := len(f.fs.calls(http.MethodPost)); n != len(u.evs()) {
		t.Errorf("registrations = %d, want %d", n, len(u.evs()))
	}
	urls := rt.seen()
	if len(urls) == 0 {
		t.Fatal("no request was made")
	}
	for _, s := range urls {
		if !strings.HasPrefix(s, base+"/api/webhooks") {
			t.Errorf("request to %s, want %s/api/webhooks", s, base)
		}
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}

// Without callback_url the connector calls nothing at Front Systems, so its
// base_url is not checked: webhook-only connections start as before.
func TestStart_WebhookOnlyDoesNotCheckBaseURL(t *testing.T) {
	f := newRetryFixture(t, time.Hour)
	f.c.allowPrivate = false
	u := urConn{id: "conn-fs-webhook-only", tenant: urTenantA}
	f.owners.set(u.id, u.tenant)
	nodes := mustJSON([]any{map[string]any{"id": "n1", "type": "consumer", "config": map[string]any{
		"type":          "front_systems",
		"front_systems": map[string]any{"base_url": "http://10.0.0.5", "subscription_key": "s", "api_key": "k"},
	}}})
	f.mock.ExpectQuery(configLookupSQL).WithArgs(u.id, u.tenant).WillReturnRows(configRows(nodes))

	f.start(u)

	if _, ok := activeTenant(f.c, u.id); !ok {
		t.Error("webhook-only connection not started")
	}
	if calls := f.fs.logCopy(); len(calls) != 0 {
		t.Errorf("the fake Front Systems received %d requests, want none", len(calls))
	}
}

// Configure is where the production HTTP client is built. These run it with
// no client injected and look at what that client does, so a plain
// http.Client in Configure fails here and not only in production.
func configuredConsumer(t *testing.T) *frontSystemsConsumer {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &frontSystemsConsumer{} // no httpClient: Configure must build it
	if err := c.Configure(context.Background(), &sdk.Resources{
		DB:     db,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return c
}

func TestConfigure_ClientRefusesLoopback(t *testing.T) {
	t.Setenv(safeurl.EnvAllowPrivate, "")
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
		t.Errorf("error = %v, want a dial refusal", err)
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener saw %d connections, want 0", n)
	}
}

func TestConfigure_ClientReachesLoopbackWithDevFlag(t *testing.T) {
	t.Setenv(safeurl.EnvAllowPrivate, "true")
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

// tcpCounter listens on 127.0.0.1 and counts connections accepted, so a test
// sees whether a client dialled at all.
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

// rewriteTransport records each request's URL and sends it to the fake
// instead, so a public base_url can be exercised offline.
type rewriteTransport struct {
	target *url.URL
	mu     sync.Mutex
	urls   []string
}

func newRewriteTransport(t *testing.T, target string) *rewriteTransport {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	return &rewriteTransport{target: u}
}

func (rt *rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.urls = append(rt.urls, r.URL.String())
	rt.mu.Unlock()
	r2 := r.Clone(r.Context())
	r2.URL.Scheme, r2.URL.Host, r2.Host = rt.target.Scheme, rt.target.Host, ""
	return http.DefaultTransport.RoundTrip(r2)
}

func (rt *rewriteTransport) seen() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.urls...)
}
