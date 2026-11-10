package safeurl

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// What a tenant may be shown for a failed request (#717): a fixed text per
// failure, never a resolved address, a resolver address or a credential.
const (
	leakIP       = "10.1.2.3"
	leakResolver = "10.0.0.10:53"
	leakSecret   = "s3cr3t-key"
)

func assertNoLeak(t *testing.T, got string) {
	t.Helper()
	for _, leak := range []string{leakIP, leakResolver, leakSecret, "127.0.0.1", "::1", "lookup", "dial tcp", "http"} {
		if strings.Contains(got, leak) {
			t.Errorf("tenant text %q leaks %q", got, leak)
		}
	}
}

func TestUserMessage(t *testing.T) {
	credURL := "https://apikey:" + leakSecret + "@api.example.com/v1?api_key=" + leakSecret
	wrap := func(err error) error { return &url.Error{Op: "Get", URL: credURL, Err: err} }

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"dial refused", wrap(&net.OpError{Op: "dial", Net: "tcp", Err: &blockedError{addr: leakIP}}), "target address not allowed"},
		{"dns not found", wrap(&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{
			Err: "no such host", Name: "svc.internal", Server: leakResolver, IsNotFound: true}}), "host name could not be resolved"},
		{"dns bare", &net.DNSError{Err: "server misbehaving", Name: "x", Server: leakResolver}, "host name could not be resolved"},
		{"connection refused", wrap(&net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP(leakIP), Port: 443},
			Err: errors.New("connect: connection refused")}), "connection failed"},
		{"timeout", wrap(context.DeadlineExceeded), "request timed out"},
		{"cancelled", wrap(context.Canceled), "request cancelled"},
		{"tls", wrap(x509.UnknownAuthorityError{}), "TLS certificate could not be verified"},
		{"credential url, unclassified", wrap(errors.New("unexpected EOF " + leakSecret)), "request failed"},
		{"redirect", wrap(&redirectError{"redirect to another host refused (a.example.com to b.example.com)"}),
			"redirect to another host refused (a.example.com to b.example.com)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := UserMessage(tt.err)
			if !ok || got != tt.want {
				t.Fatalf("UserMessage = %q, %v; want %q, true", got, ok, tt.want)
			}
			assertNoLeak(t, got)
		})
	}

	for _, err := range []error{nil, errors.New("sitoo 404 Not Found: no such account"), errors.New("parse: bad json")} {
		if got, ok := UserMessage(err); ok {
			t.Errorf("UserMessage(%v) = %q, true; want false (not a transport error)", err, got)
		}
	}
}

// End to end against the real client: a name that resolves to loopback, and a
// closed port behind a URL that carries a key in userinfo and query.
func TestUserMessage_RealClient(t *testing.T) {
	t.Run("dial refusal hides the address, keeps it for the log", func(t *testing.T) {
		c := NewClient(5*time.Second, false)
		resp, err := c.Get("http://localhost:9/")
		if err == nil {
			_ = resp.Body.Close()
			t.Fatal("connected to loopback")
		}
		if !errors.Is(err, ErrBlockedAddress) {
			t.Fatalf("err %v does not wrap ErrBlockedAddress", err)
		}
		for _, leak := range []string{"127.0.0.1", "::1"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("err.Error() = %q leaks %q (it reaches the DLQ header)", err.Error(), leak)
			}
		}
		if d := Detail(err); !strings.Contains(d, "127.0.0.1") && !strings.Contains(d, "::1") {
			t.Errorf("Detail = %q, want the refused address for the server log", d)
		}
		got, ok := UserMessage(err)
		if !ok || got != "target address not allowed" {
			t.Errorf("UserMessage = %q, %v", got, ok)
		}
	})

	t.Run("credential url never reaches the text", func(t *testing.T) {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close() // nothing listens: connection refused
		c := NewClient(5*time.Second, true)
		resp, err := c.Get("http://apikey:" + leakSecret + "@" + addr + "/v1?api_key=" + leakSecret)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatal("connected to a closed port")
		}
		if !strings.Contains(err.Error(), leakSecret) {
			t.Skipf("net/http no longer quotes the URL in %q; nothing to hide", err)
		}
		got, ok := UserMessage(err)
		if !ok {
			t.Fatalf("UserMessage(%v) not mapped", err)
		}
		if got != "connection refused" && got != "connection failed" {
			t.Errorf("UserMessage = %q", got)
		}
		assertNoLeak(t, got)
	})

	t.Run("dns failure", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://vrsky-no-such-host.invalid/", nil)
		_, err := NewClient(5*time.Second, true).Do(req)
		if err == nil {
			t.Fatal("resolved a .invalid name")
		}
		got, ok := UserMessage(err)
		if !ok {
			t.Fatalf("UserMessage(%v) not mapped", err)
		}
		// A sandbox without DNS may time out or refuse instead of NXDOMAIN.
		switch got {
		case "host name could not be resolved", "request timed out", "connection failed", "connection refused":
		default:
			t.Errorf("UserMessage = %q (from %v)", got, err)
		}
		assertNoLeak(t, got)
	})
}
