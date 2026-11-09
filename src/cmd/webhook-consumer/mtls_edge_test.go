package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Tests for the edge mTLS path (#454): ingress-nginx checks the caller's
// certificate and forwards it in ssl-client-cert; the worker trusts that
// header only on the edge listener, from a TLS client holding the ingress's
// edge certificate, and verifies the certificate against the connection's
// own CA. All certificates are generated per test.

// edgeLeaf issues a certificate from ca with the given EKU and validity.
func edgeLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string,
	eku []x509.ExtKeyUsage, notBefore, notAfter time.Time) (cert *x509.Certificate, certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
		DNSNames:     []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create %s: %v", cn, err)
	}
	cert, _ = x509.ParseCertificate(der)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// nginxEscape percent-encodes every byte outside [A-Za-z0-9-._~], the way
// $ssl_client_escaped_cert does.
func nginxEscape(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			sb.WriteByte(c)
			continue
		}
		fmt.Fprintf(&sb, "%%%02X", c)
	}
	return sb.String()
}

// pemOf re-encodes a parsed certificate.
func pemOf(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

// edgeFixture is two tenants' CAs, the certificates the cases send, and a
// consumer with an mTLS connection for tenant A and a plain one.
type edgeFixture struct {
	c         *webhookConsumer
	published *[]*envelope.Envelope

	ingressState *tls.ConnectionState // what the edge listener hands a request from the ingress

	validA, expiredA, serverOnlyA, validB *x509.Certificate
}

const (
	edgeConnMTLS  = "conn-edge-mtls"
	edgeConnPlain = "conn-edge-plain"
	edgeTenantA   = "tenant-a"
)

func newEdgeFixture(t *testing.T) *edgeFixture {
	t.Helper()
	now := time.Now()
	caAPEM, caA, caAKey := mtlsCA(t)
	_, caB, caBKey := mtlsCA(t)
	_, edgeCA, edgeKey := mtlsCA(t)
	client := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}

	f := &edgeFixture{}
	f.validA, _, _ = edgeLeaf(t, caA, caAKey, "partner-a", client, now.Add(-time.Hour), now.Add(time.Hour))
	f.expiredA, _, _ = edgeLeaf(t, caA, caAKey, "partner-a-old", client, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
	f.serverOnlyA, _, _ = edgeLeaf(t, caA, caAKey, "server-a", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, now.Add(-time.Hour), now.Add(time.Hour))
	f.validB, _, _ = edgeLeaf(t, caB, caBKey, "partner-b", client, now.Add(-time.Hour), now.Add(time.Hour))
	ingress, _, _ := edgeLeaf(t, edgeCA, edgeKey, "ingress-nginx", client, now.Add(-time.Hour), now.Add(time.Hour))
	f.ingressState = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{ingress},
		VerifiedChains:   [][]*x509.Certificate{{ingress, edgeCA}},
	}

	var mu sync.Mutex
	published := []*envelope.Envelope{}
	f.published = &published
	f.c = &webhookConsumer{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		activeConnections: map[string]*ActiveConnection{
			edgeConnMTLS:  {ConnectionID: edgeConnMTLS, TenantID: edgeTenantA, Cancel: func() {}, ClientCA: caAPEM},
			edgeConnPlain: {ConnectionID: edgeConnPlain, TenantID: edgeTenantA, Cancel: func() {}},
		},
		publish: func(_ context.Context, env *envelope.Envelope) error {
			mu.Lock()
			defer mu.Unlock()
			published = append(published, env)
			return nil
		},
	}
	return f
}

