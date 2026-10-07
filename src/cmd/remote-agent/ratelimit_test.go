package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
)

// limitEnv is a gateway whose database is a sqlmock that knows only failed
// token lookups. Every lookup must be announced with expectFailedLookups, so a
// request that reaches the database unannounced is answered 503 — which is how
// these tests see "the limiter let it through".
type limitEnv struct {
	g    *gateway
	mock sqlmock.Sqlmock
	logs *bytes.Buffer
	now  time.Time
}

func newLimitEnv(t *testing.T) *limitEnv {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	// One connection: a request holds it from BEGIN to ROLLBACK, so the mock
	// sees whole transactions even when requests arrive at once.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	e := &limitEnv{g: newGateway(), mock: mock, logs: &bytes.Buffer{}, now: time.Unix(1_760_000_000, 0)}
	e.g.db = db
	e.g.logger = slog.New(slog.NewTextHandler(e.logs, nil))
	e.g.now = func() time.Time { return e.now }
	return e
}

func (e *limitEnv) expectFailedLookups(n int) {
	for i := 0; i < n; i++ {
		e.mock.ExpectBegin()
		e.mock.ExpectQuery(`UPDATE agent_registration_tokens`).
			WillReturnRows(sqlmock.NewRows([]string{"id", "t", "s", "c", "g"}))
		e.mock.ExpectRollback()
	}
}

// register posts a made-up token as the client at realIP would arrive through
// the ingress: X-Real-IP set by nginx, the peer address being nginx's own.
func (e *limitEnv) register(realIP string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/agent/v1/register",
		bytes.NewReader(mustJSON(agentproto.RegisterRequest{RegistrationToken: agentproto.RegTokenPrefix + "made-up"})))
	req.RemoteAddr = "10.244.0.7:41000"
	if realIP != "" {
		req.Header.Set("X-Real-IP", realIP)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.g.agentRoutes().ServeHTTP(rec, req)
	return rec
}

// fail sends n attempts that must each reach the database and come back 401.
func (e *limitEnv) fail(t *testing.T, realIP string, n int) {
	t.Helper()
	e.expectFailedLookups(n)
	for i := 0; i < n; i++ {
		if rec := e.register(realIP); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d from %s: want 401, got %d %s", i+1, realIP, rec.Code, rec.Body.String())
		}
	}
}

func TestRegister_BlocksAnAddressAfterTooManyFailures(t *testing.T) {
	e := newLimitEnv(t)
	before := testutil.ToFloat64(registerLimited)
	e.fail(t, "203.0.113.9", defaultRegisterMaxFailures)

	// No lookup is announced from here on: a refused request must not touch
	// the database at all.
	const refused = 5
	for i := 0; i < refused; i++ {
		rec := e.register("203.0.113.9")
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("attempt %d: want 429, got %d %s", defaultRegisterMaxFailures+i+1, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), agentproto.ErrRateLimited) {
			t.Errorf("429 body does not name %q: %s", agentproto.ErrRateLimited, rec.Body.String())
		}
		wait, err := strconv.Atoi(rec.Header().Get("Retry-After"))
		if err != nil || wait < 1 || wait > int(registerRefillEvery.Seconds()) {
			t.Errorf("Retry-After = %q, want 1..%d seconds", rec.Header().Get("Retry-After"), int(registerRefillEvery.Seconds()))
		}
	}
	if err := e.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("database: %v", err)
	}
	if got := testutil.ToFloat64(registerLimited) - before; got != refused {
		t.Errorf("vrsky_agent_register_limited_total rose by %v, want %d", got, refused)
	}
	// A block is logged when it starts, not once per refused request: the log
	// is one of the things the limit protects.
	if n := strings.Count(e.logs.String(), "Registration attempts blocked"); n != 1 {
		t.Errorf("%d block log lines for one block, want 1:\n%s", n, e.logs.String())
	}
	if !strings.Contains(e.logs.String(), "203.0.113.9") {
		t.Errorf("the block log line does not name the address:\n%s", e.logs.String())
	}
}

func TestRegister_LimitIsPerAddress(t *testing.T) {
	e := newLimitEnv(t)
	e.fail(t, "203.0.113.9", defaultRegisterMaxFailures)
	if rec := e.register("203.0.113.9"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the first address should be blocked, got %d", rec.Code)
	}
	// Another address is unaffected: it reaches the database and gets its
	// own answer.
	e.fail(t, "198.51.100.20", 1)
	if err := e.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("database: %v", err)
	}
}

