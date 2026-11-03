package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Tests for #678: host, api_base_url and token_url are tenant config and every
// request carries the tenant's SAP credential, so the producer must not call a
// URL that is not https, that reaches a private address, or whose host was
// moved by what the tenant typed into host. A refusal is a plain error: it is
// retried and then dead-lettered with its payload, never acked and dropped.

const (
	defaultHost = "my347623.s4hana.ondemand.com"
	sfConn      = "conn-sap-out"
	sfTenant    = "tenant-out"
)

func TestCheckURLs(t *testing.T) {
	basic := func(c SAPProducerConfig) SAPProducerConfig {
		c.EntitySet, c.Username, c.Password = "A_SalesOrder", "u", "p"
		if c.APIBaseURL == "" && c.Service == "" {
			c.Service = "API_SALES_ORDER_SRV"
		}
		return c
	}
	oauth := func(c SAPProducerConfig) SAPProducerConfig {
		c = basic(c)
		c.AuthType, c.ClientID, c.ClientSecret = authOAuth2, "id", "secret"
		return c
	}
	tests := []struct {
		name         string
		cfg          SAPProducerConfig
		allowPrivate bool
		wantErr      string // substring; empty = allowed
	}{
		{"default cloud host", basic(SAPProducerConfig{Host: defaultHost}), false, ""},
		{"host with a port", basic(SAPProducerConfig{Host: "sap.example.com:44300"}), false, ""},
		{"oauth2, token url from the host", oauth(SAPProducerConfig{Host: defaultHost}), false, ""},
		{"oauth2, public token url", oauth(SAPProducerConfig{Host: defaultHost, TokenURL: "https://acme.authentication.eu10.hana.ondemand.com/oauth/token"}), false, ""},
		{"public api_base_url", basic(SAPProducerConfig{APIBaseURL: "https://sap.example.com/sap/opu/odata/sap/API_SALES_ORDER_SRV"}), false, ""},

		{"api_base_url over http", basic(SAPProducerConfig{APIBaseURL: "http://sap.example.com/odata"}), false, "https"},
		{"api_base_url loopback", basic(SAPProducerConfig{APIBaseURL: "https://127.0.0.1/odata"}), false, "private"},
		{"api_base_url loopback v6", basic(SAPProducerConfig{APIBaseURL: "https://[::1]/odata"}), false, "private"},
		{"api_base_url private 172.16/12", basic(SAPProducerConfig{APIBaseURL: "https://172.16.5.5/odata"}), false, "private"},
		{"api_base_url metadata", basic(SAPProducerConfig{APIBaseURL: "https://169.254.169.254/latest"}), false, "private"},
		{"api_base_url azure wire server", basic(SAPProducerConfig{APIBaseURL: "https://168.63.129.16/"}), false, "private"},
		{"api_base_url userinfo", basic(SAPProducerConfig{APIBaseURL: "https://u:p@sap.example.com/odata"}), false, "user name"},

		{"empty host", basic(SAPProducerConfig{}), false, "host"},
		{"host with userinfo moves the host", basic(SAPProducerConfig{Host: "x@10.0.0.1/"}), false, "host"},
		{"host with a path", basic(SAPProducerConfig{Host: "evil.example/"}), false, "host"},
		{"host with a scheme", basic(SAPProducerConfig{Host: "https://" + defaultHost}), false, "host"},
		{"host with a query", basic(SAPProducerConfig{Host: "evil.example?"}), false, "host"},
		{"host port out of range", basic(SAPProducerConfig{Host: defaultHost + ":0"}), false, "host"},
		{"host port then userinfo", basic(SAPProducerConfig{Host: "evil.example:443@" + defaultHost}), false, "host"},
		{"host loopback literal", basic(SAPProducerConfig{Host: "127.0.0.1"}), false, "private"},

		{"token_url over http", oauth(SAPProducerConfig{Host: defaultHost, TokenURL: "http://auth.example.com/token"}), false, "token_url"},
		{"token_url private", oauth(SAPProducerConfig{Host: defaultHost, TokenURL: "https://10.1.2.3/token"}), false, "token_url"},
		{"oauth2 default token url from a bad host", oauth(SAPProducerConfig{APIBaseURL: "https://sap.example.com/odata", Host: "evil.example/"}), false, "token_url"},

		{"dev flag: http mock-sap", basic(SAPProducerConfig{APIBaseURL: "http://mock-sap:8099/sap/opu/odata/sap/API_SALES_ORDER_SRV"}), true, ""},
		{"dev flag: loopback token url", oauth(SAPProducerConfig{APIBaseURL: "http://127.0.0.1:9000/odata", TokenURL: "http://127.0.0.1:9000/token"}), true, ""},
		{"dev flag does not loosen the host shape", basic(SAPProducerConfig{Host: "x@10.0.0.1/"}), true, "host"},
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

// fakeSAP answers the CSRF fetch and accepts every write, counting requests.
func fakeSAP(t *testing.T, tls bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	hits := new(atomic.Int32)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method == http.MethodGet {
			w.Header().Set("X-CSRF-Token", "tok")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	var srv *httptest.Server
	if tls {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv, hits
}

// deliverWith runs Deliver for one envelope whose connection has the given
// sap_s4hana block, read through the same lookup Deliver uses in production.
func deliverWith(t *testing.T, p *sapProducer, block string) error {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p.db = db
	if p.logger == nil {
		p.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	nodes := `[{"id":"p1","type":"producer","config":{"type":"sap_s4hana","sap_s4hana":{` + block + `}}}]`
	mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(sfConn, sfTenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	env := envelope.New()
	env.TenantID, env.IntegrationID = sfTenant, sfConn
	env.Payload = []byte(`{"SalesOrderType":"OR"}`)
	return p.Deliver(context.Background(), env)
}

// plainProducer has an unguarded client, so only the string check stands
// between a refused config and the fake server. The short timeout bounds a
// dial to an unroutable literal when the check is missing.
func plainProducer(allowPrivate bool, hc *http.Client) *sapProducer {
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Second}
	}
	jar, _ := cookiejar.New(nil)
	hc.Jar = jar
	return &sapProducer{httpClient: hc, allowPrivate: allowPrivate}
}

const basicCreds = `"entity_set":"A_SalesOrder","username":"u","password":"p"`

// A refused address sends nothing, and the error is not Permanent: the runner
// retries it and then dead-letters it with its payload.
func TestDeliver_RefusesBadAddress(t *testing.T) {
	api, hits := fakeSAP(t, false)
	tests := []struct {
		name     string
		location string
		wantErr  string
	}{
		{"http to the api itself", fmt.Sprintf(`"api_base_url":%q`, api.URL), "https"},
		{"https loopback", fmt.Sprintf(`"api_base_url":%q`, strings.Replace(api.URL, "http://", "https://", 1)), "private"},
		{"metadata service", `"api_base_url":"https://169.254.169.254/latest"`, "private"},
		{"azure wire server", `"api_base_url":"https://168.63.129.16/"`, "private"},
		{"private network", `"api_base_url":"https://10.0.0.5/odata"`, "private"},
		{"ipv6 loopback", `"api_base_url":"https://[::1]:8443/odata"`, "private"},
		{"host aimed at the api", fmt.Sprintf(`"host":%q,"service":"S"`, "x@"+strings.TrimPrefix(api.URL, "http://")+"/"), "host"},
		{"token url aimed at the api", fmt.Sprintf(`"api_base_url":"https://%s/odata","auth_type":"oauth2","client_id":"id","client_secret":"s","token_url":%q`, defaultHost, api.URL), "token_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits.Store(0)
			err := deliverWith(t, plainProducer(false, nil), tt.location+","+basicCreds)
			if err == nil {
				t.Fatal("Deliver succeeded, want the address refused")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Deliver = %v, want an error containing %q", err, tt.wantErr)
			}
			if sdk.IsPermanent(err) {
				t.Errorf("Deliver = %v is Permanent: the message would be acked and dropped instead of dead-lettered", err)
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("the API received %d requests, want none", n)
			}
		})
	}
}

// host is checked for its shape even with the dev flag on. The fake SAP is an
// https server the client trusts and the host carries a path
// ("127.0.0.1:port/admin"), which makes a URL the dev policy would otherwise
// accept, so the host check is the only thing that keeps the credential off it.
func TestDeliver_HostInjectionRefusedEvenWithDevFlag(t *testing.T) {
	api, hits := fakeSAP(t, true)
	host := strings.TrimPrefix(api.URL, "https://") + "/admin"
	err := deliverWith(t, plainProducer(true, api.Client()), fmt.Sprintf(`"host":%q,"service":"S",`, host)+basicCreds)
	if err == nil || !strings.Contains(err.Error(), "host") {
		t.Errorf("Deliver = %v, want the host refused", err)
	}
	if sdk.IsPermanent(err) {
		t.Errorf("Deliver = %v is Permanent, want it retried and dead-lettered", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the host-injected target received %d requests, want none", n)
	}
}

// The operator's dev switch lets a node deliver to a mock on http://127.0.0.1,
// as docker compose does with mock-sap.
func TestDeliver_DevFlagAllowsLoopbackMock(t *testing.T) {
	api, hits := fakeSAP(t, false)
	if err := deliverWith(t, plainProducer(true, nil), fmt.Sprintf(`"api_base_url":%q,`, api.URL)+basicCreds); err != nil {
		t.Fatalf("Deliver to the mock with the dev flag: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("the mock received %d requests, want 2 (CSRF fetch, then the write)", n)
	}
}

// recordingTransport plays SAP for the default cloud host offline: a CSRF
// token on GET, 201 on a write. It records method and URL.
type recordingTransport struct {
	mu   sync.Mutex
	reqs []string
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.reqs = append(rt.reqs, r.Method+" "+r.URL.String())
	rt.mu.Unlock()
	resp := &http.Response{StatusCode: http.StatusCreated, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}
	if r.Method == http.MethodGet {
		resp.StatusCode = http.StatusOK
		resp.Header.Set("X-CSRF-Token", "tok")
	}
	return resp, nil
}

// The default URL, built from host and service, still delivers under the
// production policy.
func TestDeliver_DefaultHostStillDelivers(t *testing.T) {
	rt := &recordingTransport{}
	p := plainProducer(false, &http.Client{Transport: rt})
	if err := deliverWith(t, p, `"host":"`+defaultHost+`","service":"API_SALES_ORDER_SRV",`+basicCreds); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	root := "https://" + defaultHost + "/sap/opu/odata/sap/API_SALES_ORDER_SRV"
	want := []string{"GET " + root + "/", "POST " + root + "/A_SalesOrder"}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if strings.Join(rt.reqs, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(rt.reqs, "\n"), strings.Join(want, "\n"))
	}
}

// A hostname passes the config check, so the guard that matters is the dial.
// "localhost" resolves to loopback: the production client must not connect,
// the refusal is retried (dead-lettered after the retries), not dropped, and
// the error does not name the resolved address.
func TestDeliver_HostnameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
	port, accepted := tcpCounter(t)
	tests := []struct {
		name     string
		location string
	}{
		{"api_base_url", fmt.Sprintf(`"api_base_url":"https://localhost:%s/odata"`, port)},
		{"host", fmt.Sprintf(`"host":"localhost:%s","service":"S"`, port)},
		{"token_url", fmt.Sprintf(`"api_base_url":"https://%s/odata","auth_type":"oauth2","client_id":"id","client_secret":"s","token_url":"https://localhost:%s/token"`, defaultHost, port)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := plainProducer(false, safeurl.NewClient(5*time.Second, false)) // what Configure builds
			err := deliverWith(t, p, tt.location+","+basicCreds)
			if !errors.Is(err, safeurl.ErrBlockedAddress) {
				t.Errorf("Deliver = %v, want the dial refused", err)
			}
			// The error is the message's last error, which the tenant sees in
			// the DLQ view: it must not say where the name resolved to.
			if err != nil && (strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "::1")) {
				t.Errorf("Deliver = %v names the resolved address", err)
			}
			if sdk.IsPermanent(err) {
				t.Errorf("Deliver = %v is Permanent, want it retried and dead-lettered", err)
			}
			if n := accepted.Load(); n != 0 {
				t.Errorf("the client opened %d connections to loopback, want none", n)
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
