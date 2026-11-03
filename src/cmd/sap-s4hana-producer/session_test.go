package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Tests for #682: SAP sets a session cookie on the CSRF fetch, and the CSRF
// token is bound to it. Two connections of two tenants writing to the same SAP
// host must never send each other's session cookies, or tenant B's write can
// run in tenant A's SAP session.

const (
	sessCookie = "SAP_SESSIONID_ABC_100"
	ssoCookie  = "MYSAPSSO2"
	tokCookie  = "TOKEN_SESSION" // set by the OAuth2 token endpoint
	svcRoot    = "/sap/opu/odata/sap/API_SALES_ORDER_SRV"
)

// sessionSAP plays one SAP host. It knows who sent a request from the request's
// own credential (Basic user, bearer token, or the token call's client_id).
// Every CSRF fetch opens a new session for that caller: a session cookie, an
// SSO cookie and a token bound to the session. A write is accepted only with
// the session cookie and the token of one session. Any cookie of one caller on
// a request of another is recorded as a leak.
type sessionSAP struct {
	mu      sync.Mutex
	seq     int
	owner   map[string]string // cookie value -> caller it was issued to
	token   map[string]string // session cookie value -> CSRF token
	leaks   []string
	bad     []string       // writes refused for a missing or foreign session
	fetches map[string]int // caller -> CSRF fetches
	writes  map[string]int // caller -> accepted writes
}