func TestRegister_BlockedAddressRecovers(t *testing.T) {
	e := newLimitEnv(t)
	e.fail(t, "203.0.113.9", defaultRegisterMaxFailures)
	if rec := e.register("203.0.113.9"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 once the allowance is spent, got %d", rec.Code)
	}

	e.now = e.now.Add(registerRefillEvery - time.Second)
	if rec := e.register("203.0.113.9"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("one second short of the refill: want 429, got %d", rec.Code)
	}
	e.now = e.now.Add(time.Second)
	e.fail(t, "203.0.113.9", 1) // exactly one attempt was earned back
	if rec := e.register("203.0.113.9"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("one minute earns one attempt, not more: got %d", rec.Code)
	}

	// Left alone, the whole allowance comes back, and no more than that.
	e.now = e.now.Add(24 * time.Hour)
	e.fail(t, "203.0.113.9", defaultRegisterMaxFailures)
	if rec := e.register("203.0.113.9"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a day's rest must not bank more than the allowance: got %d", rec.Code)
	}
}

// Two hundred requests at the same instant must stop at the allowance. This is
// what spending the attempt BEFORE the lookup buys: counting a failure only
// once it is known lets every request that arrived meanwhile through.
func TestRegister_ConcurrentFailuresStopAtTheLimit(t *testing.T) {
	e := newLimitEnv(t)
	e.expectFailedLookups(defaultRegisterMaxFailures)

	const clients = 200
	codes := make([]int, clients)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i] = e.register("203.0.113.9").Code
		}(i)
	}
	close(start)
	wg.Wait()

	count := map[int]int{}
	for _, c := range codes {
		count[c]++
	}
	if count[http.StatusUnauthorized] != defaultRegisterMaxFailures ||
		count[http.StatusTooManyRequests] != clients-defaultRegisterMaxFailures {
		t.Fatalf("answers by status = %v, want %d × 401 and %d × 429 (a 503 is a request that reached the database past the limit)",
			count, defaultRegisterMaxFailures, clients-defaultRegisterMaxFailures)
	}
	if err := e.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("database: %v", err)
	}
}

// The address is the one the ingress reports. A client that sends a different
// X-Forwarded-For with every request must still be one address.
func TestClientAddress_IgnoresForwardedFor(t *testing.T) {
	e := newLimitEnv(t)
	e.expectFailedLookups(defaultRegisterMaxFailures)
	for i := 0; i < defaultRegisterMaxFailures; i++ {
		rec := e.register("203.0.113.9", "X-Forwarded-For", fmt.Sprintf("192.0.2.%d, 203.0.113.9", i+1))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d", i+1, rec.Code)
		}
	}
	if rec := e.register("203.0.113.9", "X-Forwarded-For", "192.0.2.250, 203.0.113.9"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a new X-Forwarded-For dodged the limit: got %d, want 429", rec.Code)
	}

	// Without X-Real-IP (no ingress in front: local runs) it is the peer
	// address, still never the forwarded header.
	req := httptest.NewRequest(http.MethodPost, "/agent/v1/register", nil)
	req.RemoteAddr = "198.51.100.20:5555"
	req.Header.Set("X-Forwarded-For", "192.0.2.77")
	if got := clientAddress(req); got != "198.51.100.20" {
		t.Errorf("clientAddress = %q, want the peer address 198.51.100.20", got)
	}
	// A header that is not an address is not trusted to be a key either.
	req.Header.Set("X-Real-IP", "not-an-address")
	if got := clientAddress(req); got != "198.51.100.20" {
		t.Errorf("clientAddress with a junk X-Real-IP = %q, want the peer address", got)
	}
}

// The key itself is failurelimit.AddressKey; here only that the handler uses it.
func TestClientAddress_IPv6IsOnePrefix(t *testing.T) {
	// Through the handler: the second address of the prefix inherits the block.
	e := newLimitEnv(t)
	e.fail(t, "2001:db8:1:2::1", defaultRegisterMaxFailures)
	if rec := e.register("2001:db8:1:2:ffff:abcd:0:9"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("another address in the blocked /64: got %d, want 429", rec.Code)
	}
}

func TestRegister_LimitCanBeTurnedOff(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		env  string
		want int
	}{
		{"", defaultRegisterMaxFailures},
		{"25", 25},
		{"0", 0},
		{"-3", defaultRegisterMaxFailures},
		{"lots", defaultRegisterMaxFailures},
	} {
		t.Setenv("AGENT_REGISTER_MAX_FAILURES", tc.env)
		if got := registerMaxFailuresFromEnv(quiet); got != tc.want {
			t.Errorf("AGENT_REGISTER_MAX_FAILURES=%q → %d, want %d", tc.env, got, tc.want)
		}
	}

	// 0 means off: every attempt reaches the database, none is refused.
	t.Setenv("AGENT_REGISTER_MAX_FAILURES", "0")
	e := newLimitEnv(t)
	e.g.registerLimit = newRegisterLimiter(registerMaxFailuresFromEnv(quiet))
	e.fail(t, "203.0.113.9", 3*defaultRegisterMaxFailures)
	if err := e.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("database: %v", err)
	}
}
