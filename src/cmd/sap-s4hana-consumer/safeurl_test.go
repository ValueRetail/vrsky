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

// Tests for #678: host, api_base_url and token_url are tenant config and every
// request carries the tenant's SAP credential, so the consumer must not call a
// URL that is not https, that reaches a private address, or whose host was
// moved by what the tenant typed into host.

const defaultHost = "my347623.s4hana.ondemand.com"

func TestCheckURLs(t *testing.T) {
	basic := func(c SAPConfig) SAPConfig {
		c.EntitySet, c.Username, c.Password = "A_SalesOrder", "u", "p"
		if c.APIBaseURL == "" && c.Service == "" {
			c.Service = "API_SALES_ORDER_SRV"
		}
		return c
	}
	oauth := func(c SAPConfig) SAPConfig {
		c = basic(c)
		c.AuthType, c.ClientID, c.ClientSecret = authOAuth2, "id", "secret"
		return c
	}
	tests := []struct {
		name         string
		cfg          SAPConfig
		allowPrivate bool
		wantErr      string // substring; empty = allowed
	}{
		{"default cloud host", basic(SAPConfig{Host: defaultHost}), false, ""},
		{"host with a port", basic(SAPConfig{Host: "sap.example.com:44300"}), false, ""},
		{"oauth2, token url from the host", oauth(SAPConfig{Host: defaultHost}), false, ""},
		{"oauth2, public token url", oauth(SAPConfig{Host: defaultHost, TokenURL: "https://acme.authentication.eu10.hana.ondemand.com/oauth/token"}), false, ""},
		{"public api_base_url", basic(SAPConfig{APIBaseURL: "https://sap.example.com/sap/opu/odata/sap/API_SALES_ORDER_SRV/"}), false, ""},
		{"api_base_url, host unused and empty", basic(SAPConfig{APIBaseURL: "https://sap.example.com/odata"}), false, ""},

		{"api_base_url over http", basic(SAPConfig{APIBaseURL: "http://sap.example.com/odata"}), false, "https"},
		{"api_base_url loopback", basic(SAPConfig{APIBaseURL: "https://127.0.0.1/odata"}), false, "private"},
		{"api_base_url loopback v6", basic(SAPConfig{APIBaseURL: "https://[::1]/odata"}), false, "private"},
		{"api_base_url private 10/8", basic(SAPConfig{APIBaseURL: "https://10.0.0.5/odata"}), false, "private"},
		{"api_base_url private 192.168/16", basic(SAPConfig{APIBaseURL: "https://192.168.1.1/odata"}), false, "private"},
		{"api_base_url metadata", basic(SAPConfig{APIBaseURL: "https://169.254.169.254/latest/meta-data"}), false, "private"},
		{"api_base_url azure wire server", basic(SAPConfig{APIBaseURL: "https://168.63.129.16/"}), false, "private"},
		{"api_base_url userinfo", basic(SAPConfig{APIBaseURL: "https://u:p@sap.example.com/odata"}), false, "user name"},
		{"api_base_url query", basic(SAPConfig{APIBaseURL: "https://sap.example.com/odata?x=1"}), false, "query"},

		{"empty host", basic(SAPConfig{}), false, "host"},
		{"host with userinfo moves the host", basic(SAPConfig{Host: "x@10.0.0.1/"}), false, "host"},
		{"host with a path", basic(SAPConfig{Host: "evil.example/"}), false, "host"},
		{"host with a scheme", basic(SAPConfig{Host: "https://" + defaultHost}), false, "host"},
		{"host with a query", basic(SAPConfig{Host: "evil.example?"}), false, "host"},
		{"host with a fragment", basic(SAPConfig{Host: "evil.example#"}), false, "host"},
		{"host with whitespace", basic(SAPConfig{Host: " " + defaultHost}), false, "host"},
		{"host port out of range", basic(SAPConfig{Host: defaultHost + ":99999"}), false, "host"},
		{"host port with a sign", basic(SAPConfig{Host: defaultHost + ":+443"}), false, "host"},
		{"host with two ports", basic(SAPConfig{Host: defaultHost + ":443:80"}), false, "host"},
		{"host port then userinfo", basic(SAPConfig{Host: "evil.example:443@" + defaultHost}), false, "host"},
		{"host ipv6 literal", basic(SAPConfig{Host: "[::1]"}), false, "host"},
		{"host loopback literal", basic(SAPConfig{Host: "127.0.0.1:8080"}), false, "private"},
		{"host metadata literal", basic(SAPConfig{Host: "169.254.169.254"}), false, "private"},

		{"token_url over http", oauth(SAPConfig{Host: defaultHost, TokenURL: "http://auth.example.com/token"}), false, "token_url"},
		{"token_url loopback", oauth(SAPConfig{Host: defaultHost, TokenURL: "https://127.0.0.1/token"}), false, "token_url"},
		{"token_url metadata", oauth(SAPConfig{Host: defaultHost, TokenURL: "https://169.254.169.254/token"}), false, "token_url"},
		{"token_url userinfo", oauth(SAPConfig{Host: defaultHost, TokenURL: "https://u:p@auth.example.com/token"}), false, "token_url"},
		{"oauth2 default token url from a bad host", oauth(SAPConfig{APIBaseURL: "https://sap.example.com/odata", Host: "x@10.0.0.1/"}), false, "token_url"},
		{"oauth2 default token url with no host", oauth(SAPConfig{APIBaseURL: "https://sap.example.com/odata"}), false, "token_url"},

		{"dev flag: http mock-sap", basic(SAPConfig{APIBaseURL: "http://mock-sap:8099/sap/opu/odata/sap/API_SALES_ORDER_SRV"}), true, ""},
		{"dev flag: loopback mock", basic(SAPConfig{APIBaseURL: "http://127.0.0.1:9000/odata"}), true, ""},
		{"dev flag: loopback token url", oauth(SAPConfig{APIBaseURL: "http://127.0.0.1:9000/odata", TokenURL: "http://127.0.0.1:9000/token"}), true, ""},
		{"dev flag does not loosen the host shape", basic(SAPConfig{Host: "x@10.0.0.1/"}), true, "host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.checkURLs(tt.allowPrivate)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("checkURLs(%+v) = %v, want nil", tt.cfg, err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("checkURLs(%+v) = nil, want an error containing %q", tt.cfg, tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("checkURLs(%+v) = %v, want an error containing %q", tt.cfg, err, tt.wantErr)
			}
		})
	}
}

