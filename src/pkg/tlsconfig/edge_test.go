package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// escapeAll percent-encodes every byte outside [A-Za-z0-9-._~], the way
// nginx's $ssl_client_escaped_cert does (NGX_ESCAPE_URI_COMPONENT), '+' and
// '/' included.
func escapeAll(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func TestParseForwardedCert(t *testing.T) {
	_, ca, caKey := genCA(t)
	certPEM, keyPEM := genLeaf(t, ca, caKey, "client", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	want := parseLeaf(t, certPEM)

	tests := []struct {
		name    string
		header  string
		wantErr bool
	}{
		{"nginx escaping", escapeAll(string(certPEM)), false},
		// url.PathEscape leaves '+' literal: it must not turn into a space.
		{"literal plus signs", url.PathEscape(string(certPEM)), false},
		{"trailing whitespace", escapeAll(string(certPEM) + "\n\n"), false},
		{"empty", "", true},
		{"not escaped PEM at all", "garbage", true},
		{"bad escape", "%zz" + escapeAll(string(certPEM)), true},
		{"two certificates", escapeAll(string(certPEM) + string(certPEM)), true},
		{"data after the certificate", escapeAll(string(certPEM) + "junk"), true},
		{"data before the certificate", escapeAll("junk\n" + string(certPEM)), true},
		{"a key, not a certificate", escapeAll(string(keyPEM)), true},
		{"certificate block with bad DER", escapeAll("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"), true},
		{"PEM headers", escapeAll(strings.Replace(string(certPEM), "-----\n", "-----\nProc-Type: 4,ENCRYPTED\n\n", 1)), true},
		{"certificate bytes under another PEM type", escapeAll(strings.ReplaceAll(string(certPEM), "CERTIFICATE", "TRUSTED CERTIFICATE")), true},
		// A valid certificate padded past the cap: refused for its length alone.
		{"too long", escapeAll(string(certPEM) + strings.Repeat(" ", MaxForwardedCertLen)), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseForwardedCert(tt.header)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want an error, got a certificate")
				}
				if strings.Contains(err.Error(), "BEGIN") || strings.Contains(err.Error(), "%") {
					t.Errorf("error quotes the header: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseForwardedCert: %v", err)
			}
			if !got.Equal(want) {
				t.Error("parsed a different certificate")
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	_, ca, caKey := genCA(t)
	aPEM, _ := genLeaf(t, ca, caKey, "a", nil)
	bPEM, _ := genLeaf(t, ca, caKey, "b", nil)
	a, b := parseLeaf(t, aPEM), parseLeaf(t, bPEM)
	if len(Fingerprint(a)) != 64 {
		t.Errorf("fingerprint %q is not hex SHA-256", Fingerprint(a))
	}
	if Fingerprint(a) == Fingerprint(b) {
		t.Error("two certificates share a fingerprint")
	}
}

// TestEdgeServerConfig checks the ingress-to-worker hop over a real TLS
// handshake: the edge CA's client certificate gets through; no certificate,
// one from another CA, or a server-auth-only one from the edge CA do not.
func TestEdgeServerConfig(t *testing.T) {
	edgeCAPEM, edgeCA, edgeKey := genCA(t)
	srvCert, srvKey := genLeaf(t, edgeCA, edgeKey, "webhook-consumer", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	ingCert, ingKey := genLeaf(t, edgeCA, edgeKey, "ingress", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	serverOnlyCert, serverOnlyKey := genLeaf(t, edgeCA, edgeKey, "other", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	_, otherCA, otherKey := genCA(t)
	foreignCert, foreignKey := genLeaf(t, otherCA, otherKey, "ingress", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	cfg, err := EdgeServerConfig(srvCert, srvKey, edgeCAPEM)
	if err != nil {
		t.Fatalf("EdgeServerConfig: %v", err)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", cfg.ClientAuth)
	}
	if _, err := EdgeServerConfig(srvCert, srvKey, []byte("no CA")); err == nil {
		t.Error("EdgeServerConfig accepted an edge CA with no certificates")
	}
	if _, err := EdgeServerConfig([]byte("bad"), srvKey, edgeCAPEM); err == nil {
		t.Error("EdgeServerConfig accepted bad server material")
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.(*tls.Conn).Handshake()
				_, _ = c.Write([]byte("ok"))
			}()
		}
	}()

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(edgeCAPEM)
	pair := func(c, k []byte) []tls.Certificate {
		p, err := tls.X509KeyPair(c, k)
		if err != nil {
			t.Fatalf("keypair: %v", err)
		}
		return []tls.Certificate{p}
	}
	tests := []struct {
		name   string
		certs  []tls.Certificate
		wantOK bool
	}{
		{"ingress certificate", pair(ingCert, ingKey), true},
		{"no certificate", nil, false},
		{"another CA", pair(foreignCert, foreignKey), false},
		{"edge CA but server-auth only", pair(serverOnlyCert, serverOnlyKey), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
				RootCAs:    roots,
				ServerName: "webhook-consumer",
				// Send the certificate whatever CA names the server lists.
				GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
					if len(tt.certs) == 0 {
						return &tls.Certificate{}, nil
					}
					return &tt.certs[0], nil
				},
				MinVersion: tls.VersionTLS12,
			})
			ok := err == nil
			if ok {
				// TLS 1.3 reports a refused client certificate on the first read.
				buf := make([]byte, 2)
				_, rerr := conn.Read(buf)
				ok = rerr == nil && string(buf) == "ok"
				conn.Close()
			}
			if ok != tt.wantOK {
				t.Errorf("handshake ok = %v, want %v (err %v)", ok, tt.wantOK, err)
			}
		})
	}
}
