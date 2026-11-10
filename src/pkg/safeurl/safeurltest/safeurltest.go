// Package safeurltest checks a connector's sample-data handler against the
// transport errors of #717: the tenant gets the fixed text of
// safeurl.UserMessage, and the server log gets the detail without a key.
package safeurltest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
)

// What the injected errors carry and a tenant must never read.
const (
	Resolver = "10.0.0.10:53" // the cluster resolver
	Addr     = "10.1.2.3"     // a resolved in-cluster address
	Secret   = "s3cr3t-key"   // a key in the request URL's query
)

// keyURL is the URL the injected errors quote: userinfo and a query key.
const keyURL = "https://apikey:" + Secret + "@api.example.com/v1?api_key=" + Secret

// FailingTransport fails every request with Err, as the network would.
type FailingTransport struct{ Err error }

// RoundTrip implements http.RoundTripper.
func (f FailingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_ = r.Body.Close()
	}
	return nil, f.Err
}

// Case is one transport failure and the text a tenant is shown for it.
type Case struct {
	Name string
	Err  error
	Want string
	// LogWants is what the server log must hold (the detail), if anything.
	LogWants string
}

// Cases are the failures every sample-data handler is checked against.
func Cases() []Case {
	wrap := func(err error) error { return &url.Error{Op: "Get", URL: keyURL, Err: err} }
	return []Case{
		{"dns failure naming the resolver", wrap(&net.OpError{Op: "dial", Net: "tcp",
			Err: &net.DNSError{Err: "no such host", Name: "api.example.com", Server: Resolver, IsNotFound: true}}),
			"host name could not be resolved", Resolver},
		{"connection failure naming the address", wrap(&net.OpError{Op: "dial", Net: "tcp",
			Addr: &net.TCPAddr{IP: net.ParseIP(Addr), Port: 443}, Err: io.ErrUnexpectedEOF}),
			"connection failed", Addr},
		{"refused dial", wrap(fmt.Errorf("dial: %w", safeurl.ErrBlockedAddress)), "target address not allowed", ""},
		{"timeout", wrap(context.DeadlineExceeded), "request timed out", ""},
		{"unclassified with a credential url", wrap(io.ErrUnexpectedEOF), "request failed", "api.example.com/v1"},
	}
}

// CheckSampleData posts body to the handler newHandler builds, once per Case,
// with a client whose transport fails with the case's error and a logger the
// check reads. The response must carry the case's fixed text and nothing of
// the resolver, the address or the key; the log must carry the detail and
// not the key.
func CheckSampleData(t *testing.T, body string, newHandler func(client *http.Client, logger *slog.Logger) http.HandlerFunc) {
	t.Helper()
	for _, tc := range Cases() {
		t.Run(tc.Name, func(t *testing.T) {
			var logs strings.Builder
			h := newHandler(&http.Client{Transport: FailingTransport{tc.Err}}, slog.New(slog.NewTextHandler(&logs, nil)))
			w := httptest.NewRecorder()
			h(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body)))

			out := w.Body.String()
			if want := `"error":"` + tc.Want + `"`; !strings.Contains(out, `"ok":false`) || !strings.Contains(out, want) {
				t.Errorf("body = %s, want ok=false and %s", out, want)
			}
			for _, leak := range []string{Resolver, Addr, Secret, "lookup", "dial tcp", "api_key", "api.example.com"} {
				if strings.Contains(out, leak) {
					t.Errorf("body %s leaks %q", out, leak)
				}
			}
			if tc.LogWants != "" && !strings.Contains(logs.String(), tc.LogWants) {
				t.Errorf("server log should hold %q, got %q", tc.LogWants, logs.String())
			}
			if strings.Contains(logs.String(), Secret) {
				t.Errorf("server log leaks the key: %q", logs.String())
			}
		})
	}
}

// Recorder wraps a transport and keeps the URL of every request, so a test can
// tell which request (token or API) the failure hit.
type Recorder struct {
	Next http.RoundTripper
	mu   sync.Mutex
	urls []string
}

// RoundTrip implements http.RoundTripper.
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.urls = append(r.urls, req.URL.String())
	r.mu.Unlock()
	return r.Next.RoundTrip(req)
}

// URLs returns the request URLs seen so far.
func (r *Recorder) URLs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.urls...)
}
