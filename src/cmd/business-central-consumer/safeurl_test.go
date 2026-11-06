package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #686: api_base_url and token_url are tenant config, the token
// request carries the client secret and every API request the bearer token.

// suUpstream is a fake upstream at the transport level: every request the
// connector sends, to whatever address, lands here and is recorded. Without
// the URL check a refused config would show up as a recorded request.
type suUpstream struct {
	mu   sync.Mutex
	urls []string
}

func (u *suUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	u.mu.Lock()
	u.urls = append(u.urls, r.URL.String())
	u.mu.Unlock()
	body := `{"value":[]}`
	if strings.Contains(r.URL.Path, "token") {
		body = `{"access_token":"tok","expires_in":3600}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func (u *suUpstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.urls...)
}

// suTCPCounter is a loopback listener that counts the connections it accepts.
func suTCPCounter(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			_ = conn.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port, &n
}

// suBadURLs are api_base_url / token_url values the connector must refuse
// without the dev switch, with a fragment of the reason.
var suBadURLs = []struct {
	name, api, token, want string
}{
	{"api http", "http://bc.example.com/api/v2.0", "", "must use https"},
	{"api loopback", "https://127.0.0.1/api/v2.0", "", "private, loopback"},
	{"api IPv6 loopback", "https://[::1]/api/v2.0", "", "private, loopback"},
	{"api 10/8", "https://10.0.0.5/api/v2.0", "", "private, loopback"},
	{"api 172.16/12", "https://172.16.4.2/api/v2.0", "", "private, loopback"},
	{"api 192.168/16", "https://192.168.1.10/api/v2.0", "", "private, loopback"},
	{"api metadata", "https://169.254.169.254/latest", "", "private, loopback"},
	{"api Azure wire server", "https://168.63.129.16/api/v2.0", "", "private, loopback"},
	{"api userinfo", "https://user:pw@bc.example.com/api/v2.0", "", "user name"},
	{"api query", "https://bc.example.com/api/v2.0?x=1", "", "query string"},
	{"token http", "", "http://login.example.com/t/oauth2/v2.0/token", "must use https"},
	{"token loopback", "", "https://127.0.0.1/token", "private, loopback"},
	{"token IPv6 loopback", "", "https://[::1]/token", "private, loopback"},
	{"token metadata", "", "https://169.254.169.254/token", "private, loopback"},
	{"token 10/8", "", "https://10.1.2.3/token", "private, loopback"},
}

func TestCheckURLs(t *testing.T) {
	type tc struct {
		name         string
		cfg          BCConfig
		allowPrivate bool
		want         string // "" = allowed
	}
	cases := []tc{
		{name: "defaults", cfg: BCConfig{AADTenantID: "t1", CompanyID: "GUID"}},
		{name: "public overrides", cfg: BCConfig{AADTenantID: "t1",
			APIBaseURL: "https://bc.example.com:7048/BC/api/v2.0", TokenURL: "https://login.example.com/t1/oauth2/v2.0/token"}},
		{name: "dev switch allows a loopback mock", allowPrivate: true,
			cfg: BCConfig{APIBaseURL: "http://127.0.0.1:8080/api/v2.0", TokenURL: "http://127.0.0.1:8080/token"}},
		{name: "dev switch allows a compose service", allowPrivate: true,
			cfg: BCConfig{APIBaseURL: "http://mock-bc:8099/api/v2.0", TokenURL: "http://mock-bc:8099/token"}},
		{name: "dev switch still refuses userinfo", allowPrivate: true,
			cfg: BCConfig{APIBaseURL: "http://u:p@127.0.0.1/api/v2.0"}, want: "user name"},
	}
	for _, b := range suBadURLs {
		cases = append(cases, tc{name: b.name, cfg: BCConfig{AADTenantID: "t1", APIBaseURL: b.api, TokenURL: b.token}, want: b.want})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.checkURLs(c.allowPrivate)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.want != "" && err == nil:
				t.Fatal("allowed, want refused")
			case c.want != "" && !strings.Contains(err.Error(), c.want):
				t.Fatalf("error %q does not say %q", err, c.want)
			}
		})
	}
}

// suStartConsumer runs startConnection for one node config through the real
// config lookup, with the given HTTP client.
func suStartConsumer(t *testing.T, bc map[string]any, client *http.Client, allowPrivate bool) *bcConsumer {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	nodes, _ := json.Marshal([]map[string]any{{
		"id": "src", "type": "consumer",
		"config": map[string]any{"type": "business_central", "business_central": bc},
	}})
	mock.ExpectQuery("SELECT nodes FROM connections").WithArgs("conn-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow(nodes))
	c := &bcConsumer{
		db:           db,
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient:   client,
		allowPrivate: allowPrivate,
		active:       make(map[string]*poller),
		checkpoints:  checkpoint.NewInMemoryStore(),
		publish:      func(context.Context, *envelope.Envelope) error { return nil },
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = c.Stop(ctx)
	})
	c.startConnection(context.Background(), "conn-1", "tenant-1")
	return c
}

func suNode(api, token string) map[string]any {
	n := map[string]any{
		"aad_tenant_id": "t1", "company_id": "GUID", "client_id": "cid", "client_secret": "sec",
		"entity": "items", "poll_interval_seconds": 3600,
	}
	if api != "" {
		n["api_base_url"] = api
	}
	if token != "" {
		n["token_url"] = token
	}
	return n
}

func suActive(c *bcConsumer) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.active)
}

func TestStart_RefusesBadURLs(t *testing.T) {
	for _, b := range suBadURLs {
		t.Run(b.name, func(t *testing.T) {
			up := &suUpstream{}
			c := suStartConsumer(t, suNode(b.api, b.token), &http.Client{Transport: up}, false)
			if n := suActive(c); n != 0 {
				t.Errorf("a poller started (%d active), want none", n)
			}
			// A poller polls (token first) as soon as it starts.
			time.Sleep(100 * time.Millisecond)
			if got := up.seen(); len(got) != 0 {
				t.Errorf("requests sent: %v, want none", got)
			}
		})
	}
}

func TestStart_DefaultsAllowed(t *testing.T) {
	up := &suUpstream{}
	c := suStartConsumer(t, suNode("", ""), &http.Client{Transport: up}, false)
	if n := suActive(c); n != 1 {
		t.Fatalf("active pollers = %d, want 1", n)
	}
	harness.Eventually(t, 5*time.Second, "the poller to call the token endpoint and the API", func() bool { return len(up.seen()) >= 2 })
	got := up.seen()
	if want := "https://login.microsoftonline.com/t1/oauth2/v2.0/token"; got[0] != want {
		t.Errorf("token request to %s, want %s", got[0], want)
	}
	if want := "https://api.businesscentral.dynamics.com/v2.0/t1/Production/api/v2.0/companies(GUID)/items"; got[1] != want {
		t.Errorf("API request to %s, want %s", got[1], want)
	}
}

func TestStart_DevSwitchReachesLoopbackMock(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
			return
		}
		polls.Add(1)
		fmt.Fprint(w, `{"value":[]}`)
	}))
	defer srv.Close()
	// What Configure builds with ALLOW_PRIVATE_UPSTREAMS=true.
	c := suStartConsumer(t, suNode(srv.URL+"/api/v2.0", srv.URL+"/token"), safeurl.NewClient(5*time.Second, true), true)
	if n := suActive(c); n != 1 {
		t.Fatalf("active pollers = %d, want 1", n)
	}
	harness.Eventually(t, 5*time.Second, "the mock to be polled", func() bool { return polls.Load() >= 1 })
}

func TestSampleData_RefusesBadURLs(t *testing.T) {
	for _, b := range suBadURLs {
		t.Run(b.name, func(t *testing.T) {
			up := &suUpstream{}
			c := &bcConsumer{httpClient: &http.Client{Transport: up}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			cfg := suNode(b.api, b.token)
			raw, _ := json.Marshal(cfg)
			w := httptest.NewRecorder()
			c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(string(raw))))
			got := w.Body.String()
			if !strings.Contains(got, `"ok":false`) || !strings.Contains(got, b.want) {
				t.Errorf("want ok:false naming %q, got %s", b.want, got)
			}
			if sent := up.seen(); len(sent) != 0 {
				t.Errorf("requests sent: %v, want none", sent)
			}
		})
	}
}

// suTokenStub answers the token request itself and hands every other
// request to next, so an API request reaches the real dial check.
type suTokenStub struct{ next http.RoundTripper }

func (s suTokenStub) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/token") {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"tok","expires_in":3600}`)),
			Request:    r,
		}, nil
	}
	return s.next.RoundTrip(r)
}

