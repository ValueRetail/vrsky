package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Tests for #660: http.url is tenant config and the node's headers and OAuth
// token go with every request, so the producer must not reach a private
// address, whether it is written as an IP, resolved from a name, or reached
// through a redirect, and an mTLS node must be guarded the same way. Public
// http and https targets stay reachable.

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

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

// configuredProducer runs Configure with no client injected, so the test looks
// at the client production builds. Replacing the safe client with a plain
// http.Client fails here and not only in prod.
func configuredProducer(t *testing.T) *httpProducer {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := &httpProducer{}
	if err := p.Configure(context.Background(), &sdk.Resources{DB: db, Logger: quietLogger()}); err != nil {
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
	resp, err := p.clientFor(&HTTPConfig{}).Get("http://127.0.0.1:" + port + "/")
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
	p := configuredProducer(t)
	if !p.allowPrivate {
		t.Fatal("allowPrivate is off with the env var set")
	}
	port, accepted := tcpCounter(t)
	if resp, err := p.clientFor(&HTTPConfig{}).Get("http://127.0.0.1:" + port + "/"); err == nil {
		_ = resp.Body.Close()
	}
	if n := accepted.Load(); n == 0 {
		t.Error("with ALLOW_PRIVATE_UPSTREAMS=true the client never reached the listener")
	}
}

// A producer that was never configured still gets the guarded client, and the
// env var does not reach a struct literal.
func TestClientFor_UnconfiguredIsStillGuarded(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
	port, accepted := tcpCounter(t)
	p := &httpProducer{}
	if resp, err := p.clientFor(&HTTPConfig{}).Get("http://127.0.0.1:" + port + "/"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("an unconfigured producer connected to loopback")
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener saw %d connections, want 0", n)
	}
}

// Public targets are legitimate over http and https. A documentation address
// (TEST-NET-1) is public as far as the guard is concerned: the dial gets past
// it and then fails for lack of a route or a timeout, which is not
// ErrBlockedAddress.
func TestSend_PublicTargetsGetPastTheGuard(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "")
	p := configuredProducer(t)
	for _, u := range []string{"http://192.0.2.1/hook", "https://192.0.2.1/hook"} {
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		err := p.sendHTTPRequest(ctx, "conn-1", &HTTPConfig{URL: u, Method: "POST"}, testEnv(), nil)
		cancel()
		if errors.Is(err, safeurl.ErrBlockedAddress) || (err != nil && strings.Contains(err.Error(), "destination refused")) {
			t.Errorf("POST %s was refused by the guard: %v", u, err)
		}
	}
}

func testEnv() *envelope.Envelope {
	return &envelope.Envelope{ID: "env-1", TenantID: "tenant-x", IntegrationID: "conn-1", Payload: []byte(`{"a":1}`)}
}

// A refused target is an ordinary failure: nothing is sent, the error names the
// reason, the activity feed shows it, and the message is not dropped (not
// Permanent), so it is retried on the usual backoff and then dead-lettered
// with its payload intact.
func TestSend_PrivateTargetsAreRefused(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "")
	port, accepted := tcpCounter(t)
	tests := []struct {
		name string
		url  string
		want string // substring of the error
	}{
		{"loopback ip", "http://127.0.0.1:" + port + "/hook", "destination refused"},
		{"loopback ip, https", "https://127.0.0.1:" + port + "/hook", "destination refused"},
		{"ipv6 loopback", "http://[::1]:" + port + "/hook", "destination refused"},
		{"azure metadata", "http://169.254.169.254/metadata/instance", "destination refused"},
		{"azure wire server", "http://168.63.129.16/", "destination refused"},
		{"v4-mapped metadata", "http://[::ffff:169.254.169.254]/", "destination refused"},
		{"private 10", "http://10.0.0.5/admin", "destination refused"},
		{"private 192.168", "https://192.168.1.10/", "destination refused"},
		{"cgnat", "http://100.64.0.1/", "destination refused"},
		{"not http", "ftp://example.com/x", "destination refused"},
		{"no host", "http:///x", "destination refused"},
		// A hostname passes the string check; the dial is what refuses it.
		{"hostname resolving to loopback", "http://localhost:" + port + "/hook", "refused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := configuredProducer(t)
			ch, unsub := p.subscribeEvents("conn-1")
			defer unsub()

			err := p.sendHTTPRequest(context.Background(), "conn-1", &HTTPConfig{
				URL: tt.url, Method: "POST", Headers: map[string]string{"X-Api-Key": "secret"},
			}, testEnv(), nil)
			if err == nil {
				t.Fatalf("POST %s was not refused", tt.url)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
			if sdk.IsPermanent(err) {
				t.Error("a refused delivery must not be Permanent: the payload would be dropped instead of dead-lettered")
			}
			var sawError bool
			for len(ch) > 0 {
				if ev := <-ch; ev.Type == "error" {
					sawError = true
				}
			}
			if !sawError {
				t.Error("no error event reached the activity feed")
			}
		})
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener saw %d connections across the refused targets, want 0", n)
	}
}

