package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Tests for #736: a delivery ends its SAP session with the ICF logoff.

const logoffPath = "/sap/public/bc/icf/logoff"

// logoffSAP is sessionSAP plus the logoff service.
type logoffSAP struct {
	*sessionSAP
	mu2     sync.Mutex
	logoffs []string // session cookie values seen on logoff
	creds   []string // Authorization headers seen on logoff
	status  int
	delay   time.Duration
	block   chan struct{}
}

func (l *logoffSAP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != logoffPath {
		l.sessionSAP.ServeHTTP(w, r)
		return
	}
	l.mu2.Lock()
	sess := ""
	for _, c := range r.Cookies() {
		if c.Name == sessCookie {
			sess = c.Value
		}
	}
	l.logoffs = append(l.logoffs, sess)
	l.creds = append(l.creds, r.Header.Get("Authorization"))
	l.mu2.Unlock()
	if l.block != nil {
		select {
		case <-l.block:
		case <-r.Context().Done():
		}
	}
	time.Sleep(l.delay)
	if l.status != 0 {
		w.WriteHeader(l.status)
	}
}

// runDelivery is the Deliver tail: session client, deferred logoff, write.
func runDelivery(ctx context.Context, p *sapProducer, cfg *SAPProducerConfig, payload string) error {
	hc := p.sessionClient()
	defer p.logoff(ctx, hc, cfg)
	return p.write(ctx, hc, cfg, newAuthorizer(cfg, hc), []byte(payload))
}

func newLogoffSAP(t *testing.T) (*logoffSAP, *httptest.Server) {
	t.Helper()
	l := &logoffSAP{sessionSAP: &sessionSAP{owner: map[string]string{}, token: map[string]string{}, fetches: map[string]int{}, writes: map[string]int{}, logoffs: map[string]int{}}}
	srv := httptest.NewServer(l)
	t.Cleanup(srv.Close)
	return l, srv
}

func producerFor(t *testing.T) *sapProducer {
	t.Helper()
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
	return configuredProducer(t)
}

func TestLogoff_EachDeliveryEndsItsOwnSession(t *testing.T) {
	l, srv := newLogoffSAP(t)
	p := producerFor(t)
	cfg := basicCfg(srv.URL + svcRoot)
	cfg.Username, cfg.Password = "alice", "pw-alice"
	for range 3 {
		if err := runDelivery(context.Background(), p, cfg, `{}`); err != nil {
			t.Fatal(err)
		}
	}
	if len(l.logoffs) != 3 {
		t.Fatalf("logoffs = %v, want 3", l.logoffs)
	}
	seen := map[string]bool{}
	for _, s := range l.logoffs {
		if s == "" || seen[s] {
			t.Errorf("logoff session cookie %q empty or repeated in %v", s, l.logoffs)
		}
		seen[s] = true
		if _, ok := l.owner[s]; !ok {
			t.Errorf("logoff carried unknown session %q", s)
		}
	}
	for _, a := range l.creds {
		if a != "" {
			t.Errorf("logoff carried a credential: %q", a)
		}
	}
	if len(l.leaks) != 0 {
		t.Errorf("leaks: %v", l.leaks)
	}
}

func TestLogoff_AlsoAfterAFailedDelivery(t *testing.T) {
	l, srv := newLogoffSAP(t)
	p := producerFor(t)
	cfg := basicCfg(srv.URL + svcRoot)
	cfg.EntitySet = "Missing" // the write gets 404 from the fake
	cfg.Username, cfg.Password = "alice", "pw-alice"
	if err := runDelivery(context.Background(), p, cfg, `{}`); err == nil {
		t.Fatal("want the write to fail")
	}
	if len(l.logoffs) != 1 || l.logoffs[0] == "" {
		t.Fatalf("logoffs = %v, want one with a session", l.logoffs)
	}
}

func TestLogoff_NotSentWithoutSessionCookie(t *testing.T) {
	var logoffs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == logoffPath:
			logoffs++
		case r.Method == http.MethodGet:
			w.Header().Set("X-CSRF-Token", "t") // no Set-Cookie
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}))
	t.Cleanup(srv.Close)
	p := producerFor(t)
	cfg := basicCfg(srv.URL + svcRoot)
	if err := runDelivery(context.Background(), p, cfg, `{}`); err != nil {
		t.Fatal(err)
	}
	if logoffs != 0 {
		t.Errorf("logoffs = %d, want 0 without a session cookie", logoffs)
	}
}

