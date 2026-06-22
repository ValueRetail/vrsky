package tlsconfig

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func genCA(t *testing.T) (caPEM []byte, ca *x509.Certificate, caKey *ecdsa.PrivateKey) {
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

func genLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, eku []x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
		DNSNames:     []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func parseLeaf(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return c
}

func TestServerConfig(t *testing.T) {
	caPEM, ca, caKey := genCA(t)
	srvCert, srvKey := genLeaf(t, ca, caKey, "webhook-consumer", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})

	cfg, err := ServerConfig(srvCert, srvKey)
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	if cfg.ClientAuth != tls.RequireAnyClientCert {
		t.Errorf("ClientAuth = %v, want RequireAnyClientCert", cfg.ClientAuth)
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("expected 1 server cert, got %d", len(cfg.Certificates))
	}
	_ = caPEM

	if _, err := ServerConfig([]byte("not a cert"), srvKey); err == nil {
		t.Error("ServerConfig should reject bad cert material")
	}
}

func TestClientConfig(t *testing.T) {
	caPEM, ca, caKey := genCA(t)
	cliCert, cliKey := genLeaf(t, ca, caKey, "client", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	cfg, err := ClientConfig(cliCert, cliKey, caPEM)
	if err != nil {
		t.Fatalf("ClientConfig: %v", err)
	}
	if cfg.RootCAs == nil {
		t.Error("RootCAs should be set when a CA is supplied")
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("expected 1 client cert, got %d", len(cfg.Certificates))
	}

	// Empty CA → system roots (nil RootCAs), no error.
	cfg2, err := ClientConfig(cliCert, cliKey, nil)
	if err != nil {
		t.Fatalf("ClientConfig (no CA): %v", err)
	}
	if cfg2.RootCAs != nil {
		t.Error("RootCAs should be nil (system roots) when no CA supplied")
	}
}

func TestVerifyClientCert(t *testing.T) {
	caPEM, ca, caKey := genCA(t)
	cliCert, _ := genLeaf(t, ca, caKey, "client", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	leaf := parseLeaf(t, cliCert)

	// Signed by the configured CA → accepted.
	if err := VerifyClientCert(leaf, caPEM); err != nil {
		t.Errorf("VerifyClientCert should accept a cert signed by the configured CA: %v", err)
	}

	// A different CA → rejected.
	otherCAPEM, _, _ := genCA(t)
	if err := VerifyClientCert(leaf, otherCAPEM); err == nil {
		t.Error("VerifyClientCert should reject a cert signed by a different CA")
	}
}

func TestSelfSignedServer(t *testing.T) {
	certPEM, keyPEM, err := SelfSignedServer("webhook-consumer")
	if err != nil {
		t.Fatalf("SelfSignedServer: %v", err)
	}
	// Must be usable as a server keypair.
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("self-signed material is not a valid keypair: %v", err)
	}
	// And feed straight into ServerConfig.
	cfg, err := ServerConfig(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("ServerConfig with self-signed cert: %v", err)
	}
	if cfg.ClientAuth != tls.RequireAnyClientCert {
		t.Errorf("ClientAuth = %v, want RequireAnyClientCert", cfg.ClientAuth)
	}
}

func TestNodeConfigEnabled(t *testing.T) {
	if (&NodeConfig{}).Enabled() {
		t.Error("empty NodeConfig should be disabled")
	}
	if !(&NodeConfig{Cert: "x", Key: "y"}).Enabled() {
		t.Error("NodeConfig with cert/key should be enabled")
	}
	var nilCfg *NodeConfig
	if nilCfg.Enabled() {
		t.Error("nil NodeConfig should be disabled")
	}
}