// Dial-time refusal wraps ErrBlockedAddress, so a hostname that resolves to a
// private address is told apart from an ordinary network failure.
func TestSend_HostnameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "")
	port, accepted := tcpCounter(t)
	p := configuredProducer(t)
	err := p.sendHTTPRequest(context.Background(), "conn-1",
		&HTTPConfig{URL: "http://localhost:" + port + "/hook", Method: "POST"}, testEnv(), nil)
	if !errors.Is(err, safeurl.ErrBlockedAddress) {
		t.Fatalf("error %v does not wrap ErrBlockedAddress", err)
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the client opened %d connections to loopback, want none", n)
	}
}

// The streaming path shares sendHTTPRequest, so a large offloaded payload is
// guarded the same way, and the body is not read.
func TestDeliverStream_PrivateTargetIsRefused(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "")
	port, accepted := tcpCounter(t)
	p := newStreamingProducer(t, []*HTTPConfig{{URL: "http://127.0.0.1:" + port + "/", Method: "POST"}})
	p.allowPrivate = false // production policy
	body := &countingReader{r: bytes.NewReader([]byte("x"))}
	err := p.DeliverStream(context.Background(), streamEnv(1), body)
	if err == nil {
		t.Fatal("DeliverStream reached a loopback target")
	}
	if sdk.IsPermanent(err) {
		t.Error("a refused delivery must stay retriable")
	}
	if body.n.Load() != 0 {
		t.Error("the stream was read although the target was refused")
	}
	if accepted.Load() != 0 {
		t.Error("the listener saw a connection")
	}
}

type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n.Add(int64(n))
	return n, err
}

// Deliver turns the refusal into sdk.Retriable, which the SDK NAKs.
func TestDeliver_PrivateTargetIsRetriable(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "")
	p := newStreamingProducer(t, []*HTTPConfig{{URL: "http://169.254.169.254/latest/", Method: "POST"}})
	p.allowPrivate = false
	err := p.Deliver(context.Background(), testEnv())
	if err == nil {
		t.Fatal("Deliver sent to the metadata address")
	}
	if sdk.IsPermanent(err) {
		t.Error("a refused delivery must not be Permanent")
	}
}