func TestLogoff_FailureDoesNotChangeTheResult(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusFound} {
		l, srv := newLogoffSAP(t)
		l.status = status
		p := producerFor(t)
		cfg := basicCfg(srv.URL + svcRoot)
		cfg.Username, cfg.Password = "alice", "pw-alice"
		if err := runDelivery(context.Background(), p, cfg, `{}`); err != nil {
			t.Errorf("status %d: delivery failed: %v", status, err)
		}
		if len(l.logoffs) != 1 {
			t.Errorf("status %d: logoffs = %v", status, l.logoffs)
		}
	}
}

func TestLogoff_SlowLogoffIsBounded(t *testing.T) {
	l, srv := newLogoffSAP(t)
	l.block = make(chan struct{}) // never released: the client must give up
	t.Cleanup(func() { close(l.block) })
	p := producerFor(t)
	cfg := basicCfg(srv.URL + svcRoot)
	cfg.Username, cfg.Password = "alice", "pw-alice"
	start := time.Now()
	if err := runDelivery(context.Background(), p, cfg, `{}`); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	if d := time.Since(start); d > logoffTimeout+3*time.Second {
		t.Errorf("delivery took %v, logoff is not bounded by %v", d, logoffTimeout)
	}
}

func TestLogoff_RunsWhenTheDeliveryContextIsCancelled(t *testing.T) {
	l, srv := newLogoffSAP(t)
	p := producerFor(t)
	cfg := basicCfg(srv.URL + svcRoot)
	cfg.Username, cfg.Password = "alice", "pw-alice"
	hc := p.sessionClient()
	ctx, cancel := context.WithCancel(context.Background())
	if err := p.write(ctx, hc, cfg, newAuthorizer(cfg, hc), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	cancel()
	p.logoff(ctx, hc, cfg)
	if len(l.logoffs) != 1 {
		t.Errorf("logoffs = %v, want 1 after cancel", l.logoffs)
	}
}

func TestLogoff_OnlyToTheConfiguredOrigin(t *testing.T) {
	var other []string
	otherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other = append(other, r.URL.Path)
	}))
	t.Cleanup(otherSrv.Close)
	l, srv := newLogoffSAP(t)
	// SAP answers the logoff with a redirect to another host: not followed.
	l.status = http.StatusFound
	p := producerFor(t)
	cfg := basicCfg(srv.URL + svcRoot)
	cfg.Username, cfg.Password = "alice", "pw-alice"
	if err := runDelivery(context.Background(), p, cfg, `{}`); err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Errorf("other host got %v", other)
	}
	// The URL is built from the service root's origin, whatever else is set.
	cfg.Host = strings.TrimPrefix(otherSrv.URL, "http://")
	u, ok := cfg.logoffURL()
	if !ok || !strings.HasPrefix(u, srv.URL+logoffPath) {
		t.Errorf("logoffURL = %q, want origin %s", u, srv.URL)
	}
	cfg.SAPClient = "100"
	if u, _ := cfg.logoffURL(); !strings.HasSuffix(u, "?sap-client=100") {
		t.Errorf("logoffURL = %q, want sap-client", u)
	}
}

// TestLogoff_DeliverLogsOffAfterAFailedWrite goes through Deliver itself: a
// write SAP refuses still ends with a logoff.
func TestLogoff_DeliverLogsOffAfterAFailedWrite(t *testing.T) {
	l, srv := newLogoffSAP(t)
	p := producerFor(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p.db = db
	nodes := `[{"id":"p1","type":"producer","config":{"type":"sap_s4hana","sap_s4hana":{"api_base_url":"` + srv.URL + svcRoot + `","entity_set":"Missing","username":"alice","password":"pw"}}}]`
	mock.ExpectQuery("SELECT nodes FROM connections").WithArgs("conn", "tenant").
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	env := envelope.New()
	env.TenantID, env.IntegrationID = "tenant", "conn"
	env.Payload = []byte(`{}`)
	if err := p.Deliver(context.Background(), env); err == nil {
		t.Fatal("want the write to fail")
	}
	if len(l.logoffs) != 1 || l.logoffs[0] == "" {
		t.Errorf("logoffs = %v, want one with a session", l.logoffs)
	}
}