// A connection whose address is refused is not started: no poller, and the
// fake API (on http://127.0.0.1, which the production policy refuses on two
// counts) receives nothing.
func TestStart_RefusesBadAddress(t *testing.T) {
	tests := []struct {
		name     string
		location func(apiURL string) string // replaces "api_base_url":"<fake>"
	}{
		{"http to the api itself", func(api string) string { return fmt.Sprintf(`"api_base_url":%q`, api) }},
		{"https loopback", func(api string) string {
			return fmt.Sprintf(`"api_base_url":%q`, strings.Replace(api, "http://", "https://", 1))
		}},
		{"metadata service", func(string) string { return `"api_base_url":"https://169.254.169.254/latest"` }},
		{"azure wire server", func(string) string { return `"api_base_url":"https://168.63.129.16/"` }},
		{"private network", func(string) string { return `"api_base_url":"https://10.0.0.5/odata"` }},
		{"ipv6 loopback", func(string) string { return `"api_base_url":"https://[::1]:8443/odata"` }},
		{"host aimed at the api", func(api string) string {
			return fmt.Sprintf(`"host":%q,"service":"S"`, "x@"+strings.TrimPrefix(api, "http://")+"/")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSRFixture(t)
			f.c.allowPrivate = false // production policy
			apiURL := between(f.nodes, `"api_base_url":"`, `"`)
			f.nodes = strings.Replace(f.nodes, fmt.Sprintf(`"api_base_url":%q`, apiURL), tt.location(apiURL), 1)
			f.expectLookup(nil)

			f.c.startConnection(context.Background(), srConn, srTenant)

			if n := f.activeCount(); n != 0 {
				t.Errorf("active pollers = %d, want 0: a refused address must not start", n)
			}
			time.Sleep(50 * time.Millisecond)
			if n := f.polls.Load(); n != 0 {
				t.Errorf("the API received %d requests from a refused start, want none", n)
			}
		})
	}
}

