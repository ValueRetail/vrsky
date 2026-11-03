package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// Tests for #677: base_url is tenant config and every delivery carries both
// keys, so the producer must not call a base_url that is not https or that
// reaches a private, loopback or link-local address. A refusal is a plain
// error, not sdk.Permanent: Permanent acks and drops the message, a plain
// error is retried and ends in the DLQ with its payload.

func TestCheckBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		allowPrivate bool
		ok           bool
	}{
		{"public apim host", "https://fs-partner.azure-api.net", false, true},
		{"public host with a path", "https://fs-partner.azure-api.net/frontsystems/", false, true},
		{"empty", "", false, false},
		{"bare host", "fs-partner.azure-api.net", false, false},
		{"http", "http://fs-partner.azure-api.net", false, false},
		{"loopback", "https://127.0.0.1:8443", false, false},
		{"loopback v6", "https://[::1]/", false, false},
		{"private 10/8", "https://10.0.0.5/", false, false},
		{"cloud metadata", "https://169.254.169.254/latest/meta-data/", false, false},
		{"azure wire server", "https://168.63.129.16/", false, false},
		{"userinfo", "https://u:hunter2@fs-partner.azure-api.net", false, false},
		{"query", "https://fs-partner.azure-api.net/?x=1", false, false},
		{"dev flag: http mock", "http://fs-mock:8080", true, true},
		{"dev flag: loopback mock", "http://127.0.0.1:9000", true, true},
		{"dev flag still refuses userinfo", "http://u:hunter2@127.0.0.1:9000", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkBaseURL(tt.raw, tt.allowPrivate)
			if tt.ok && err != nil {
				t.Errorf("checkBaseURL(%q) = %v, want nil", tt.raw, err)
			}
			if !tt.ok && err == nil {
				t.Errorf("checkBaseURL(%q) = nil, want an error", tt.raw)
			}
			if err != nil && strings.Contains(err.Error(), "hunter2") {
				t.Errorf("checkBaseURL(%q) = %v, the password is in the error", tt.raw, err)
			}
		})
	}
}

// A refused base_url sends nothing, and the error is not Permanent.
func TestWrite_RefusesBadBaseURL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	port, accepted := tcpCounter(t)

	tests := []struct {
		name string
		base string
	}{
		{"http to the fake itself", srv.URL},
		{"https loopback", "https://127.0.0.1:" + port},
		{"loopback v6", "https://[::1]:" + port},
		{"private network", "https://10.0.0.5"},
		{"cloud metadata", "https://169.254.169.254/latest"},
		{"azure wire server", "https://168.63.129.16"},
		{"userinfo", "https://u:hunter2@127.0.0.1:" + port},
		{"bare host", "fs-partner.azure-api.net"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testProducer()
			p.allowPrivate = false // production policy
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := p.write(ctx, cfgFor(tt.base), []byte(`[{"extId":"A1"}]`))
			if err == nil {
				t.Fatal("write succeeded, want the base_url refused")
			}
			if sdk.IsPermanent(err) {
				t.Errorf("error is Permanent (acked and dropped); want a plain error that ends in the DLQ: %v", err)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("the password is in the error: %v", err)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the fake Front Systems received %d requests, want none", n)
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the loopback listener saw %d connections, want none", n)
	}
}

// Deliver goes through the same check, and its refusal is not Permanent
// either, so the runner NAKs the message rather than acking it.
func TestDeliver_RefusedBaseURLIsNotPermanent(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	nodes := `[{"id":"n2","type":"producer","config":{"type":"front_systems","front_systems":` +
		`{"base_url":"` + srv.URL + `","subscription_key":"sub","api_key":"key"}}}]`
	mock.ExpectQuery(`SELECT nodes FROM connections WHERE id = \$1 AND tenant_id = \$2`).
		WithArgs("conn-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))

	p := testProducer()
	p.db = db
	p.allowPrivate = false
	env := envelope.New()
	env.TenantID, env.IntegrationID = "tenant-1", "conn-1"
	env.Payload = []byte(`[{"extId":"A1"}]`)

	err = p.Deliver(context.Background(), env)
	if err == nil {
		t.Fatal("Deliver succeeded, want the base_url refused")
	}
	if sdk.IsPermanent(err) {
		t.Errorf("Deliver error is Permanent (acked and dropped); want a plain error: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the fake Front Systems received %d requests, want none", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}

// A host name that resolves to loopback passes the string check and is
// refused at dial. That is a normal, retriable error as well.
func TestWrite_HostnameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
	port, accepted := tcpCounter(t)
	p := testProducer()
	p.allowPrivate = false
	p.httpClient = safeurl.NewClient(5*time.Second, false)

	err := p.write(context.Background(), cfgFor("https://localhost:"+port), []byte(`{}`))
	if err == nil {
		t.Fatal("write succeeded, want a dial refusal")
	}
	if !errors.Is(err, safeurl.ErrBlockedAddress) {
		t.Errorf("error = %v, want a dial refusal", err)
	}
	if sdk.IsPermanent(err) {
		t.Errorf("dial refusal is Permanent; want a plain error that ends in the DLQ: %v", err)
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener saw %d connections, want 0", n)
	}
	// The error ends up in the DLQ, where the tenant reads it: it must not
	// say which internal address the host resolved to.
	for _, leak := range []string{"::1", "127.0.0.1", port} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q shows %q to the tenant", err, leak)
		}
	}
}

// A public https base_url is called with the production policy.
func TestWrite_PublicBaseURLIsCalled(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	rt := newRewriteTransport(t, srv.URL)
	p := testProducer()
	p.allowPrivate = false
	p.httpClient = &http.Client{Transport: rt, Timeout: 5 * time.Second}

	const base = "https://fs-partner.azure-api.net"
	if err := p.write(context.Background(), cfgFor(base), []byte(`[{"extId":"A1"}]`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := rt.seen(); len(got) != 1 || got[0] != base+"/api/Products/bulk-upsert" {
		t.Errorf("requests = %v, want one to %s/api/Products/bulk-upsert", got, base)
	}
	if path != "/api/Products/bulk-upsert" {
		t.Errorf("the fake saw path %q", path)
	}
}

// Configure builds the production client. These run it with no client
// injected, so a plain http.Client in Configure fails here.
func configuredProducer(t *testing.T) *frontSystemsProducer {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := &frontSystemsProducer{} // no httpClient: Configure must build it
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
	p := configuredProducer(t)
	if p.allowPrivate {
		t.Fatal("allowPrivate is on without the env var")
	}
	port, accepted := tcpCounter(t)
	resp, err := p.httpClient.Get("http://127.0.0.1:" + port + "/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the configured client connected to loopback")
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener saw %d connections, want 0", n)
	}
}

func TestConfigure_ClientReachesLoopbackWithDevFlag(t *testing.T) {
	t.Setenv(safeurl.EnvAllowPrivate, "true")
	p := configuredProducer(t)
	if !p.allowPrivate {
		t.Fatal("allowPrivate is off with the env var set")
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := p.write(context.Background(), cfgFor(srv.URL), []byte(`{}`)); err != nil {
		t.Fatalf("write with ALLOW_PRIVATE_UPSTREAMS=true: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("the mock received %d requests, want 1", hits.Load())
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
