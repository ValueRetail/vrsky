package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Tests for #667: base_url (or the datacenter and account code it is derived
// from) is tenant config and the request carries the staff token, so the
// producer refuses a URL that is not https, that reaches a private address,
// or whose derived host is not Brightpearl's, and its client refuses to dial
// a private address.

func TestCheckBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		cfg          BrightpearlProducerConfig
		allowPrivate bool
		ok           bool
	}{
		{"derived default", BrightpearlProducerConfig{Datacenter: "eu1", AccountCode: "acme"}, false, true},
		{"derived, other datacenter", BrightpearlProducerConfig{Datacenter: "USE1", AccountCode: "acme_2-x"}, false, true},
		{"override to the default host", BrightpearlProducerConfig{BaseURL: "https://euw1.brightpearlconnect.com/public-api/acme"}, false, true},
		{"http", BrightpearlProducerConfig{BaseURL: "http://euw1.brightpearlconnect.com/public-api/acme"}, false, false},
		{"loopback", BrightpearlProducerConfig{BaseURL: "https://127.0.0.1/x"}, false, false},
		{"loopback v6", BrightpearlProducerConfig{BaseURL: "https://[::1]/x"}, false, false},
		{"private 10/8", BrightpearlProducerConfig{BaseURL: "https://10.0.0.1/x"}, false, false},
		{"cloud metadata", BrightpearlProducerConfig{BaseURL: "https://169.254.169.254/latest/meta-data"}, false, false},
		{"azure wire server", BrightpearlProducerConfig{BaseURL: "https://168.63.129.16/"}, false, false},
		{"userinfo", BrightpearlProducerConfig{BaseURL: "https://u:p@euw1.brightpearlconnect.com/public-api/acme"}, false, false},
		{"datacenter moves the host", BrightpearlProducerConfig{Datacenter: "x@10.0.0.1/", AccountCode: "acme"}, false, false},
		{"datacenter adds a domain", BrightpearlProducerConfig{Datacenter: "eu1.evil.example/", AccountCode: "acme"}, false, false},
		{"account code leaves its segment", BrightpearlProducerConfig{Datacenter: "eu1", AccountCode: "../x"}, false, false},
		{"dev flag: http mock", BrightpearlProducerConfig{BaseURL: "http://bp-mock:8080/public-api/acme"}, true, true},
		{"dev flag: loopback mock", BrightpearlProducerConfig{BaseURL: "http://127.0.0.1:9000/"}, true, true},
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

// deliverWith runs Deliver for one connection whose brightpearl block holds
// location (base_url, or datacenter and account_code).
func deliverWith(t *testing.T, p *brightpearlProducer, location string) error {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	nodes := `[{"id":"dst","type":"producer","config":{"type":"brightpearl","brightpearl":{` +
		location + `,"app_ref":"app","staff_token":"tok","resource":"/order-service/order"}}}]`
	mock.ExpectQuery("SELECT nodes FROM connections").WithArgs("conn-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	p.db = db
	return p.Deliver(context.Background(), &envelope.Envelope{
		TenantID: "tenant-1", IntegrationID: "conn-1", Payload: []byte(`{"orderTypeCode":"SO"}`),
	})
}

// A refused base URL is a retriable error: the runner NAKs, and after the
// last attempt the message waits in the DLQ until the node is fixed.
// sdk.Permanent would ack and drop it. Each case has its own fake API, served
// over http and https, and the producer uses a client that trusts the https
// one, so without the check the request would arrive.
func TestDeliver_RefusesBadBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		location func(httpAPI, httpsAPI string) string
	}{
		{"http", func(h, _ string) string { return fmt.Sprintf(`"base_url":%q`, h) }},
		{"https loopback", func(_, s string) string { return fmt.Sprintf(`"base_url":%q`, s) }},
		{"metadata service", func(_, _ string) string { return `"base_url":"https://169.254.169.254/latest"` }},
		{"private network", func(_, _ string) string { return `"base_url":"https://10.0.0.5/x"` }},
		{"ipv6 loopback", func(_, _ string) string { return `"base_url":"https://[::1]/x"` }},
		{"userinfo", func(_, _ string) string {
			return `"base_url":"https://u:p@euw1.brightpearlconnect.com/public-api/acme"`
		}},
		{"datacenter aimed at the api", func(_, s string) string {
			return fmt.Sprintf(`"datacenter":%q,"account_code":"acme"`, "x@"+strings.TrimPrefix(s, "https://")+"/")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			count := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) })
			plain := httptest.NewServer(count)
			defer plain.Close()
			tlsAPI := httptest.NewTLSServer(count)
			defer tlsAPI.Close()

			p := testProducer()
			p.allowPrivate = false         // production policy
			p.httpClient = tlsAPI.Client() // trusts the fake API; not the dial guard
			err := deliverWith(t, p, tt.location(plain.URL, tlsAPI.URL))
			if err == nil {
				t.Fatal("Deliver accepted the base URL, want an error")
			}
			if sdk.IsPermanent(err) {
				t.Errorf("error %v is Permanent: the message would be dropped, not dead-lettered", err)
			}
			if hits.Load() != 0 {
				t.Errorf("the API received %d requests, want none", hits.Load())
			}
		})
	}
}

// recordingTransport answers 200 and records the URL, so the default
// (derived) host can be exercised offline.
type recordingTransport struct{ urls []string }

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.urls = append(rt.urls, r.URL.String())
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
}

// The default URL, derived from datacenter and account code, still delivers
// under the production policy.
func TestDeliver_DerivedDefaultStillDelivers(t *testing.T) {
	rt := &recordingTransport{}
	p := testProducer()
	p.allowPrivate = false
	p.httpClient = &http.Client{Transport: rt}
	if err := deliverWith(t, p, `"datacenter":"eu1","account_code":"acme"`); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	want := "https://euw1.brightpearlconnect.com/public-api/acme/order-service/order"
	if len(rt.urls) != 1 || rt.urls[0] != want {
		t.Errorf("requests = %v, want exactly [%s]", rt.urls, want)
	}
}

// The operator's dev switch lets the producer deliver to a mock on
// http://127.0.0.1.
func TestDeliver_DevFlagAllowsLoopbackMock(t *testing.T) {
	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer api.Close()

	p := testProducer()
	p.allowPrivate = true
	p.httpClient = safeurl.NewClient(5*time.Second, true) // what Configure builds with the switch on
	if err := deliverWith(t, p, fmt.Sprintf(`"base_url":%q`, api.URL)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("the mock received %d requests, want 1", hits.Load())
	}
}

// A hostname passes the config check; the dial refuses it when it resolves to
// loopback. The refusal is retriable: a transient DNS answer must not drop
// the message.
func TestWrite_HostnameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
	port, accepted := tcpCounter(t)
	cfg := cfgFor("https://localhost:" + port + "/public-api/acme")
	if err := cfg.checkBaseURL(false); err != nil {
		t.Fatalf("a hostname is not refused at config time (only the dial can tell), got %v", err)
	}

	p := testProducer()
	p.httpClient = safeurl.NewClient(5*time.Second, false) // what Configure builds
	err := p.write(context.Background(), cfg, []byte(`{}`))
	if err == nil {
		t.Fatal("write connected to a hostname that resolves to loopback")
	}
	if !errors.Is(err, safeurl.ErrBlockedAddress) {
		t.Errorf("error %v does not wrap safeurl.ErrBlockedAddress", err)
	}
	if sdk.IsPermanent(err) {
		t.Errorf("error %v is Permanent: the message would be dropped, not retried", err)
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
