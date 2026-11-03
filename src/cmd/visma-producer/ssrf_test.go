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

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Tests for #661: base_url and token_url are tenant config, and the token
// request carries the client secret and the API request the bearer token. The
// producer refuses a URL that is not https or that reaches a private address,
// and its clients refuse to dial one.

const publicBase = "https://salesorder.visma.net/api/v3"

func deliverWith(t *testing.T, p *vismaProducer, baseURL, tokenURL string) error {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	nodes := fmt.Sprintf(`[{"id":"dst","type":"producer","config":{"type":"visma","visma":{
		"base_url":%q,"token_url":%q,"scope":"s","client_id":"id","client_secret":"sec","resource":"SalesOrders"}}}]`, baseURL, tokenURL)
	mock.ExpectQuery("SELECT nodes FROM connections").WithArgs("conn-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	p.db = db
	return p.Deliver(context.Background(), &envelope.Envelope{
		TenantID: "tenant-1", IntegrationID: "conn-1", Payload: []byte(`{"orderNumber":"SO1"}`),
	})
}

func TestDeliver_RefusesBadURLs(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"access_token":"t","expires_in":3600}`)
	}))
	defer srv.Close()
	const publicToken = "https://connect.visma.com/connect/token"

	tests := []struct {
		name, baseURL, tokenURL string
	}{
		{"base_url http", srv.URL, publicToken},
		{"base_url https loopback", strings.Replace(srv.URL, "http://", "https://", 1), publicToken},
		{"base_url metadata service", "https://169.254.169.254/latest", publicToken},
		{"base_url private network", "https://10.0.0.5/v2", publicToken},
		{"base_url ipv6 loopback", "https://[::1]/v2", publicToken},
		{"base_url userinfo", "https://u:p@salesorder.visma.net/api/v3", publicToken},
		{"token_url http", publicBase, srv.URL + "/token"},
		{"token_url https loopback", publicBase, strings.Replace(srv.URL, "http://", "https://", 1) + "/token"},
		{"token_url metadata service", publicBase, "https://169.254.169.254/token"},
		{"token_url private network", publicBase, "https://172.16.0.9/token"},
		{"token_url ipv6 loopback", publicBase, "https://[::1]/token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testProducer()
			p.allowPrivate = false // production policy
			err := deliverWith(t, p, tt.baseURL, tt.tokenURL)
			if err == nil {
				t.Fatal("Deliver accepted the URL, want an error")
			}
			// Permanent acks and drops the message; a refusal must be retried
			// and then dead-lettered so the payload can be replayed.
			if sdk.IsPermanent(err) {
				t.Errorf("error %v is Permanent: the message would be dropped instead of dead-lettered", err)
			}
			if strings.HasPrefix(tt.name, "token_url") && !strings.Contains(err.Error(), "token_url") {
				t.Errorf("the error should name token_url, got %v", err)
			}
			if hits.Load() != 0 {
				t.Errorf("the server received %d requests, want none", hits.Load())
			}
		})
	}
}

// The operator's dev flag lifts the check for a mock upstream.
func TestDeliver_DevFlagAllowsMockUpstream(t *testing.T) {
	var apiHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"t","expires_in":3600}`)
			return
		}
		apiHits.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	p := testProducer()
	p.allowPrivate = true
	if err := deliverWith(t, p, srv.URL+"/api/v3", srv.URL+"/token"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if apiHits.Load() != 1 {
		t.Errorf("the mock API received %d requests, want 1", apiHits.Load())
	}
}

// Configure is where the production client is built. These tests run it with
// no client injected and look at what that client does, so a plain
// http.Client fails here and not only in prod.
func configuredProducer(t *testing.T) *vismaProducer {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := &vismaProducer{} // no httpClient: Configure must build it
	if err := p.Configure(context.Background(), &sdk.Resources{
		DB:     db,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return p
}

func TestConfigure_ClientRefusesLoopback(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "")
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
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
	p := configuredProducer(t)
	if !p.allowPrivate {
		t.Fatal("allowPrivate is off with the env var set")
	}
	port, accepted := tcpCounter(t)
	if resp, err := p.httpClient.Get("http://127.0.0.1:" + port + "/"); err == nil {
		_ = resp.Body.Close()
	}
	if n := accepted.Load(); n == 0 {
		t.Error("with ALLOW_PRIVATE_UPSTREAMS=true the client never reached the listener")
	}
}

// A hostname passes the string check; the dial refuses it when it resolves to
// loopback. This covers the API request.
func TestWrite_HostnameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
	tok := tokenServer()
	defer tok.Close()
	port, accepted := tcpCounter(t)

	p := testProducer()
	p.httpClient = safeurl.NewClient(5*time.Second, false) // what Configure builds
	cfg := cfgFor("https://localhost:"+port, tok.URL)
	// A token source that is allowed to reach the test token server, so the
	// failure below is the API request's.
	c := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.Scope).WithHTTPClient(http.DefaultClient)
	if err := p.write(context.Background(), cfg, c, []byte(`{}`)); err == nil {
		t.Fatal("write connected to a hostname that resolves to loopback")
	}
	if accepted.Load() != 0 {
		t.Errorf("the client opened %d connections to loopback, want none", accepted.Load())
	}
}

// Same for the token request, which carries the client secret: Deliver builds
// the token source on the producer's client, so a token_url whose host
// resolves to loopback is never dialled.
func TestDeliver_TokenRequestUsesTheSafeClient(t *testing.T) {
	port, accepted := tcpCounter(t)
	p := testProducer()
	p.allowPrivate = false
	p.httpClient = safeurl.NewClient(5*time.Second, false) // what Configure builds
	err := deliverWith(t, p, publicBase, "https://localhost:"+port+"/token")
	if err == nil {
		t.Fatal("Deliver reached a token_url that resolves to loopback")
	}
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