// host is checked for its shape even with the dev flag on, which lifts the
// address check. The fake API is an https server the client trusts and the
// host carries a path ("127.0.0.1:port/admin"), which makes a URL the dev
// policy would otherwise accept, so the host check is the only thing that
// keeps the request (and the credential) off it.
func TestStart_HostInjectionRefusedEvenWithDevFlag(t *testing.T) {
	var hits atomic.Int32
	tlsAPI := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"d":{"results":[]}}`))
	}))
	defer tlsAPI.Close()

	f := newSRFixture(t) // allowPrivate: true
	f.c.httpClient = tlsAPI.Client()
	apiURL := between(f.nodes, `"api_base_url":"`, `"`)
	host := strings.TrimPrefix(tlsAPI.URL, "https://") + "/admin"
	f.nodes = strings.Replace(f.nodes, fmt.Sprintf(`"api_base_url":%q`, apiURL), fmt.Sprintf(`"host":%q,"service":"S"`, host), 1)
	f.expectLookup(nil)

	f.c.startConnection(context.Background(), srConn, srTenant)

	if n := f.activeCount(); n != 0 {
		t.Errorf("active pollers = %d, want 0", n)
	}
	time.Sleep(50 * time.Millisecond)
	if n := hits.Load(); n != 0 {
		t.Errorf("the host-injected target received %d requests, want none", n)
	}
}

// OAuth2: the token call carries the client secret, so a refused token_url
// stops the start before the token is fetched.
func TestStart_RefusesBadTokenURL(t *testing.T) {
	var hits atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
	}))
	defer tokenSrv.Close()

	f := newSRFixture(t)
	f.c.allowPrivate = false
	f.c.httpClient = tokenSrv.Client()
	apiURL := between(f.nodes, `"api_base_url":"`, `"`)
	f.nodes = strings.Replace(f.nodes, fmt.Sprintf(`"api_base_url":%q`, apiURL),
		fmt.Sprintf(`"api_base_url":"https://%s/odata","auth_type":"oauth2","client_id":"id","client_secret":"s","token_url":%q`, defaultHost, tokenSrv.URL), 1)
	f.expectLookup(nil)

	f.c.startConnection(context.Background(), srConn, srTenant)

	if n := f.activeCount(); n != 0 {
		t.Errorf("active pollers = %d, want 0", n)
	}
	time.Sleep(50 * time.Millisecond)
	if n := hits.Load(); n != 0 {
		t.Errorf("the token endpoint received %d requests, want none", n)
	}
}

// The operator's dev switch lets a connection poll a mock on http://127.0.0.1,
// as docker compose does with mock-sap.
func TestStart_DevFlagAllowsLoopbackMock(t *testing.T) {
	f := newSRFixture(t) // allowPrivate: true, api_base_url is the fake API
	f.expectLookup(nil)

	f.c.startConnection(context.Background(), srConn, srTenant)

	if n := f.activeCount(); n != 1 {
		t.Fatalf("active pollers = %d, want 1", n)
	}
	harness.Eventually(t, srWait, "the mock to be polled", func() bool { return f.polls.Load() > 0 })
}

// recordingTransport answers every request with an empty page and records the
// URLs, so the default cloud host can be exercised offline.
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
		Body:       io.NopCloser(strings.NewReader(`{"d":{"results":[]}}`)),
		Request:    r,
	}, nil
}

func (rt *recordingTransport) seen() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.urls...)
}

// The default URL, built from host and service, still starts and polls SAP
// under the production policy.
func TestStart_DefaultHostStillPolls(t *testing.T) {
	f := newSRFixture(t)
	f.c.allowPrivate = false
	rt := &recordingTransport{}
	f.c.httpClient = &http.Client{Transport: rt}
	apiURL := between(f.nodes, `"api_base_url":"`, `"`)
	f.nodes = strings.Replace(f.nodes, fmt.Sprintf(`"api_base_url":%q`, apiURL), `"host":"`+defaultHost+`","service":"API_SALES_ORDER_SRV"`, 1)
	f.expectLookup(nil)

	f.c.startConnection(context.Background(), srConn, srTenant)

	if n := f.activeCount(); n != 1 {
		t.Fatalf("active pollers = %d, want 1", n)
	}
	harness.Eventually(t, srWait, "the default host to be polled", func() bool { return len(rt.seen()) > 0 })
	if got, want := rt.seen()[0], "https://"+defaultHost+"/sap/opu/odata/sap/API_SALES_ORDER_SRV/A_SalesOrder"; got != want {
		t.Errorf("polled %s, want %s", got, want)
	}
}

func between(s, from, to string) string {
	_, rest, _ := strings.Cut(s, from)
	v, _, _ := strings.Cut(rest, to)
	return v
}

// The sample-data endpoint fetches with the tenant's config too.
func TestSampleData_RefusesBadAddress(t *testing.T) {
	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600,"d":{"results":[]}}`))
	}))
	defer api.Close()

	tests := []struct {
		name     string
		location string
		wantErr  string
	}{
		{"http to the api", fmt.Sprintf(`"api_base_url":%q`, api.URL), "https"},
		{"metadata service", `"api_base_url":"https://169.254.169.254/latest"`, "private"},
		{"private network", `"api_base_url":"https://10.0.0.5/x"`, "private"},
		{"host aimed at the api", fmt.Sprintf(`"host":%q,"service":"S"`, "x@"+strings.TrimPrefix(api.URL, "http://")+"/"), "host"},
		{"token url aimed at the api", fmt.Sprintf(`"api_base_url":"https://%s/odata","auth_type":"oauth2","client_id":"id","client_secret":"s","token_url":%q`, defaultHost, api.URL), "token_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits.Store(0)
			c := &sapConsumer{httpClient: &http.Client{Timeout: 2 * time.Second}} // allowPrivate false
			body := `{"username":"u","password":"p",` + tt.location + `,"entity_set":"A_SalesOrder"}`
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
// and the caller is not told which address it was. Both ways of naming the
// host are covered.
func TestSampleData_HostnameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
	port, accepted := tcpCounter(t)
	tests := []struct {
		name     string
		location string
	}{
		{"api_base_url", fmt.Sprintf(`"api_base_url":"https://localhost:%s/odata"`, port)},
		{"host", fmt.Sprintf(`"host":"localhost:%s","service":"S"`, port)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &sapConsumer{httpClient: safeurl.NewClient(5*time.Second, false)} // what Configure builds
			body := `{"username":"u","password":"p",` + tt.location + `,"entity_set":"A_SalesOrder"}`
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