func newSessionSAP(t *testing.T) (*sessionSAP, *httptest.Server) {
	t.Helper()
	s := &sessionSAP{owner: map[string]string{}, token: map[string]string{}, fetches: map[string]int{}, writes: map[string]int{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *sessionSAP) caller(r *http.Request) string {
	if r.URL.Path == "/token" {
		_ = r.ParseForm()
		return "client:" + r.PostForm.Get("client_id")
	}
	authz := r.Header.Get("Authorization")
	if b, ok := strings.CutPrefix(authz, "Basic "); ok {
		raw, _ := base64.StdEncoding.DecodeString(b)
		user, _, _ := strings.Cut(string(raw), ":")
		return "user:" + user
	}
	if tok, ok := strings.CutPrefix(authz, "Bearer at-"); ok {
		return "client:" + tok
	}
	return "anonymous"
}

func (s *sessionSAP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	who := s.caller(r)
	for _, c := range r.Cookies() {
		if o, ok := s.owner[c.Value]; !ok || o != who {
			s.leaks = append(s.leaks, fmt.Sprintf("%s %s by %s carried %s=%s issued to %q", r.Method, r.URL.Path, who, c.Name, c.Value, o))
		}
	}
	s.seq++
	switch {
	case r.URL.Path == "/token":
		v := fmt.Sprintf("tok-%d", s.seq)
		s.owner[v] = who
		http.SetCookie(w, &http.Cookie{Name: tokCookie, Value: v, Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-" + strings.TrimPrefix(who, "client:"), "expires_in": 3600})
	case r.Method == http.MethodGet && r.Header.Get("X-CSRF-Token") == "Fetch":
		sess, sso := fmt.Sprintf("sess-%d", s.seq), fmt.Sprintf("sso-%d", s.seq)
		s.owner[sess], s.owner[sso] = who, who
		s.token[sess] = "csrf-" + sess
		s.fetches[who]++
		http.SetCookie(w, &http.Cookie{Name: sessCookie, Value: sess, Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: ssoCookie, Value: sso, Path: "/"})
		w.Header().Set("X-CSRF-Token", s.token[sess])
	case r.Method == http.MethodPost && r.URL.Path == svcRoot+"/A_SalesOrder":
		var sessions []string
		for _, c := range r.Cookies() {
			if c.Name == sessCookie {
				sessions = append(sessions, c.Value)
			}
		}
		if len(sessions) != 1 || s.owner[sessions[0]] != who || s.token[sessions[0]] != r.Header.Get("X-CSRF-Token") {
			s.bad = append(s.bad, fmt.Sprintf("write by %s with sessions %v and token %q", who, sessions, r.Header.Get("X-CSRF-Token")))
			w.Header().Set("X-CSRF-Token", "Required")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		s.writes[who]++
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type sapConn struct {
	tenant, conn string
	caller       string // as sessionSAP names it
	block        func(base string) string
}

func basicConn(tenant, conn, user string) sapConn {
	return sapConn{tenant, conn, "user:" + user, func(base string) string {
		return fmt.Sprintf(`"api_base_url":%q,"entity_set":"A_SalesOrder","username":%q,"password":"pw-%s"`, base+svcRoot, user, user)
	}}
}

func oauthConn(tenant, conn, client string) sapConn {
	return sapConn{tenant, conn, "client:" + client, func(base string) string {
		return fmt.Sprintf(`"api_base_url":%q,"entity_set":"A_SalesOrder","auth_type":"oauth2","client_id":%q,"client_secret":"s-%s","token_url":%q`, base+svcRoot, client, client, base+"/token")
	}}
}

// TestDeliver_SessionCookiesStayWithTheirConnection runs deliveries for two
// connections of two tenants against one fake SAP host through Deliver and
// the client Configure builds, in sequence and concurrently.
func TestDeliver_SessionCookiesStayWithTheirConnection(t *testing.T) {
	a := basicConn("tenant-a", "conn-a", "alice")
	b := basicConn("tenant-b", "conn-b", "bob")
	aOAuth := oauthConn("tenant-a", "conn-a", "client-a")
	bOAuth := oauthConn("tenant-b", "conn-b", "client-b")
	tests := []struct {
		name       string
		conns      []sapConn
		order      []int // indexes into conns, delivered in this order
		concurrent bool
	}{
		{"basic, A then B then A", []sapConn{a, b}, []int{0, 1, 0}, false},
		{"basic, B then A then B", []sapConn{a, b}, []int{1, 0, 1}, false},
		{"oauth2 A and basic B, alternating", []sapConn{aOAuth, b}, []int{0, 1, 0, 1}, false},
		{"oauth2 A and oauth2 B on one token endpoint, alternating", []sapConn{aOAuth, bOAuth}, []int{0, 1, 0, 1}, false},
		{"basic, concurrent", []sapConn{a, b}, repeat([]int{0, 1}, 20), true},
		{"oauth2 A and basic B, concurrent", []sapConn{aOAuth, b}, repeat([]int{0, 1}, 20), true},
		{"oauth2 A and oauth2 B, concurrent", []sapConn{aOAuth, bOAuth}, repeat([]int{0, 1}, 20), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true") // the fake is http on loopback
			p := configuredProducer(t)
			fake, srv := newSessionSAP(t)

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.MatchExpectationsInOrder(false)
			p.db = db
			for _, i := range tt.order {
				c := tt.conns[i]
				nodes := `[{"id":"p1","type":"producer","config":{"type":"sap_s4hana","sap_s4hana":{` + c.block(srv.URL) + `}}}]`
				mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(c.conn, c.tenant).
					WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
			}

			deliver := func(c sapConn) error {
				env := envelope.New()
				env.TenantID, env.IntegrationID = c.tenant, c.conn
				env.Payload = []byte(`{"SalesOrderType":"OR"}`)
				return p.Deliver(context.Background(), env)
			}
			errs := make([]error, len(tt.order))
			if tt.concurrent {
				var wg sync.WaitGroup
				start := make(chan struct{})
				for k, i := range tt.order {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						errs[k] = deliver(tt.conns[i])
					}()
				}
				close(start)
				wg.Wait()
			} else {
				for k, i := range tt.order {
					errs[k] = deliver(tt.conns[i])
				}
			}

			for k, err := range errs {
				if err != nil {
					t.Errorf("delivery %d (%s): %v", k, tt.conns[tt.order[k]].conn, err)
				}
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			for _, l := range fake.leaks {
				t.Errorf("cookie crossed connections: %s", l)
			}
			for _, w := range fake.bad {
				t.Errorf("write not in its own session: %s", w)
			}
			want := map[string]int{}
			for _, i := range tt.order {
				want[tt.conns[i].caller]++
			}
			for who, n := range want {
				if fake.writes[who] != n || fake.fetches[who] != n {
					t.Errorf("%s: %d CSRF fetches and %d writes accepted, want %d of each (fetch, then write with its token)", who, fake.fetches[who], fake.writes[who], n)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("config lookups: %v", err)
			}
		})
	}
}

func repeat(xs []int, n int) []int {
	var out []int
	for range n {
		out = append(out, xs...)
	}
	return out
}
