package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
)

// Tests for #616: base_url is tenant config and every request carries the
// Sitoo Basic-auth credential, so the consumer must not call a URL that is not
// https or that reaches a private address, and a webhook event's id must not be
// able to change the path or query of the request it triggers.

func TestCheckBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		baseURL      string
		allowPrivate bool
		ok           bool
	}{
		{"unset means the default", "", false, true},
		{"the default host", "https://api.mysitoo.com/v2", false, true},
		{"another public https host", "https://sitoo.example.com/v2", false, true},
		{"http", "http://api.mysitoo.com/v2", false, false},
		{"loopback", "https://127.0.0.1/v2", false, false},
		{"loopback v6", "https://[::1]/v2", false, false},
		{"private 10/8", "https://10.0.0.1/v2", false, false},
		{"private 172.16/12", "https://172.16.5.5/v2", false, false},
		{"private 192.168/16", "https://192.168.1.1/v2", false, false},
		{"cloud metadata", "https://169.254.169.254/latest/meta-data", false, false},
		{"azure wire server", "https://168.63.129.16/", false, false},
		{"userinfo", "https://user:pw@api.mysitoo.com/v2", false, false},
		{"query", "https://api.mysitoo.com/v2?a=b", false, false},
		{"dev flag: http mock", "http://sitoo-mock:8080/v2", true, true},
		{"dev flag: loopback mock", "http://127.0.0.1:9000/v2", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&SitooConfig{BaseURL: tt.baseURL}).checkBaseURL(tt.allowPrivate)
			if tt.ok && err != nil {
				t.Errorf("checkBaseURL(%q) = %v, want nil", tt.baseURL, err)
			}
			if !tt.ok && err == nil {
				t.Errorf("checkBaseURL(%q) = nil, want an error", tt.baseURL)
			}
		})
	}
}

// A connection whose base_url is refused is not started: no poller, and the
// fake API (here on http://127.0.0.1, which the production policy refuses on
// two counts) receives nothing.
func TestStart_RefusesBadBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL func(apiURL string) string
	}{
		{"http to the api itself", func(api string) string { return api }},
		{"https loopback", func(api string) string { return strings.Replace(api, "http://", "https://", 1) }},
		{"metadata service", func(string) string { return "https://169.254.169.254/latest" }},
		{"private network", func(string) string { return "https://10.0.0.5/v2" }},
		{"ipv6 loopback", func(string) string { return "https://[::1]:8443/v2" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSRFixture(t)
			f.c.allowPrivate = false // production policy
			apiURL := between(f.nodes, `"base_url":"`, `"`)
			f.nodes = strings.Replace(f.nodes, apiURL, tt.baseURL(apiURL), 1)
			f.expectLookup(nil)

			f.c.startConnection(context.Background(), srConn, srTenant)

			if n := f.activeCount(); n != 0 {
				t.Errorf("active pollers = %d, want 0: a refused base_url must not start", n)
			}
			time.Sleep(50 * time.Millisecond)
			if n := f.polls.Load(); n != 0 {
				t.Errorf("the API received %d requests from a refused start, want none", n)
			}
		})
	}
}

func between(s, from, to string) string {
	_, rest, _ := strings.Cut(s, from)
	v, _, _ := strings.Cut(rest, to)
	return v
}

// The sample-data endpoint fetches with the tenant's base_url too.
func TestSampleData_RefusesBadBaseURL(t *testing.T) {
	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer api.Close()

	c := &sitooConsumer{httpClient: http.DefaultClient} // allowPrivate false
	body := fmt.Sprintf(`{"api_id":"id","api_password":"pw","account_id":1,"site_id":2,"base_url":%q,"resource":"transactions"}`, api.URL)
	w := httptest.NewRecorder()
	c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body)))

	if !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), "https") {
		t.Errorf("want ok=false naming https, got %s", w.Body.String())
	}
	if hits.Load() != 0 {
		t.Errorf("the API received %d requests, want none", hits.Load())
	}
}

