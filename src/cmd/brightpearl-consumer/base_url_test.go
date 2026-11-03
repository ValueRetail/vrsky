package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #667: base_url (or the datacenter and account code it is derived
// from) is tenant config and every request carries the staff token, so the
// consumer must not call a URL that is not https, that reaches a private
// address, or whose derived host is not Brightpearl's.

func TestCheckBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		cfg          BrightpearlConfig
		allowPrivate bool
		ok           bool
	}{
		{"derived default", BrightpearlConfig{Datacenter: "eu1", AccountCode: "acme"}, false, true},
		{"derived, other datacenter", BrightpearlConfig{Datacenter: "use1", AccountCode: "acme_2-x"}, false, true},
		{"override to the default host", BrightpearlConfig{BaseURL: "https://ws-eu1.brightpearl.com/public-api/acme"}, false, true},
		{"override to another public https host", BrightpearlConfig{BaseURL: "https://bp-proxy.example.com/"}, false, true},
		{"http", BrightpearlConfig{BaseURL: "http://ws-eu1.brightpearl.com/public-api/acme"}, false, false},
		{"loopback", BrightpearlConfig{BaseURL: "https://127.0.0.1/x"}, false, false},
		{"loopback v6", BrightpearlConfig{BaseURL: "https://[::1]/x"}, false, false},
		{"private 10/8", BrightpearlConfig{BaseURL: "https://10.0.0.1/x"}, false, false},
		{"private 172.16/12", BrightpearlConfig{BaseURL: "https://172.16.5.5/x"}, false, false},
		{"private 192.168/16", BrightpearlConfig{BaseURL: "https://192.168.1.1/x"}, false, false},
		{"cloud metadata", BrightpearlConfig{BaseURL: "https://169.254.169.254/latest/meta-data"}, false, false},
		{"azure wire server", BrightpearlConfig{BaseURL: "https://168.63.129.16/"}, false, false},
		{"userinfo", BrightpearlConfig{BaseURL: "https://u:p@ws-eu1.brightpearl.com/public-api/acme"}, false, false},
		{"query", BrightpearlConfig{BaseURL: "https://ws-eu1.brightpearl.com/public-api/acme?a=b"}, false, false},
		{"datacenter moves the host", BrightpearlConfig{Datacenter: "x@10.0.0.1/", AccountCode: "acme"}, false, false},
		{"datacenter adds a domain", BrightpearlConfig{Datacenter: "eu1.evil.example/", AccountCode: "acme"}, false, false},
		{"datacenter with a dot", BrightpearlConfig{Datacenter: "eu1.x", AccountCode: "acme"}, false, false},
		{"account code leaves its segment", BrightpearlConfig{Datacenter: "eu1", AccountCode: "../x"}, false, false},
		{"account code adds a query", BrightpearlConfig{Datacenter: "eu1", AccountCode: "acme?x=1"}, false, false},
		{"dev flag: http mock", BrightpearlConfig{BaseURL: "http://bp-mock:8080/public-api/acme"}, true, true},
		{"dev flag: loopback mock", BrightpearlConfig{BaseURL: "http://127.0.0.1:9000/"}, true, true},
		{"dev flag does not loosen the derived check", BrightpearlConfig{Datacenter: "x@10.0.0.1/", AccountCode: "acme"}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.checkBaseURL(tt.allowPrivate)
			if tt.ok && err != nil {
				t.Errorf("checkBaseURL(%+v) = %v, want nil", tt.cfg, err)
			}
			if !tt.ok && err == nil {
				t.Errorf("checkBaseURL(%+v) = nil, want an error", tt.cfg)
			}
		})
	}
}

// A connection whose base URL is refused is not started: no poller, and the
// fake API (on http://127.0.0.1, which the production policy refuses on two
// counts) receives nothing.
func TestStart_RefusesBadBaseURL(t *testing.T) {
	tests := []struct {
		name  string
		block func(apiURL string) string // the location fields of the brightpearl block
	}{
		{"http to the api itself", func(api string) string { return fmt.Sprintf(`"base_url":%q`, api) }},
		{"https loopback", func(api string) string {
			return fmt.Sprintf(`"base_url":%q`, strings.Replace(api, "http://", "https://", 1))
		}},
		{"metadata service", func(string) string { return `"base_url":"https://169.254.169.254/latest"` }},
		{"azure wire server", func(string) string { return `"base_url":"https://168.63.129.16/"` }},
		{"private network", func(string) string { return `"base_url":"https://10.0.0.5/public-api/acme"` }},
		{"ipv6 loopback", func(string) string { return `"base_url":"https://[::1]:8443/x"` }},
		{"datacenter aimed at the api", func(api string) string {
			return fmt.Sprintf(`"datacenter":%q,"account_code":"acme"`, "x@"+strings.TrimPrefix(api, "http://")+"/")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSRFixture(t)
			f.c.allowPrivate = false // production policy
			apiURL := between(f.nodes, `"base_url":"`, `"`)
			f.nodes = strings.Replace(f.nodes, fmt.Sprintf(`"base_url":%q`, apiURL), tt.block(apiURL), 1)
			f.expectLookup(nil)

			f.c.startConnection(context.Background(), srConn, srTenant)

			if n := f.activeCount(); n != 0 {
				t.Errorf("active pollers = %d, want 0: a refused base URL must not start", n)
			}
			time.Sleep(50 * time.Millisecond)
			if n := f.polls.Load(); n != 0 {
				t.Errorf("the API received %d requests from a refused start, want none", n)
			}
		})
	}
}