func TestWebhookConsumer_EdgeMTLS(t *testing.T) {
	f := newEdgeFixture(t)
	hdr := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Add(kv[i], kv[i+1])
		}
		return h
	}
	certA := nginxEscape(pemOf(f.validA))

	tests := []struct {
		name       string
		edge       bool                 // edge handler (else the aux-port handler)
		tlsState   *tls.ConnectionState // the request's own TLS connection
		conn       string
		headers    http.Header
		wantStatus int
		wantReason string // webhook_client_cert_failures_total reason that must go up
	}{
		{
			name: "valid certificate for this connection's CA", edge: true, tlsState: f.ingressState, conn: edgeConnMTLS,
			headers:    hdr(headerClientVerify, "SUCCESS", headerClientCert, certA),
			wantStatus: http.StatusAccepted,
		},
		{
			name: "another tenant's CA", edge: true, tlsState: f.ingressState, conn: edgeConnMTLS,
			headers:    hdr(headerClientVerify, "SUCCESS", headerClientCert, nginxEscape(pemOf(f.validB))),
			wantStatus: http.StatusUnauthorized, wantReason: "untrusted_cert",
		},
		{
			name: "expired certificate from this connection's CA", edge: true, tlsState: f.ingressState, conn: edgeConnMTLS,
			headers:    hdr(headerClientVerify, "SUCCESS", headerClientCert, nginxEscape(pemOf(f.expiredA))),
			wantStatus: http.StatusUnauthorized, wantReason: "untrusted_cert",
		},
		{
			name: "server-auth-only certificate from this connection's CA", edge: true, tlsState: f.ingressState, conn: edgeConnMTLS,
			headers:    hdr(headerClientVerify, "SUCCESS", headerClientCert, nginxEscape(pemOf(f.serverOnlyA))),
			wantStatus: http.StatusUnauthorized, wantReason: "untrusted_cert",
		},
		{
			name: "malformed header", edge: true, tlsState: f.ingressState, conn: edgeConnMTLS,
			headers:    hdr(headerClientVerify, "SUCCESS", headerClientCert, "-----BEGIN%20CERTIFICATE-----%0Anope"),
			wantStatus: http.StatusUnauthorized, wantReason: "malformed_cert",
		},
		{
			name: "certificate header twice", edge: true, tlsState: f.ingressState, conn: edgeConnMTLS,
			headers:    hdr(headerClientVerify, "SUCCESS", headerClientCert, certA, headerClientCert, certA),
			wantStatus: http.StatusUnauthorized, wantReason: "malformed_cert",
		},
		{
			name: "missing certificate", edge: true, tlsState: f.ingressState, conn: edgeConnMTLS,
			headers:    hdr(headerClientVerify, "NONE"),
			wantStatus: http.StatusUnauthorized, wantReason: "missing_cert",
		},
		{
			name: "no verify header", edge: true, tlsState: f.ingressState, conn: edgeConnMTLS,
			headers:    hdr(headerClientCert, certA),
			wantStatus: http.StatusUnauthorized, wantReason: "untrusted_cert",
		},
		{
			name: "ingress reports a failed verification", edge: true, tlsState: f.ingressState, conn: edgeConnMTLS,
			headers:    hdr(headerClientVerify, "FAILED:certificate has expired", headerClientCert, certA),
			wantStatus: http.StatusUnauthorized, wantReason: "untrusted_cert",
		},
		{
			name: "verify header twice", edge: true, tlsState: f.ingressState, conn: edgeConnMTLS,
			headers:    hdr(headerClientVerify, "SUCCESS", headerClientVerify, "SUCCESS", headerClientCert, certA),
			wantStatus: http.StatusUnauthorized, wantReason: "untrusted_cert",
		},
		{
			name: "spoofed headers on the edge handler without the ingress connection", edge: true, tlsState: nil, conn: edgeConnMTLS,
			headers:    hdr(headerClientVerify, "SUCCESS", headerClientCert, certA),
			wantStatus: http.StatusUnauthorized, wantReason: "untrusted_edge",
		},
		{
			name: "spoofed headers over TLS without a verified ingress certificate", edge: true,
			tlsState: &tls.ConnectionState{PeerCertificates: f.ingressState.PeerCertificates}, conn: edgeConnMTLS,
			headers:    hdr(headerClientVerify, "SUCCESS", headerClientCert, certA),
			wantStatus: http.StatusUnauthorized, wantReason: "untrusted_edge",
		},
		{
			// The public host has no auth-tls, so nginx passes these headers
			// through to 9100. That port must never read them.
			name: "spoofed headers on the aux port", edge: false, tlsState: nil, conn: edgeConnMTLS,
			headers:    hdr(headerClientVerify, "SUCCESS", headerClientCert, certA),
			wantStatus: http.StatusUnauthorized, wantReason: "missing_cert",
		},
		{
			name: "connection without a client CA on the edge port", edge: true, tlsState: f.ingressState, conn: edgeConnPlain,
			headers:    hdr(headerClientVerify, "SUCCESS", headerClientCert, nginxEscape(pemOf(f.validB))),
			wantStatus: http.StatusAccepted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var before float64
			if tt.wantReason != "" {
				before = testutil.ToFloat64(webhookClientCertFailures.WithLabelValues(tt.conn, tt.wantReason))
			}
			published := len(*f.published)

			req := httptest.NewRequest(http.MethodPost, "/webhook/"+tt.conn, strings.NewReader(`{"ok":true}`))
			req.TLS = tt.tlsState
			for k, vs := range tt.headers {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			rec := httptest.NewRecorder()
			if tt.edge {
				f.c.handleEdgeWebhook()(rec, req)
			} else {
				f.c.handleWebhook()(rec, req)
			}

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %q", rec.Code, tt.wantStatus, rec.Body.String())
			}
			gotPublished := len(*f.published) - published
			if tt.wantStatus == http.StatusAccepted && gotPublished != 1 {
				t.Errorf("published %d envelopes, want 1", gotPublished)
			}
			if tt.wantStatus != http.StatusAccepted && gotPublished != 0 {
				t.Errorf("a refused request published %d envelopes", gotPublished)
			}
			if tt.wantReason != "" {
				after := testutil.ToFloat64(webhookClientCertFailures.WithLabelValues(tt.conn, tt.wantReason))
				if after != before+1 {
					t.Errorf("webhook_client_cert_failures_total{reason=%q} went %v -> %v, want +1", tt.wantReason, before, after)
				}
			}
		})
	}
}