// The webhook path loads the tenant's config and builds a URL from it, so it
// refuses a bad base_url as start does. 202: redelivery cannot fix the config.
func TestWebhook_RefusesBadBaseURL(t *testing.T) {
	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer api.Close()

	c, got, mu := newTestConsumer()
	c.allowPrivate = false
	c.loadConfig = func(context.Context, string, string) (*SitooConfig, error) {
		return &SitooConfig{AccountID: 1, SiteID: 2, APIID: "x", APIPassword: "y", BaseURL: api.URL, Resource: "transactions"}, nil
	}
	c.resolveTenant = func(string) (string, error) { return "tenant-9", nil }

	rec := httptest.NewRecorder()
	c.handleWebhook()(rec, httptest.NewRequest(http.MethodPost, "/sitoo/events/conn-9",
		strings.NewReader(`{"eventtype":"transaction.created","transactionid":90210}`)))

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rec.Code)
	}
	if hits.Load() != 0 {
		t.Errorf("the API received %d requests, want none", hits.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 0 {
		t.Errorf("published %d envelopes, want 0", len(*got))
	}
}

// A hostname passes the config check, so the guard that matters is the dial.
// "localhost" resolves to loopback: the production client must not connect.
func TestGet_HostnameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
	port, accepted := tcpCounter(t)
	baseURL := "https://localhost:" + port + "/v2"

	cfg := &SitooConfig{AccountID: 1, SiteID: 2, APIID: "x", APIPassword: "y", BaseURL: baseURL, Resource: "transactions"}
	if err := cfg.checkBaseURL(false); err != nil {
		t.Fatalf("a hostname is not refused at config time (only the dial can tell), got %v", err)
	}

	c, _, _ := newTestConsumer()
	c.allowPrivate = false
	c.httpClient = safeurl.NewClient(5*time.Second, false) // what Configure builds
	if _, err := c.get(context.Background(), cfg, cfg.collectionURL(0, 10, nil)); err == nil {
		t.Fatal("get connected to a hostname that resolves to loopback")
	}
	if accepted.Load() != 0 {
		t.Errorf("the client opened %d connections to loopback, want none", accepted.Load())
	}
}

// The webhook id goes into the URL path escaped: it cannot add segments, a
// query or a fragment.
func TestResourceURL_EscapesTheEventID(t *testing.T) {
	cfg := &SitooConfig{AccountID: 1, SiteID: 2, BaseURL: "https://api.mysitoo.com/v2", Resource: "transactions"}
	const prefix = "https://api.mysitoo.com/v2/accounts/1/sites/2/transactions/"
	tests := []struct {
		id   string
		want string // the escaped last segment
	}{
		{"90210", "90210"},
		{"a/b", "a%2Fb"},
		{"../../x", "..%2F..%2Fx"},
		{"1?admin=true", "1%3Fadmin=true"},
		{"1#frag", "1%23frag"},
		{"a b", "a%20b"},
		{"%2e%2e", "%252e%252e"},
		{"x/../../../etc?y#z", "x%2F..%2F..%2F..%2Fetc%3Fy%23z"},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got := cfg.resourceURL(tt.id)
			if got != prefix+tt.want {
				t.Errorf("resourceURL(%q) = %q, want %q", tt.id, got, prefix+tt.want)
			}
			u, err := url.Parse(got)
			if err != nil {
				t.Fatal(err)
			}
			if u.RawQuery != "" || u.Fragment != "" {
				t.Errorf("resourceURL(%q) carries query %q / fragment %q", tt.id, u.RawQuery, u.Fragment)
			}
			if rest := strings.TrimPrefix(u.EscapedPath(), "/v2/accounts/1/sites/2/transactions/"); strings.Contains(rest, "/") {
				t.Errorf("resourceURL(%q) added path segments: %q", tt.id, rest)
			}
		})
	}
}

// End to end through the webhook: what the fake Sitoo receives is one request
// for the one escaped path, with no query.
func TestWebhook_HostileEventIDStaysInItsSegment(t *testing.T) {
	var uris []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uris = append(uris, r.RequestURI)
		_, _ = w.Write([]byte(`{"transactionid":1}`))
	}))
	defer api.Close()

	tests := []struct {
		id      string
		wantURI string // "" = the event is acked and nothing is fetched
	}{
		{"a/b", "/accounts/1/sites/2/transactions/a%2Fb"},
		{"x?admin=true", "/accounts/1/sites/2/transactions/x%3Fadmin=true"},
		{"x#frag", "/accounts/1/sites/2/transactions/x%23frag"},
		{"../../../x", "/accounts/1/sites/2/transactions/..%2F..%2F..%2Fx"},
		{"..", ""},
		{".", ""},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			uris = nil
			c, _, _ := newTestConsumer()
			c.resolveTenant = func(string) (string, error) { return "tenant-9", nil }
			c.loadConfig = func(context.Context, string, string) (*SitooConfig, error) {
				return &SitooConfig{AccountID: 1, SiteID: 2, APIID: "x", APIPassword: "y", BaseURL: api.URL, Resource: "transactions"}, nil
			}
			rec := httptest.NewRecorder()
			c.handleWebhook()(rec, httptest.NewRequest(http.MethodPost, "/sitoo/events/conn-9",
				strings.NewReader(fmt.Sprintf(`{"eventtype":"transaction.created","transactionid":%q}`, tt.id))))

			if tt.wantURI == "" {
				if len(uris) != 0 {
					t.Errorf("fetched %v for id %q, want no request", uris, tt.id)
				}
				if rec.Code != http.StatusAccepted {
					t.Errorf("status = %d, want 202", rec.Code)
				}
				return
			}
			if len(uris) != 1 || uris[0] != tt.wantURI {
				t.Errorf("Sitoo received %v, want exactly [%s]", uris, tt.wantURI)
			}
		})
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
