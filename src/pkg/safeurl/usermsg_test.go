package safeurl

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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

// A refused redirect's text reaches the tenant as it is (UserMessage). The
// Location may carry a key in the query or fragment, and a relative one keeps
// the request's userinfo: every refusal text shows scheme, host and path only.
func TestSameHostRedirect_TextsHideQueryAndCredentials(t *testing.T) {
	q := "?api_key=" + leakSecret + "#token=" + leakSecret
	five := []string{"https://a.example.com/1", "https://a.example.com/2", "https://a.example.com/3", "https://a.example.com/4", "https://a.example.com/5"}
	tests := []struct {
		name         string
		via          []string
		next         string
		allowPrivate bool
		want         string
	}{
		{"https to http", []string{"https://api.example.com/x"}, "http://api.example.com/y" + q, false,
			"redirect from https to http refused (http://api.example.com/y)"},
		{"https to http, userinfo", []string{"https://api.example.com/x"}, "http://apikey:" + leakSecret + "@api.example.com/y" + q, false,
			"redirect from https to http refused (http://xxxxx@api.example.com/y)"},
		{"https to http, password with ? in it", []string{"https://api.example.com/x"}, "http://apikey:1234?" + leakSecret + "@api.example.com", false,
			"redirect from https to http refused ([invalid URL])"},
		{"private literal", []string{"https://api.example.com/x"}, "https://10.0.0.5/y" + q, false,
			"redirect refused: url https://10.0.0.5/y points at a private, loopback or link-local address"},
		{"metadata, userinfo", []string{"https://api.example.com/x"}, "http://apikey:" + leakSecret + "@169.254.169.254/latest" + q, false,
			"redirect refused: url http://xxxxx@169.254.169.254/latest points at a private, loopback or link-local address"},
		{"not http", []string{"https://api.example.com/x"}, "ftp://api.example.com/y" + q, false,
			"redirect refused: url ftp://api.example.com/y must use http or https"},
		{"another host", []string{"https://api.example.com/x"}, "https://other.example.net/y" + q, false,
			"redirect to another host refused (api.example.com to other.example.net)"},
		{"another host, userinfo", []string{"https://apikey:" + leakSecret + "@api.example.com/x"}, "https://apikey:" + leakSecret + "@other.example.net/y" + q, false,
			"redirect to another host refused (api.example.com to other.example.net)"},
		{"too many", append(append([]string{}, five...), "https://a.example.com/6"), "https://a.example.com/7" + q, false,
			"stopped after 5 redirects"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var via []*http.Request
			for _, v := range tt.via {
				via = append(via, newReq(t, v))
			}
			err := sameHostRedirect(tt.allowPrivate)(newReq(t, tt.next), via)
			if err == nil {
				t.Fatalf("redirect to %s followed", tt.next)
			}
			got, ok := UserMessage(&url.Error{Op: "Get", URL: tt.via[0], Err: err})
			if !ok || got != tt.want {
				t.Errorf("UserMessage = %q, %v; want %q", got, ok, tt.want)
			}
			if strings.Contains(got, leakSecret) || strings.Contains(err.Error(), leakSecret) {
				t.Errorf("redirect text %q leaks the key", got)
			}
		})
	}
}

// The reported case end to end: a TLS server redirects to http with a key in
// the query. The tenant text names the target without it.
func TestUserMessage_RefusedRedirectWithKey(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/x?api_key="+leakSecret, http.StatusFound)
	}))
	defer srv.Close()
	c := NewClientWith(5*time.Second, Options{AllowPrivate: true, SameHostRedirects: true, TLSConfig: srv.Client().Transport.(*http.Transport).TLSClientConfig})
	resp, err := c.Get(srv.URL + "/start")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("https to http redirect followed")
	}
	got, ok := UserMessage(err)
	if !ok || !strings.HasPrefix(got, "redirect from https to http refused (http://") || !strings.HasSuffix(got, "/x)") {
		t.Errorf("UserMessage = %q, %v; want the downgrade refusal naming http://host/x", got, ok)
	}
	if strings.Contains(got, leakSecret) || strings.Contains(got, "api_key") {
		t.Errorf("UserMessage = %q leaks the query", got)
	}
}

// Detail is for the server log: it keeps the resolver and the resolved
// address, but no URL in it carries a query, fragment or userinfo.
func TestDetail_RedactsURLs(t *testing.T) {
	keyURL := "https://apikey:" + leakSecret + "@api.example.com/v1?api_key=" + leakSecret + "#t=" + leakSecret
	dns := &net.DNSError{Err: "no such host", Name: "api.example.com", Server: leakResolver, IsNotFound: true}
	tokenErr := &url.Error{Op: "Post", URL: "https://login.example.com/token?client_secret=" + leakSecret, Err: dns}
	tests := []struct {
		name  string
		err   error
		wants []string
	}{
		{"request url", &url.Error{Op: "Get", URL: keyURL, Err: dns},
			[]string{`Get "https://xxxxx@api.example.com/v1"`, leakResolver}},
		{"wrapped token request inside a request", fmt.Errorf("visma get: %w", &url.Error{Op: "Get", URL: keyURL,
			Err: fmt.Errorf("token request: %w", tokenErr)}),
			[]string{"visma get: ", `Post "https://login.example.com/token"`, `Get "https://xxxxx@api.example.com/v1"`, leakResolver}},
		{"one url extends another", fmt.Errorf("%w; %w",
			&url.Error{Op: "Get", URL: "https://h.example.com/x?k=1", Err: errors.New("a")},
			&url.Error{Op: "Get", URL: "https://h.example.com/x?k=1&s=" + leakSecret, Err: errors.New("b")}),
			[]string{`Get "https://h.example.com/x": a`, `Get "https://h.example.com/x": b`}},
		{"formatted with %s too", fmt.Errorf("calling %s: %w", keyURL, &url.Error{Op: "Get", URL: keyURL, Err: dns}),
			[]string{"calling https://xxxxx@api.example.com/v1:"}},
		{"refused dial keeps the address", &url.Error{Op: "Get", URL: keyURL, Err: &blockedError{addr: leakIP}},
			[]string{"(address " + leakIP + ")", "https://xxxxx@api.example.com/v1"}},
		{"no url error", errors.New("plain"), []string{"plain"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Detail(tt.err)
			for _, w := range tt.wants {
				if !strings.Contains(got, w) {
					t.Errorf("Detail = %q, want it to contain %q", got, w)
				}
			}
			for _, leak := range []string{leakSecret, "api_key", "client_secret", "apikey"} {
				if strings.Contains(got, leak) {
					t.Errorf("Detail = %q leaks %q", got, leak)
				}
			}
		})
	}
	if Detail(nil) != "" {
		t.Error("Detail(nil) not empty")
	}
}
