package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Tests for #678 review: every page carries the credential and the next link
// comes from the server, so pagination stays on the configured service root's
// origin and never goes from https to http.

const svcPath = "/sap/opu/odata/sap/API_SALES_ORDER_SRV"

func TestFollowNext(t *testing.T) {
	root := "https://sap.example.com" + svcPath
	tests := []struct {
		name string
		next string
		want string // "" = refused
	}{
		{"same origin", root + "/A_SalesOrder?$skiptoken=P2", root + "/A_SalesOrder?$skiptoken=P2"},
		{"same origin, explicit default port", "https://sap.example.com:443" + svcPath + "/A_SalesOrder?$skiptoken=P2", "https://sap.example.com" + svcPath + "/A_SalesOrder?$skiptoken=P2"},
		{"host name case", "https://SAP.example.com" + svcPath + "/A?$skiptoken=1", root + "/A?$skiptoken=1"},
		{"same origin, outside the root path", "https://sap.example.com/other?x=1", "https://sap.example.com/other?x=1"},
		{"user name is dropped", "https://u:p@sap.example.com" + svcPath + "/A?$skiptoken=1", root + "/A?$skiptoken=1"},
		{"fragment is dropped", root + "/A?$skiptoken=1#f", root + "/A?$skiptoken=1"},
		{"http downgrade is rebased", "http://sap.example.com" + svcPath + "/A?$skiptoken=1", root + "/A?$skiptoken=1"},
		{"internal host under the root path is rebased", "http://sapgw01.corp.local:8000" + svcPath + "/A?$skiptoken=1", root + "/A?$skiptoken=1"},
		{"other port under the root path is rebased", "https://sap.example.com:8443" + svcPath + "/A?$skiptoken=1", root + "/A?$skiptoken=1"},
		{"other host, other path", "https://evil.example/steal", ""},
		{"other host, root path as a prefix of a segment", "https://evil.example" + svcPath + "X/A", ""},
		{"http downgrade outside the root path", "http://sap.example.com/other", ""},
		{"metadata service", "http://169.254.169.254/latest/meta-data/", ""},
		{"not absolute", "A_SalesOrder?$skiptoken=1", ""},
		{"encoded dot segments", "https://evil.example" + svcPath + "/%2e%2e/%2e%2e/admin", ""},
		{"encoded dot segments, upper case", "https://evil.example" + svcPath + "/%2E%2E/%2E%2E/admin", ""},
		{"encoded dots and slash in one segment", "https://evil.example" + svcPath + "/%2E%2E%2F%2E%2E%2Fadmin", ""},
		{"encoded slash", "https://evil.example" + svcPath + "/A%2fB", ""},
		{"literal dot segments leave the root", "https://evil.example" + svcPath + "/../../admin", ""},
		{"literal dot segments stay under the root", "https://evil.example" + svcPath + "/x/../A?$skiptoken=1", root + "/x/../A?$skiptoken=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SAPConfig{APIBaseURL: root}
			got, err := cfg.followNext(tt.next)
			if tt.want == "" {
				if !errors.Is(err, errNextOffService) {
					t.Errorf("followNext(%q) = %q, %v; want errNextOffService", tt.next, got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("followNext(%q) = %q, %v; want %q", tt.next, got, err, tt.want)
			}
		})
	}
}

type recordedReq struct {
	uri, auth string
	tls       bool
}

type recorder struct {
	mu   sync.Mutex
	reqs []recordedReq
}

func (r *recorder) add(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, recordedReq{uri: req.URL.RequestURI(), auth: req.Header.Get("Authorization"), tls: req.TLS != nil})
}

func (r *recorder) all() []recordedReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedReq(nil), r.reqs...)
}

func collectingConsumer() (*sapConsumer, *atomic.Int32) {
	published := new(atomic.Int32)
	return testConsumer(func(_ context.Context, env *envelope.Envelope) error {
		published.Add(1)
		return nil
	}), published
}