// Redirects: a hop on the same host is followed; a hop to another host is an
// error and the node's headers never leave.
func TestSend_Redirects(t *testing.T) {
	var otherHits atomic.Int32
	var otherKey atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		otherKey.Store(r.Header.Get("X-Api-Key"))
	}))
	defer other.Close()
	_, otherPort, _ := strings.Cut(strings.TrimPrefix(other.URL, "http://"), ":")

	var landed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/new", http.StatusTemporaryRedirect)
		case "/new":
			landed.Add(1)
		case "/other-host":
			http.Redirect(w, r, "http://localhost:"+otherPort+"/", http.StatusTemporaryRedirect)
		case "/private":
			http.Redirect(w, r, "http://10.0.0.5/admin", http.StatusTemporaryRedirect)
		case "/metadata":
			http.Redirect(w, r, "http://169.254.169.254/metadata/instance", http.StatusTemporaryRedirect)
		}
	}))
	defer srv.Close()

	tests := []struct {
		path    string
		wantErr string // empty = followed and delivered
	}{
		{"/old", ""},
		{"/other-host", "another host"},
		{"/private", "another host"},
		{"/metadata", "another host"},
	}
	for _, tt := range tests {
		t.Run(strings.TrimPrefix(tt.path, "/"), func(t *testing.T) {
			landed.Store(0)
			p := &httpProducer{logger: quietLogger(), allowPrivate: true, // srv is on 127.0.0.1
				eventSubs: map[string][]chan HTTPEvent{}, recentEvents: map[string][]HTTPEvent{}}
			err := p.sendHTTPRequest(context.Background(), "conn-1", &HTTPConfig{
				URL: srv.URL + tt.path, Method: "POST", Headers: map[string]string{"X-Api-Key": "secret"},
			}, testEnv(), nil)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("same-host redirect failed: %v", err)
			case tt.wantErr == "" && landed.Load() != 1:
				t.Errorf("same-host redirect landed %d times, want 1", landed.Load())
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error %v does not contain %q", err, tt.wantErr)
			}
		})
	}
	if otherHits.Load() != 0 {
		t.Errorf("the other host received %d requests, want none (key %v)", otherHits.Load(), otherKey.Load())
	}
}

// --- mTLS ------------------------------------------------------------------

type testPKI struct {
	caPEM, clientCertPEM, clientKeyPEM []byte
	server                             tls.Certificate
}

func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	caKey := key()
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leaf := func(serial int64, cn string, eku x509.ExtKeyUsage) ([]byte, []byte, *ecdsa.PrivateKey, []byte) {
		k := key()
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{eku},
			DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		kb, _ := x509.MarshalECPrivateKey(k)
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), k, der
	}
	srvCertPEM, srvKeyPEM, _, _ := leaf(2, "server", x509.ExtKeyUsageServerAuth)
	srvPair, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	cCert, cKey, _, _ := leaf(3, "tenant-client", x509.ExtKeyUsageClientAuth)
	return testPKI{
		caPEM:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		clientCertPEM: cCert, clientKeyPEM: cKey, server: srvPair,
	}
}

// mtlsServer requires a client certificate signed by the test CA and records
// the common name it saw.
func mtlsServer(t *testing.T, pki testPKI) (srv *httptest.Server, gotCN *atomic.Value, hits *atomic.Int32) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pki.caPEM)
	gotCN, hits = new(atomic.Value), new(atomic.Int32)
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if len(r.TLS.PeerCertificates) > 0 {
			gotCN.Store(r.TLS.PeerCertificates[0].Subject.CommonName)
		}
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pki.server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, gotCN, hits
}

// mtlsConfigs loads the node config through getHTTPConfigs, as production does,
// so the mTLS client is the one the producer builds.
func mtlsConfigs(t *testing.T, p *httpProducer, pki testPKI, url string) []*HTTPConfig {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p.db = db
	p.logger = quietLogger()
	p.configCache = map[string][]*HTTPConfig{}
	p.configCacheTime = map[string]time.Time{}
	p.configCacheTTL = time.Hour
	p.eventSubs = map[string][]chan HTTPEvent{}
	p.recentEvents = map[string][]HTTPEvent{}

	nodes, _ := json.Marshal([]map[string]any{{
		"id": "prod1", "type": "producer",
		"config": map[string]any{
			"type": "http",
			"http": map[string]any{"url": url, "method": "POST"},
			"tls": map[string]any{
				"cert": string(pki.clientCertPEM), "key": string(pki.clientKeyPEM), "client_ca": string(pki.caPEM),
			},
		},
	}})
	mock.ExpectQuery("FROM connections WHERE id").WithArgs("conn-1").
		WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow(nodes, []byte(`[]`)))
	cfgs, err := p.getHTTPConfigs(context.Background(), "conn-1", "tenant-x")
	if err != nil {
		t.Fatalf("getHTTPConfigs: %v", err)
	}
	return cfgs
}