// TestWebhookConsumer_EdgeListener runs the real edge listener: a client
// holding the ingress's edge certificate gets a forwarded certificate
// verified; a client with no certificate, or one from another CA, is refused
// at the handshake, so nothing else on the network can set the header.
func TestWebhookConsumer_EdgeListener(t *testing.T) {
	f := newEdgeFixture(t)
	now := time.Now()
	edgeCAPEM, edgeCA, edgeKey := mtlsCA(t)
	_, srvPEM, srvKey := edgeLeaf(t, edgeCA, edgeKey, "vrsky-webhook-consumer.vrsky-platform.svc",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, now.Add(-time.Hour), now.Add(time.Hour))
	_, ingPEM, ingKey := edgeLeaf(t, edgeCA, edgeKey, "ingress-nginx",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now.Add(-time.Hour), now.Add(time.Hour))
	_, otherCA, otherKey := mtlsCA(t)
	_, fakePEM, fakeKey := edgeLeaf(t, otherCA, otherKey, "ingress-nginx",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now.Add(-time.Hour), now.Add(time.Hour))

	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("WEBHOOK_EDGE_CERT_FILE", write("tls.crt", srvPEM))
	t.Setenv("WEBHOOK_EDGE_KEY_FILE", write("tls.key", srvKey))
	t.Setenv("WEBHOOK_EDGE_CA_FILE", write("ca.crt", edgeCAPEM))
	f.c.edgePort = "0"
	if err := f.c.startEdgeListener(); err != nil {
		t.Fatalf("startEdgeListener: %v", err)
	}
	defer func() { _ = f.c.edgeServer.Shutdown(context.Background()) }()
	addr := strings.Replace(f.c.edgeAddr, "[::]", "127.0.0.1", 1)

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(edgeCAPEM)
	clientFor := func(certPEM, keyPEM []byte) *http.Client {
		cfg := &tls.Config{RootCAs: roots, ServerName: "vrsky-webhook-consumer.vrsky-platform.svc", MinVersion: tls.VersionTLS12}
		if certPEM != nil {
			pair, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				t.Fatal(err)
			}
			cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &pair, nil }
		}
		return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg}}
	}

	tests := []struct {
		name            string
		certPEM, keyPEM []byte
		wantStatus      int // 0: the request must fail at TLS
	}{
		{"ingress certificate", ingPEM, ingKey, http.StatusAccepted},
		{"no client certificate", nil, nil, 0},
		{"certificate from another CA", fakePEM, fakeKey, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "https://"+addr+"/webhook/"+edgeConnMTLS, strings.NewReader(`{"ok":true}`))
			req.Header.Set(headerClientVerify, "SUCCESS")
			req.Header.Set(headerClientCert, nginxEscape(pemOf(f.validA)))
			resp, err := clientFor(tt.certPEM, tt.keyPEM).Do(req)
			if tt.wantStatus == 0 {
				if err == nil {
					resp.Body.Close()
					t.Fatalf("request without the ingress certificate got through: %d", resp.StatusCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d; body %q", resp.StatusCode, tt.wantStatus, body)
			}
		})
	}
}

