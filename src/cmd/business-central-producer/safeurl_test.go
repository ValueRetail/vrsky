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

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Tests for #686: api_base_url and token_url are tenant config, the token
// request carries the client secret and the write the bearer token.

// suUpstream is a fake upstream at the transport level: every request the
// producer sends, to whatever address, lands here and is recorded. Without the
// URL check a refused config would show up as a recorded request.
type suUpstream struct {
	mu   sync.Mutex
	urls []string
}

func (u *suUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	u.mu.Lock()
	u.urls = append(u.urls, r.Method+" "+r.URL.String())
	u.mu.Unlock()
	status, body := http.StatusCreated, `{}`
	if strings.Contains(r.URL.Path, "token") {
		status, body = http.StatusOK, `{"access_token":"tok","expires_in":3600}`
	}
	return &http.Response{
		StatusCode: status,
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

// suTokenStub answers the token request itself and hands every other request
// to next, so the write reaches the real dial check.
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
		cfg          BCProducerConfig
		allowPrivate bool
		want         string // "" = allowed
	}
	cases := []tc{
		{name: "defaults", cfg: BCProducerConfig{AADTenantID: "t1", CompanyID: "GUID"}},
		{name: "public overrides", cfg: BCProducerConfig{AADTenantID: "t1",
			APIBaseURL: "https://bc.example.com:7048/BC/api/v2.0", TokenURL: "https://login.example.com/t1/oauth2/v2.0/token"}},
		{name: "dev switch allows a loopback mock", allowPrivate: true,
			cfg: BCProducerConfig{APIBaseURL: "http://127.0.0.1:8080/api/v2.0", TokenURL: "http://127.0.0.1:8080/token"}},
		{name: "dev switch still refuses userinfo", allowPrivate: true,
			cfg: BCProducerConfig{APIBaseURL: "http://u:p@127.0.0.1/api/v2.0"}, want: "user name"},
	}
	for _, b := range suBadURLs {
		cases = append(cases, tc{name: b.name, cfg: BCProducerConfig{AADTenantID: "t1", APIBaseURL: b.api, TokenURL: b.token}, want: b.want})
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

// suDeliver runs Deliver for one destination node through the real config
// lookup, with the given HTTP client.
func suDeliver(t *testing.T, api, token string, client *http.Client, allowPrivate bool) error {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bc := map[string]any{
		"aad_tenant_id": "t1", "company_id": "GUID", "client_id": "cid", "client_secret": "sec", "entity": "items",
	}
	if api != "" {
		bc["api_base_url"] = api
	}
	if token != "" {
		bc["token_url"] = token
	}
	nodes, _ := json.Marshal([]map[string]any{{
		"id": "dst", "type": "producer",
		"config": map[string]any{"type": "business_central", "business_central": bc},
	}})
	mock.ExpectQuery("SELECT nodes, edges FROM connections").WithArgs("conn-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow(nodes, []byte(`[]`)))
	p := &bcProducer{
		db:           db,
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient:   client,
		allowPrivate: allowPrivate,
	}
	return p.Deliver(context.Background(), &envelope.Envelope{
		ID: "env-1", TenantID: "tenant-1", IntegrationID: "conn-1", Payload: []byte(`{"number":"1000"}`),
	})
}

// A refused URL sends nothing and is retried, then dead-lettered with its
// payload: not sdk.Permanent, which acks and drops the message.
func TestDeliver_RefusesBadURLs(t *testing.T) {
	for _, b := range suBadURLs {
		t.Run(b.name, func(t *testing.T) {
			up := &suUpstream{}
			err := suDeliver(t, b.api, b.token, &http.Client{Transport: up}, false)
			if err == nil {
				t.Fatal("delivered, want refused")
			}
			if sdk.IsPermanent(err) {
				t.Errorf("refusal is Permanent (acked and dropped): %v", err)
			}
			if !strings.Contains(err.Error(), b.want) {
				t.Errorf("error %q does not say %q", err, b.want)
			}
			if sent := up.seen(); len(sent) != 0 {
				t.Errorf("requests sent: %v, want none", sent)
			}
		})
	}
}

func TestDeliver_DefaultsAllowed(t *testing.T) {
	up := &suUpstream{}
	if err := suDeliver(t, "", "", &http.Client{Transport: up}, false); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	want := []string{
		"POST https://login.microsoftonline.com/t1/oauth2/v2.0/token",
		"POST https://api.businesscentral.dynamics.com/v2.0/t1/Production/api/v2.0/companies(GUID)/items",
	}
	if got := up.seen(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

func TestDeliver_DevSwitchReachesLoopbackMock(t *testing.T) {
	var writes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
			return
		}
		writes.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	// What Configure builds with ALLOW_PRIVATE_UPSTREAMS=true.
	if err := suDeliver(t, srv.URL+"/api/v2.0", srv.URL+"/token", safeurl.NewClient(5*time.Second, true), true); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if n := writes.Load(); n != 1 {
		t.Errorf("writes = %d, want 1", n)
	}
}

// A host name passes the string check; the dial refuses where it resolves to.
// The error becomes the DLQ's X-Vrsky-Last-Error, so it must not say which
// address that was, and it must stay retriable.
func TestDeliver_HostnameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
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
			err := suDeliver(t, tc.api, tc.token, tc.client, false)
			if err == nil {
				t.Fatal("delivered, want refused")
			}
			if !errors.Is(err, safeurl.ErrBlockedAddress) {
				t.Errorf("err = %v, want safeurl.ErrBlockedAddress", err)
			}
			if sdk.IsPermanent(err) {
				t.Errorf("refusal is Permanent (acked and dropped): %v", err)
			}
			if n := accepted.Load(); n != 0 {
				t.Errorf("%d connections reached loopback, want none", n)
			}
			for _, leak := range []string{"127.0.0.1", "::1", "dial tcp"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("the error leaks the resolved address (%q): %v", leak, err)
				}
			}
		})
	}
}

// Configure is where the production client is built. These run it with no
// client injected and look at what that client does.
func suConfigured(t *testing.T) *bcProducer {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := &bcProducer{} // no httpClient: Configure must build it
	if err := p.Configure(context.Background(), &sdk.Resources{
		DB:     db,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return p
}

func TestConfigure_ClientRefusesLoopback(t *testing.T) {
	t.Setenv(safeurl.EnvAllowPrivate, "")
	p := suConfigured(t)
	if p.allowPrivate {
		t.Fatal("allowPrivate is on without the env var")
	}
	port, accepted := suTCPCounter(t)
	resp, err := p.httpClient.Get("http://127.0.0.1:" + port + "/")
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
	p := suConfigured(t)
	if !p.allowPrivate {
		t.Fatal("allowPrivate is off with the env var set")
	}
	port, accepted := suTCPCounter(t)
	if resp, err := p.httpClient.Get("http://127.0.0.1:" + port + "/"); err == nil {
		_ = resp.Body.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for accepted.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if accepted.Load() == 0 {
		t.Error("with ALLOW_PRIVATE_UPSTREAMS=true the client never reached the listener")
	}
}