// A host name passes the string check; the dial refuses where it resolves to.
// The answer must not say which address that was.
func TestSampleData_HostnameResolvingToLoopbackHidesTheAddress(t *testing.T) {
	port, accepted := suTCPCounter(t)
	local := "https://localhost:" + port
	safe := safeurl.NewClient(5*time.Second, false) // what Configure builds
	for _, tc := range []struct {
		name   string
		api    string
		token  string
		client *http.Client
	}{
		{"token_url", "https://bc.example.com/api/v2.0", local + "/oauth2/v2.0/token", safe},
		{"api_base_url", local + "/api/v2.0", "https://login.example.com/t1/token",
			&http.Client{Transport: suTokenStub{next: safe.Transport}, CheckRedirect: safe.CheckRedirect}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &bcConsumer{httpClient: tc.client, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			raw, _ := json.Marshal(suNode(tc.api, tc.token))
			w := httptest.NewRecorder()
			c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(string(raw))))
			got := w.Body.String()
			if n := accepted.Load(); n != 0 {
				t.Errorf("%d connections reached loopback, want none", n)
			}
			if !strings.Contains(got, `"ok":false`) || !strings.Contains(got, "target address not allowed") {
				t.Errorf(`want ok:false with "target address not allowed", got %s`, got)
			}
			for _, leak := range []string{"127.0.0.1", "::1", "dial tcp"} {
				if strings.Contains(got, leak) {
					t.Errorf("the answer leaks the resolved address (%q): %s", leak, got)
				}
			}
		})
	}
}