// TestWebhookConsumer_EdgeListenerNeedsItsFiles: with the port set but the
// edge certificate files missing, the listener refuses to start (there is no
// self-signed fallback that would let anything set the header).
func TestWebhookConsumer_EdgeListenerNeedsItsFiles(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"nothing set", map[string]string{}},
		{"CA missing", map[string]string{"WEBHOOK_EDGE_CERT_FILE": "/nonexistent/tls.crt", "WEBHOOK_EDGE_KEY_FILE": "/nonexistent/tls.key"}},
		{"files do not exist", map[string]string{"WEBHOOK_EDGE_CERT_FILE": "/nonexistent/tls.crt", "WEBHOOK_EDGE_KEY_FILE": "/nonexistent/tls.key", "WEBHOOK_EDGE_CA_FILE": "/nonexistent/ca.crt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"WEBHOOK_EDGE_CERT_FILE", "WEBHOOK_EDGE_KEY_FILE", "WEBHOOK_EDGE_CA_FILE"} {
				t.Setenv(k, tt.env[k])
			}
			c := &webhookConsumer{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), edgePort: "0"}
			if err := c.startEdgeListener(); err == nil {
				_ = c.edgeServer.Shutdown(context.Background())
				t.Fatal("edge listener started without its certificate files")
			}
			if c.edgeServer != nil {
				t.Error("edgeServer set after a failed start")
			}
		})
	}
	// Unset port: nothing to start, no error.
	c := &webhookConsumer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := c.startEdgeListener(); err != nil || c.edgeServer != nil {
		t.Errorf("unset port: err %v, server %v", err, c.edgeServer)
	}
}

// TestWebhookConsumer_EdgeMTLSIntermediate: nginx forwards only the leaf, so
// a client certificate issued by an intermediate verifies only when the
// connection's client CA includes that intermediate. Root alone is refused
// (closed, not open), which is what the docs tell tenants.
func TestWebhookConsumer_EdgeMTLSIntermediate(t *testing.T) {
	f := newEdgeFixture(t)
	now := time.Now()
	rootPEM, root, rootKey := mtlsCA(t)

	intKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	intTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "test-intermediate"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	intDER, err := x509.CreateCertificate(rand.Reader, intTmpl, root, &intKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	inter, _ := x509.ParseCertificate(intDER)
	leaf, _, _ := edgeLeaf(t, inter, intKey, "partner-int", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now.Add(-time.Hour), now.Add(time.Hour))

	tests := []struct {
		name       string
		clientCA   []byte
		wantStatus int
	}{
		{"client CA holds root and intermediate", append(append([]byte{}, rootPEM...), pemOf(inter)...), http.StatusAccepted},
		{"client CA holds only the root", rootPEM, http.StatusUnauthorized},
		{"client CA holds only the intermediate", pemOf(inter), http.StatusAccepted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.c.mu.Lock()
			f.c.activeConnections["conn-int"] = &ActiveConnection{ConnectionID: "conn-int", TenantID: edgeTenantA, Cancel: func() {}, ClientCA: tt.clientCA}
			f.c.mu.Unlock()
			req := httptest.NewRequest(http.MethodPost, "/webhook/conn-int", strings.NewReader(`{"ok":true}`))
			req.TLS = f.ingressState
			req.Header.Set(headerClientVerify, "SUCCESS")
			req.Header.Set(headerClientCert, nginxEscape(pemOf(leaf)))
			rec := httptest.NewRecorder()
			f.c.handleEdgeWebhook()(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %q", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}
