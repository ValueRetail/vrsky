package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// #616: base_url is tenant config and the request carries the Sitoo Basic-auth
// credential, so the producer refuses a URL that is not https or that reaches
// a private address, and its client refuses to dial one. A refused base_url
// is retried and then dead-lettered (#721), not dropped.

func TestDeliver_RefusesBadBaseURL(t *testing.T) {
	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer api.Close()

	tests := []struct {
		name    string
		baseURL string
	}{
		{"http", api.URL},
		{"https loopback", "https://127.0.0.1:9/v2"},
		{"metadata service", "https://169.254.169.254/latest"},
		{"private network", "https://10.0.0.5/v2"},
		{"ipv6 loopback", "https://[::1]/v2"},
		{"userinfo", "https://u:p@api.mysitoo.com/v2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			nodes := fmt.Sprintf(`[{"id":"dst","type":"producer","config":{"type":"sitoo","sitoo":{
				"account_id":1,"site_id":2,"api_id":"id","api_password":"pw","base_url":%q,"resource":"warehouseitems"}}}]`, tt.baseURL)
			mock.ExpectQuery("SELECT nodes, edges FROM connections").WithArgs("conn-1", "tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))

			p := testProducer()
			p.db = db
			p.allowPrivate = false // production policy
			err = p.Deliver(context.Background(), &envelope.Envelope{
				TenantID: "tenant-1", IntegrationID: "conn-1", Payload: []byte(`[{"sku":"1"}]`),
			})
			if err == nil {
				t.Fatal("Deliver accepted the base_url, want an error")
			}
			// Retried and dead-lettered (#721), like the other destinations,
			// so the message can be replayed once the node is fixed.
			if sdk.IsPermanent(err) {
				t.Errorf("error is Permanent (acked and dropped); want it retried and dead-lettered: %v", err)
			}
			if hits.Load() != 0 {
				t.Errorf("the API received %d requests, want none", hits.Load())
			}
		})
	}
}

// A hostname passes the config check; the dial refuses it when it resolves to
// loopback.
func TestWrite_HostnameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
	port, accepted := tcpCounter(t)

	p := testProducer()
	p.allowPrivate = false
	p.httpClient = safeurl.NewClient(5*time.Second, false) // what Configure builds
	err := p.write(context.Background(), cfgFor("https://localhost:"+port+"/v2"), []byte(`[]`))
	if err == nil {
		t.Fatal("write connected to a hostname that resolves to loopback")
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
