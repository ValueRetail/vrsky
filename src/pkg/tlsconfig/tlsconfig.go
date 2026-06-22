// Package tlsconfig builds *tls.Config values for the HTTP connectors' mutual-TLS
// support (Phase 3F, #89). Cert material is supplied as PEM bytes — the workers
// resolve the connector's tls.*_secret_id references to plaintext via
// crypto.ResolveSecrets before calling in here, so this package never touches
// the secrets vault.
package tlsconfig

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// NodeConfig is the resolved (plaintext PEM) mTLS material a connector node
// carries under its "tls" block. In the stored config these are *_secret_id
// references; crypto.ResolveSecrets replaces them with the PEM values below.
//
//	consumer (webhook): ClientCA verifies inbound client certs;
//	                    Cert/Key are the listener's server identity.
//	producer (http):    Cert/Key are the client cert presented to the endpoint;
//	                    ClientCA (optional) is the server CA to trust.
type NodeConfig struct {
	ClientCA string `json:"client_ca"`
	Cert     string `json:"cert"`
	Key      string `json:"key"`
}

// Enabled reports whether the block carries any mTLS material.
func (c *NodeConfig) Enabled() bool {
	return c != nil && (c.ClientCA != "" || c.Cert != "" || c.Key != "")
}

// ServerConfig builds a server-side *tls.Config that presents serverCert/Key and
// requires the client to present *a* certificate (RequireAnyClientCert). The
// per-connection trust decision is made at the application layer with
// VerifyClientCert, because a single shared listener can't bind one CA pool per
// connection at handshake time.
func ServerConfig(serverCert, serverKey []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(serverCert, serverKey)
	if err != nil {
		return nil, fmt.Errorf("server keypair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// VerifyClientCert reports whether the presented chain (leaf first, any
// intermediates following — i.e. tls.ConnectionState.PeerCertificates) chains to
// clientCA. Used by the consumer to enforce the per-connection client CA after
// the handshake captured the cert. Intermediates from the chain are added to a
// pool so a leaf signed by an intermediate under a configured root still
// verifies. EKU is restricted to client-auth: a server-auth-only leaf signed by
// the same CA is rejected (a leaf with no EKU still validates, per x509).
func VerifyClientCert(chain []*x509.Certificate, clientCA []byte) error {
	if len(chain) == 0 {
		return fmt.Errorf("no client certificate presented")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(clientCA) {
		return fmt.Errorf("client CA: no certificates parsed")
	}
	intermediates := x509.NewCertPool()
	for _, c := range chain[1:] {
		intermediates.AddCert(c)
	}
	_, err := chain[0].Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	return err
}

// ClientConfig builds a client-side *tls.Config. When clientCert/Key are both
// supplied it presents that client cert (mTLS); supplying only one is a config
// error. When rootCA is non-empty it is the only trusted server CA; empty →
// system roots. A rootCA-only config (no client cert) just pins the server CA.
func ClientConfig(clientCert, clientKey, rootCA []byte) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case len(clientCert) > 0 && len(clientKey) > 0:
		cert, err := tls.X509KeyPair(clientCert, clientKey)
		if err != nil {
			return nil, fmt.Errorf("client keypair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	case len(clientCert) > 0 || len(clientKey) > 0:
		return nil, fmt.Errorf("client cert and key must both be set (got cert=%t key=%t)", len(clientCert) > 0, len(clientKey) > 0)
	}
	if len(rootCA) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(rootCA) {
			return nil, fmt.Errorf("root CA: no certificates parsed")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// SelfSignedServer generates a throwaway self-signed server certificate for host.
// The webhook consumer's mTLS listener uses this as a dev fallback when no server
// cert (WEBHOOK_TLS_CERT_FILE/_KEY_FILE) is configured: mTLS security here comes
// from verifying the *client* cert (see VerifyClientCert), not from the client
// trusting this server cert, so a self-signed identity is acceptable for dev.
func SelfSignedServer(host string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{host},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create cert: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}