// The operator's dev switch lets a connection poll a mock on http://127.0.0.1.
func TestStart_DevFlagAllowsLoopbackMock(t *testing.T) {
	f := newSRFixture(t) // allowPrivate: true, base_url is the fake API
	f.expectLookup(nil)

	f.c.startConnection(context.Background(), srConn, srTenant)

	if n := f.activeCount(); n != 1 {
		t.Fatalf("active pollers = %d, want 1", n)
	}
	harness.Eventually(t, srWait, "the mock to be polled", func() bool { return f.polls.Load() > 0 })
}

// recordingTransport answers every request with a canned body and records
// the URLs, so the default (derived) host can be exercised offline.
type recordingTransport struct {
	mu   sync.Mutex
	urls []string
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.urls = append(rt.urls, r.URL.String())
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"response":[]}`)),
		Request:    r,
	}, nil
}

func (rt *recordingTransport) seen() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.urls...)
}

// The default URL, derived from datacenter and account code, still starts and
// polls Brightpearl under the production policy.
func TestStart_DerivedDefaultStillPolls(t *testing.T) {
	f := newSRFixture(t)
	f.c.allowPrivate = false
	rt := &recordingTransport{}
	f.c.httpClient = &http.Client{Transport: rt}
	apiURL := between(f.nodes, `"base_url":"`, `"`)
	f.nodes = strings.Replace(f.nodes, fmt.Sprintf(`"base_url":%q`, apiURL), `"datacenter":"eu1","account_code":"acme"`, 1)
	f.expectLookup(nil)

	f.c.startConnection(context.Background(), srConn, srTenant)

	if n := f.activeCount(); n != 1 {
		t.Fatalf("active pollers = %d, want 1", n)
	}
	harness.Eventually(t, srWait, "the default host to be polled", func() bool { return len(rt.seen()) > 0 })
	if got, want := rt.seen()[0], "https://ws-eu1.brightpearl.com/public-api/acme/order-service/order-search"; got != want {
		t.Errorf("polled %s, want %s", got, want)
	}
}

func between(s, from, to string) string {
	_, rest, _ := strings.Cut(s, from)
	v, _, _ := strings.Cut(rest, to)
	return v
}

// The sample-data endpoint fetches with the tenant's base URL too.
func TestSampleData_RefusesBadBaseURL(t *testing.T) {
	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer api.Close()

	tests := []struct {
		name     string
		location string
		wantErr  string
	}{
		{"http to the api", fmt.Sprintf(`"base_url":%q`, api.URL), "https"},
		{"metadata service", `"base_url":"https://169.254.169.254/latest"`, "private"},
		{"private network", `"base_url":"https://10.0.0.5/x"`, "private"},
		{"datacenter moves the host", fmt.Sprintf(`"datacenter":%q,"account_code":"acme"`, "x@"+strings.TrimPrefix(api.URL, "http://")+"/"), "datacenter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &brightpearlConsumer{httpClient: http.DefaultClient} // allowPrivate false
			body := `{"app_ref":"app","staff_token":"tok",` + tt.location + `,"resource":"/order-service/order"}`
			w := httptest.NewRecorder()
			c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body)))

			if !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), tt.wantErr) {
				t.Errorf("want ok=false naming %q, got %s", tt.wantErr, w.Body.String())
			}
			if hits.Load() != 0 {
				t.Errorf("the API received %d requests, want none", hits.Load())
			}
		})
	}
}

// A hostname passes the config check, so the guard that matters is the dial.
// "localhost" resolves to loopback: the production client must not connect,
// and the caller is not told which address it was.
func TestSampleData_HostnameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
	port, accepted := tcpCounter(t)
	baseURL := "https://localhost:" + port + "/public-api/acme"
	if err := (&BrightpearlConfig{BaseURL: baseURL}).checkBaseURL(false); err != nil {
		t.Fatalf("a hostname is not refused at config time (only the dial can tell), got %v", err)
	}

	c := &brightpearlConsumer{httpClient: safeurl.NewClient(5*time.Second, false)} // what Configure builds
	body := fmt.Sprintf(`{"app_ref":"app","staff_token":"tok","base_url":%q,"resource":"/order-service/order"}`, baseURL)
	w := httptest.NewRecorder()
	c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body)))

	if !strings.Contains(w.Body.String(), "target address not allowed") {
		t.Errorf("want the dial refusal, got %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "127.0.0.1") || strings.Contains(w.Body.String(), "::1") {
		t.Errorf("the response names the resolved address: %s", w.Body.String())
	}
	if accepted.Load() != 0 {
		t.Errorf("the client opened %d connections to loopback, want none", accepted.Load())
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