// mTLS still works on top of the safe dialer: the node's client certificate is
// presented and its CA is the one trusted.
func TestMTLS_ClientCertStillPresented(t *testing.T) {
	pki := newTestPKI(t)
	srv, gotCN, hits := mtlsServer(t, pki)
	p := &httpProducer{allowPrivate: true} // the test server is on 127.0.0.1
	cfgs := mtlsConfigs(t, p, pki, srv.URL)
	if len(cfgs) != 1 || cfgs[0].client == nil {
		t.Fatalf("expected one config with an mTLS client, got %+v", cfgs)
	}
	if err := p.sendHTTPRequest(context.Background(), "conn-1", cfgs[0], testEnv(), nil); err != nil {
		t.Fatalf("mTLS delivery failed: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("server hits = %d, want 1", hits.Load())
	}
	if cn, _ := gotCN.Load().(string); cn != "tenant-client" {
		t.Errorf("server saw client CN %q, want tenant-client", cn)
	}
}

// An mTLS node without the client cert does not get through the server, which
// proves the test above is exercising the configured certificate.
func TestMTLS_ServerRejectsPlainClient(t *testing.T) {
	pki := newTestPKI(t)
	srv, _, hits := mtlsServer(t, pki)
	p := &httpProducer{logger: quietLogger(), allowPrivate: true,
		eventSubs: map[string][]chan HTTPEvent{}, recentEvents: map[string][]HTTPEvent{}}
	if err := p.sendHTTPRequest(context.Background(), "conn-1", &HTTPConfig{URL: srv.URL, Method: "POST"}, testEnv(), nil); err == nil {
		t.Fatal("a client without a certificate got through")
	}
	if hits.Load() != 0 {
		t.Errorf("server hits = %d, want 0", hits.Load())
	}
}

// The safe dialer is under the mTLS client too: a hostname that resolves to
// loopback is refused before any TLS handshake.
func TestMTLS_ClientRefusesLoopbackAtDial(t *testing.T) {
	pki := newTestPKI(t)
	srv, _, hits := mtlsServer(t, pki)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	p := &httpProducer{} // production policy
	cfgs := mtlsConfigs(t, p, pki, "https://localhost:"+port+"/")
	if cfgs[0].client == nil {
		t.Fatal("no mTLS client was built")
	}
	err := p.sendHTTPRequest(context.Background(), "conn-1", cfgs[0], testEnv(), nil)
	if !errors.Is(err, safeurl.ErrBlockedAddress) {
		t.Fatalf("error %v does not wrap ErrBlockedAddress", err)
	}
	if hits.Load() != 0 {
		t.Errorf("the server received %d requests, want none", hits.Load())
	}
}

// The mTLS client does not follow a redirect away from the host either (here
// to http on another host, which both the scheme and the host rules refuse).
func TestMTLS_ClientDoesNotFollowCrossHostRedirect(t *testing.T) {
	pki := newTestPKI(t)
	var other atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { other.Add(1) }))
	defer target.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pki.caPEM)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+port+"/", http.StatusTemporaryRedirect)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pki.server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	defer srv.Close()

	p := &httpProducer{allowPrivate: true}
	cfgs := mtlsConfigs(t, p, pki, srv.URL)
	err := p.sendHTTPRequest(context.Background(), "conn-1", cfgs[0], testEnv(), nil)
	if err == nil || !strings.Contains(err.Error(), "redirect") || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("error %v, want a refused redirect", err)
	}
	if other.Load() != 0 {
		t.Errorf("the redirect target received %d requests, want none", other.Load())
	}
}