// Configure is where the production client is built. These run it with no
// client injected and look at what that client does, so a plain http.Client
// fails here and not only in production.
func suConfigured(t *testing.T) *bcConsumer {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &bcConsumer{} // no httpClient: Configure must build it
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
	c := suConfigured(t)
	if c.allowPrivate {
		t.Fatal("allowPrivate is on without the env var")
	}
	port, accepted := suTCPCounter(t)
	resp, err := c.httpClient.Get("http://127.0.0.1:" + port + "/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the configured client connected to loopback")
	}
	if !errors.Is(err, safeurl.ErrBlockedAddress) {
		t.Errorf("err = %v, want safeurl.ErrBlockedAddress", err)
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener saw %d connections, want 0", n)
	}
}

func TestConfigure_ClientReachesLoopbackWithDevSwitch(t *testing.T) {
	t.Setenv(safeurl.EnvAllowPrivate, "true")
	c := suConfigured(t)
	if !c.allowPrivate {
		t.Fatal("allowPrivate is off with the env var set")
	}
	port, accepted := suTCPCounter(t)
	if resp, err := c.httpClient.Get("http://127.0.0.1:" + port + "/"); err == nil {
		_ = resp.Body.Close()
	}
	harness.Eventually(t, 2*time.Second, "the client to reach the listener", func() bool { return accepted.Load() > 0 })
}