// The configured SAP returns a next link on another server. A link outside the
// service root's path stops the poll; one under it is fetched from the
// configured server. Either way the other server gets nothing, so it never
// sees the Basic credential.
func TestPagination_NextLinkToAnotherServer(t *testing.T) {
	tests := []struct {
		name      string
		nextPath  string // path and query on the other server
		wantErr   bool
		wantPages int // requests the configured server receives
	}{
		{"outside the service root", "/steal", true, 1},
		{"under the service root", svcPath + "/A_SalesOrder?$skiptoken=P2", false, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other := &recorder{}
			otherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				other.add(r)
				fmt.Fprint(w, `{"d":{"results":[{"SalesOrder":"stolen"}]}}`)
			}))
			defer otherSrv.Close()

			sap := &recorder{}
			sapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sap.add(r)
				if r.URL.Query().Get("$skiptoken") == "" {
					fmt.Fprintf(w, `{"d":{"results":[{"SalesOrder":"1"}],"__next":%q}}`, otherSrv.URL+tt.nextPath)
					return
				}
				fmt.Fprint(w, `{"d":{"results":[{"SalesOrder":"2"}]}}`)
			}))
			defer sapSrv.Close()

			c, published := collectingConsumer()
			cfg := &SAPConfig{APIBaseURL: sapSrv.URL + svcPath, EntitySet: "A_SalesOrder", Username: "u", Password: "p"}
			err := c.fetchAndPublish(context.Background(), "conn", "tenant", cfg, newAuthorizer(cfg, c.httpClient), c.logger)

			if got := other.all(); len(got) != 0 {
				t.Fatalf("the other server received %d requests (first with Authorization %q), want none", len(got), got[0].auth)
			}
			if tt.wantErr {
				if !errors.Is(err, errNextOffService) {
					t.Fatalf("fetchAndPublish = %v, want errNextOffService", err)
				}
				if strings.Contains(err.Error(), otherSrv.URL) || strings.Contains(err.Error(), "Basic") {
					t.Errorf("the error names the link or the credential: %v", err)
				}
			} else if err != nil {
				t.Fatalf("fetchAndPublish: %v", err)
			}
			if n := len(sap.all()); n != tt.wantPages {
				t.Errorf("the configured server received %d requests, want %d", n, tt.wantPages)
			}
			if n := int(published.Load()); n != tt.wantPages {
				t.Errorf("published %d pages, want %d", n, tt.wantPages)
			}
		})
	}
}

// A TLS-terminating gateway can answer with an http next link to its own
// host and port. The page is fetched over https from the configured origin,
// never over http.
func TestPagination_NoDowngradeToHTTP(t *testing.T) {
	sap := &recorder{}
	sapSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sap.add(r)
		if r.URL.Query().Get("$skiptoken") == "" {
			next := "http://" + r.Host + svcPath + "/A_SalesOrder?$skiptoken=P2"
			fmt.Fprintf(w, `{"d":{"results":[{"SalesOrder":"1"}],"__next":%q}}`, next)
			return
		}
		fmt.Fprint(w, `{"d":{"results":[{"SalesOrder":"2"}]}}`)
	}))
	defer sapSrv.Close()

	c, published := collectingConsumer()
	c.httpClient = sapSrv.Client()
	cfg := &SAPConfig{APIBaseURL: sapSrv.URL + svcPath, EntitySet: "A_SalesOrder", Username: "u", Password: "p"}
	if err := c.fetchAndPublish(context.Background(), "conn", "tenant", cfg, newAuthorizer(cfg, c.httpClient), c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	got := sap.all()
	if len(got) != 2 {
		t.Fatalf("the server handled %d requests, want 2 (both pages)", len(got))
	}
	for _, r := range got {
		if !r.tls {
			t.Errorf("request %s went over http, want https", r.uri)
		}
	}
	if got[1].uri != svcPath+"/A_SalesOrder?$skiptoken=P2" {
		t.Errorf("second page = %s, want the link's path and query", got[1].uri)
	}
	if published.Load() != 2 {
		t.Errorf("published %d pages, want 2", published.Load())
	}
}
