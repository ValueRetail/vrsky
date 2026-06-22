package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// mtlsCA generates a throwaway CA and returns its PEM plus the signing material.
func mtlsCA(t *testing.T) (caPEM []byte, ca *x509.Certificate, caKey *ecdsa.PrivateKey) {
	t.Helper()
	caKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	ca, _ = x509.ParseCertificate(der)
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return caPEM, ca, caKey
}

// mtlsClientCert returns a parsed client leaf certificate signed by ca.
func mtlsClientCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client leaf: %v", err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return leaf
}

// TestWebhookConsumer_MTLS verifies the #89 client-cert path: a connection with
// a tls.client_ca block rejects requests that present no client cert (401) or a
// cert signed by a different CA (401), and accepts one signed by the configured
// CA (202, published). Plain-HTTP requests (r.TLS == nil) are treated as
// "no cert presented", which closes the SDK plain-port bypass.
func TestWebhookConsumer_MTLS(t *testing.T) {
	const (
		connID = "conn-wh-3"
		tenant = "tenant-x"
	)

	caPEM, ca, caKey := mtlsCA(t)
	validCert := mtlsClientCert(t, ca, caKey)
	_, otherCA, otherKey := mtlsCA(t)
	foreignCert := mtlsClientCert(t, otherCA, otherKey)

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)

	nodesObj := []map[string]any{{
		"id":   "c1",
		"type": "consumer",
		"config": map[string]any{
			"type": "http",
			"tls":  map[string]any{"client_ca": string(caPEM)},
		},
	}}
	nodes, _ := json.Marshal(nodesObj)
	mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(connID, tenant, "WH Conn", nodes, []byte(`[]`)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	// Only the valid request publishes → exactly one last_payload write.
	mock.ExpectExec("UPDATE connections SET last_payload").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &webhookConsumer{}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "webhook-consumer", DB: mgmtDB})
	startConn(t, h, c, connID, tenant)

	body := `{"secure":true}`

	// No client cert (plain HTTP) → 401.
	noCert := httptest.NewRequest("POST", "/webhook/"+connID, strings.NewReader(body))
	noRec := httptest.NewRecorder()
	c.handleWebhook()(noRec, noCert)
	if noRec.Code != http.StatusUnauthorized {
		t.Fatalf("no-cert status = %d, want 401", noRec.Code)
	}

	// Cert signed by a different CA → 401.
	wrong := httptest.NewRequest("POST", "/webhook/"+connID, strings.NewReader(body))
	wrong.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{foreignCert}}
	wrongRec := httptest.NewRecorder()
	c.handleWebhook()(wrongRec, wrong)
	if wrongRec.Code != http.StatusUnauthorized {
		t.Fatalf("foreign-cert status = %d, want 401", wrongRec.Code)
	}

	// Cert signed by the configured CA → 202, published.
	good := httptest.NewRequest("POST", "/webhook/"+connID, strings.NewReader(body))
	good.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{validCert}}
	goodRec := httptest.NewRecorder()
	c.handleWebhook()(goodRec, good)
	if goodRec.Code != http.StatusAccepted {
		t.Fatalf("valid-cert status = %d, want 202; body=%s", goodRec.Code, goodRec.Body.String())
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if string(got.Payload) != body {
		t.Errorf("payload = %q, want %q", got.Payload, body)
	}
}