func TestFollowNext(t *testing.T) {
	const root = "https://bc.example.com/BC/api/v2.0"
	const page1 = root + "/companies(GUID)/items"
	cfg := &BCConfig{APIBaseURL: root, CompanyID: "GUID"}
	for _, tc := range []struct {
		name, next, want string // want "" = refused
	}{
		{"same origin", page1 + "?$skiptoken=2", page1 + "?$skiptoken=2"},
		{"same origin, explicit default port", "https://bc.example.com:443/BC/api/v2.0/companies(GUID)/items?$skiptoken=2", "https://bc.example.com/BC/api/v2.0/companies(GUID)/items?$skiptoken=2"},
		{"same origin, host in capitals", "https://BC.EXAMPLE.COM/BC/api/v2.0/companies(GUID)/items?$skiptoken=2", page1 + "?$skiptoken=2"},
		{"same origin, outside the root path", "https://bc.example.com/other?x=1", "https://bc.example.com/other?x=1"},
		{"user name and fragment dropped", "https://u:p@bc.example.com/BC/api/v2.0/companies(GUID)/items?$skiptoken=2#f", page1 + "?$skiptoken=2"},
		{"relative query", "?$skiptoken=2", page1 + "?$skiptoken=2"},
		{"relative path", "/BC/api/v2.0/companies(GUID)/items?$skiptoken=2", page1 + "?$skiptoken=2"},
		{"internal host, under the root path", "https://bc-internal.corp:7048/BC/api/v2.0/companies(GUID)/items?$skiptoken=2", page1 + "?$skiptoken=2"},
		{"http, under the root path", "http://bc.example.com/BC/api/v2.0/companies(GUID)/items?$skiptoken=2", page1 + "?$skiptoken=2"},
		{"other host, outside the root path", "https://evil.example.net/collect?x=1", ""},
		{"other port, outside the root path", "https://bc.example.com:8443/other", ""},
		{"http, outside the root path", "http://bc.example.com/other", ""},
		{"metadata", "http://169.254.169.254/latest/meta-data/", ""},
		{"path that only shares a prefix", "https://evil.example.net/BC/api/v2.0x/items", ""},
		{"protocol-relative to another host", "//evil.example.net/x", ""},
		{"another host, %2e%2e", "https://evil.example.net/BC/api/v2.0/%2e%2e/%2e%2e/admin", ""},
		{"another host, %2E%2E", "https://evil.example.net/BC/api/v2.0/%2E%2E/%2E%2E/admin", ""},
		{"another host, ..%2F..", "https://evil.example.net/BC/api/v2.0/..%2F..%2Fadmin", ""},
		{"another host, ..%5C..", "https://evil.example.net/BC/api/v2.0/..%5C..%5Cadmin", ""},
		{"another host, literal backslashes", `https://evil.example.net/BC/api/v2.0/..\..\admin`, ""},
		{"another host, literal dots leaving the root", "https://evil.example.net/BC/api/v2.0/../../admin", ""},
		{"another host, literal dots staying under the root", "https://bc-internal.corp/BC/api/v2.0/companies(GUID)/x/../items?$skiptoken=2", page1 + "?$skiptoken=2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cfg.followNext(page1, tc.next)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("followed to %s, want refused", got)
				}
				if strings.Contains(err.Error(), "evil") || strings.Contains(err.Error(), "169.254") {
					t.Errorf("the error names the link: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if got != tc.want {
				t.Errorf("next = %s, want %s", got, tc.want)
			}
		})
	}
}

// suSide is a server the configured API's nextLink points at. It must never
// be called: it is not the configured origin.
type suSide struct {
	srv  *httptest.Server
	hits atomic.Int32
	auth atomic.Value
}

func suNewSide(t *testing.T, tls bool) *suSide {
	t.Helper()
	s := &suSide{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.auth.Store(r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"value":[{"number":"FROM-THE-OTHER-HOST","lastModifiedDateTime":"2026-10-02T10:00:00Z"}]}`)
	})
	if tls {
		s.srv = httptest.NewTLSServer(h)
	} else {
		s.srv = httptest.NewServer(h)
	}
	t.Cleanup(s.srv.Close)
	return s
}

// Two servers: the configured API (https) and another origin. The first page's
// nextLink names the other one; the bearer token must not go there (#686).
func TestFetch_NextLinkStaysOnTheConfiguredOrigin(t *testing.T) {
	const entityPath = "/api/v2.0/companies(GUID)/items"
	for _, tc := range []struct {
		name        string
		next        func(api, otherHTTPS, otherHTTP string) string
		wantPages   int  // pages fetched from the configured API
		wantErr     bool // the poll stops
		incremental bool
	}{
		{"same origin", func(api, _, _ string) string { return api + entityPath + "?page=2" }, 2, false, false},
		{"relative", func(_, _, _ string) string { return "?page=2" }, 2, false, false},
		{"another host, outside the API path", func(_, o, _ string) string { return o + "/collect?page=2" }, 1, true, false},
		{"another host, under the API path", func(_, o, _ string) string { return o + entityPath + "?page=2" }, 2, false, false},
		{"https to http, outside the API path", func(_, _, h string) string { return h + "/collect?page=2" }, 1, true, false},
		{"https to http, under the API path", func(_, _, h string) string { return h + entityPath + "?page=2" }, 2, false, false},
		// The poll stops on the only value it read, so it has nothing to
		// advance to (#733): the next poll reads the same window again
		// instead of skipping it.
		{"incremental, another host, outside the API path", func(_, o, _ string) string { return o + "/collect?page=2" }, 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			otherHTTPS, otherHTTP := suNewSide(t, true), suNewSide(t, false)
			var pages []string
			var mu sync.Mutex
			var apiURL string
			api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					fmt.Fprint(w, `{"access_token":"tok-secret","expires_in":3600}`)
					return
				}
				mu.Lock()
				pages = append(pages, r.URL.RequestURI())
				mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer tok-secret" {
					t.Errorf("page without the bearer token: %s", r.URL)
				}
				if r.URL.Query().Get("page") == "2" {
					fmt.Fprint(w, `{"value":[{"number":"P2"}]}`)
					return
				}
				next, _ := json.Marshal(tc.next(apiURL, otherHTTPS.srv.URL, otherHTTP.srv.URL))
				fmt.Fprintf(w, `{"value":[{"number":"P1","lastModifiedDateTime":"2026-10-01T10:00:00Z"}],"@odata.nextLink":%s}`, next)
			}))
			defer api.Close()
			apiURL = api.URL

			c, got, gotMu := newTestConsumer()
			c.httpClient = api.Client() // trusts the test certificate; any address
			cfg := &BCConfig{
				AADTenantID: "t1", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
				Entity: "items", APIBaseURL: api.URL + "/api/v2.0", TokenURL: api.URL + "/token",
				Incremental: tc.incremental, NodeID: "src",
			}
			tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(c.httpClient)
			err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger)

			if tc.incremental {
				if cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src"); cp != nil && cp.LastProcessedMessageID != "" {
					t.Errorf("a watermark was stored (%q) by a poll that stopped", cp.LastProcessedMessageID)
				}
			}
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "127.0.0.1") {
				t.Errorf("the error names the link: %v", err)
			}
			for _, s := range []*suSide{otherHTTPS, otherHTTP} {
				if n := s.hits.Load(); n != 0 {
					t.Errorf("%s got %d requests (Authorization %q), want none", s.srv.URL, n, s.auth.Load())
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(pages) != tc.wantPages {
				t.Fatalf("configured API served %d pages (%v), want %d", len(pages), pages, tc.wantPages)
			}
			if tc.wantPages == 2 && pages[1] != entityPath+"?page=2" {
				t.Errorf("page 2 asked for %s, want %s", pages[1], entityPath+"?page=2")
			}
			gotMu.Lock()
			defer gotMu.Unlock()
			if n := len(*got); n != tc.wantPages {
				t.Errorf("published %d pages, want %d", n, tc.wantPages)
			}
		})
	}
}
