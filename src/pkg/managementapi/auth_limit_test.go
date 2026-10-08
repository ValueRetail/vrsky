package managementapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/crypto/bcrypt"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// loginRepo is a MockRepository that knows some users and records what the
// login handler did to it: lookups, sessions, audit rows.
type loginRepo struct {
	*MockRepository
	users    map[string]*User
	lookups  int
	sessions []*Session
	audits   []*AuthAuditLog
	known    map[string]bool // email + "|" + network → a past successful login
	knownQ   []string        // the networks knownAddress asked about
}

func newLoginRepo() *loginRepo {
	return &loginRepo{MockRepository: NewMockRepository(), users: map[string]*User{}, known: map[string]bool{}}
}

// addUser creates a verified user with a cheap hash so the suite stays fast
// under -race; VerifyPassword honours the cost stored in the hash.
func (r *loginRepo) addUser(t *testing.T, email, password string) *User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := NewUser(email, string(hash), "Test User")
	u.Status, u.EmailVerified = UserStatusActive, true
	r.users[email] = u
	return u
}

func (r *loginRepo) GetUserByEmail(_ context.Context, email string) (*User, error) {
	r.lookups++
	if u, ok := r.users[email]; ok {
		return u, nil
	}
	return nil, &NotFoundError{ResourceType: "User", ResourceID: email}
}
func (r *loginRepo) CreateSession(_ context.Context, s *Session) error {
	r.sessions = append(r.sessions, s)
	return nil
}
func (r *loginRepo) CreateAuthAuditLog(_ context.Context, l *AuthAuditLog) error {
	r.audits = append(r.audits, l)
	return nil
}
func (r *loginRepo) HasLoginSucceededFrom(_ context.Context, email, network string, _ time.Time) (bool, error) {
	r.knownQ = append(r.knownQ, network)
	return r.known[email+"|"+network], nil
}

func (r *loginRepo) auditCount(eventType, status string) int {
	n := 0
	for _, a := range r.audits {
		if a.EventType == eventType && a.Status == status {
			n++
		}
	}
	return n
}

// loginEnv is a handler with a fixed clock and a captured default log.
type loginEnv struct {
	h    *Handler
	repo *loginRepo
	now  time.Time
	logs *bytes.Buffer
}

func newLoginEnv(t *testing.T) *loginEnv {
	t.Helper()
	e := &loginEnv{repo: newLoginRepo(), now: time.Unix(1_760_000_000, 0), logs: &bytes.Buffer{}}
	e.h = NewHandler(e.repo, NewValidator())
	e.h.authLimits.now = func() time.Time { return e.now }
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(e.logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return e
}

// login posts as a browser at client would arrive in prod: through ingress-nginx
// and the UI pod's nginx, so X-Forwarded-For is "<client>, <ingress pod>".
func (e *loginEnv) login(client, email, password string) *httptest.ResponseRecorder {
	return e.post(e.h.LoginUser, client, mustMarshal(LoginRequest{Email: email, Password: password}))
}

func (e *loginEnv) post(handler http.HandlerFunc, client string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.RemoteAddr = "10.244.1.5:51000"
	req.Header.Set("X-Real-IP", "10.240.0.4")
	req.Header.Set("X-Forwarded-For", client+", 10.240.0.4")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func client(i int) string { return fmt.Sprintf("203.0.113.%d", i) }

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int, what string) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s: want %d, got %d %s", what, want, rec.Code, rec.Body.String())
	}
}

func TestLogin_BlocksAnAddressAfterTooManyFailures(t *testing.T) {
	e := newLoginEnv(t)
	before := testutil.ToFloat64(authLimited.WithLabelValues("login", "address"))

	// Ten wrong passwords for ten different accounts: the address limit is
	// about where the guesses come from, not whom they are aimed at.
	for i := 0; i < defaultLoginMaxFailures; i++ {
		wantStatus(t, e.login(client(1), fmt.Sprintf("user%d@example.com", i), "wrong"), http.StatusUnauthorized, fmt.Sprintf("attempt %d", i+1))
	}
	lookups := e.repo.lookups

	for i := 0; i < 3; i++ {
		rec := e.login(client(1), "another@example.com", "wrong")
		wantStatus(t, rec, http.StatusTooManyRequests, fmt.Sprintf("refusal %d", i+1))
		var body ErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body.Error != "RateLimited" || body.Message != limitedMessage {
			t.Errorf("429 body = %+v", body)
		}
		if ra, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || ra < 1 || ra > int(loginAddressRefill.Seconds()) {
			t.Errorf("Retry-After = %q, want 1..%d", rec.Header().Get("Retry-After"), int(loginAddressRefill.Seconds()))
		}
	}
	if e.repo.lookups != lookups {
		t.Errorf("a refused request still looked the user up (%d lookups after, %d before)", e.repo.lookups, lookups)
	}
	if got := testutil.ToFloat64(authLimited.WithLabelValues("login", "address")) - before; got != 3 {
		t.Errorf("vrsky_auth_limited_total{login,address} rose by %v, want 3", got)
	}
	// Once per block, not once per refused request.
	if n := strings.Count(e.logs.String(), "Sign-in attempts blocked"); n != 1 {
		t.Errorf("%d block log lines, want 1:\n%s", n, e.logs.String())
	}
	if n := e.repo.auditCount("login", "blocked"); n != 1 {
		t.Errorf("%d audit rows for the block, want 1", n)
	}
	// Another address is unaffected.
	wantStatus(t, e.login(client(2), "another@example.com", "wrong"), http.StatusUnauthorized, "other address")
}

func TestLogin_BlocksAnAccountAcrossAddresses(t *testing.T) {
	e := newLoginEnv(t)
	e.repo.addUser(t, "victim@example.com", "correct horse battery staple")

	for i := 0; i < defaultLoginMaxFailures; i++ {
		wantStatus(t, e.login(client(10+i), "victim@example.com", "wrong"), http.StatusUnauthorized, fmt.Sprintf("attempt %d", i+1))
	}
	rec := e.login(client(99), "victim@example.com", "wrong")
	wantStatus(t, rec, http.StatusTooManyRequests, "11th address against the same account")
	if ra, _ := strconv.Atoi(rec.Header().Get("Retry-After")); ra < 1 || ra > int(loginAccountRefill.Seconds()) {
		t.Errorf("Retry-After = %q, want 1..%d", rec.Header().Get("Retry-After"), int(loginAccountRefill.Seconds()))
	}
	// The account is blocked, not the address: the same address can still
	// try another account.
	wantStatus(t, e.login(client(99), "someone-else@example.com", "wrong"), http.StatusUnauthorized, "same address, other account")
	// Case and whitespace do not make a new account.
	wantStatus(t, e.login(client(98), "  Victim@Example.com ", "wrong"), http.StatusTooManyRequests, "same account spelt differently")
}

// An email nobody registered is limited exactly like one somebody did, so the
// limit cannot be used to find out which is which.
func TestLogin_UnknownEmailIsLimitedLikeARealOne(t *testing.T) {
	e := newLoginEnv(t)
	e.repo.addUser(t, "real@example.com", "correct horse battery staple")
	block := func(email string) *httptest.ResponseRecorder {
		for i := 0; i < defaultLoginMaxFailures; i++ {
			wantStatus(t, e.login(client(20+i), email, "wrong"), http.StatusUnauthorized, email+" attempt")
		}
		return e.login(client(50), email, "wrong")
	}
	real, fake := block("real@example.com"), block("nobody@example.com")
	if real.Code != http.StatusTooManyRequests || fake.Code != real.Code || fake.Body.String() != real.Body.String() ||
		fake.Header().Get("Retry-After") != real.Header().Get("Retry-After") {
		t.Fatalf("real account: %d %s %s\nunknown email: %d %s %s", real.Code, real.Header().Get("Retry-After"), real.Body.String(),
			fake.Code, fake.Header().Get("Retry-After"), fake.Body.String())
	}
}

// An unknown email costs the same bcrypt verify as a wrong password, at the
// real cost, so the time a login takes says nothing about who has an account.
func TestLogin_UnknownEmailStillVerifiesAPassword(t *testing.T) {
	e := newLoginEnv(t)
	u := e.repo.addUser(t, "real@example.com", "correct horse battery staple")
	var hashes []string
	e.h.verifyPassword = func(hash, password string) error {
		hashes = append(hashes, hash)
		return auth.VerifyPassword(hash, password)
	}

	wantStatus(t, e.login(client(1), "nobody@example.com", "wrong"), http.StatusUnauthorized, "unknown email")
	wantStatus(t, e.login(client(1), "real@example.com", "wrong"), http.StatusUnauthorized, "wrong password")
	wantStatus(t, e.login(client(1), "nobody@example.com", "wrong"), http.StatusUnauthorized, "unknown email again")
	if len(hashes) != 3 {
		t.Fatalf("verify ran %d times for 3 attempts; an unknown email must cost a verify too", len(hashes))
	}
	if hashes[1] != u.PasswordHash {
		t.Fatalf("the real account was verified against %q, want its own hash", hashes[1])
	}
	if hashes[0] == u.PasswordHash || hashes[0] != hashes[2] {
		t.Fatalf("unknown emails verified against %q then %q; want one dummy hash, not a user's", hashes[0], hashes[2])
	}
	if cost, err := bcrypt.Cost([]byte(hashes[0])); err != nil || cost != auth.DefaultBcryptCost {
		t.Fatalf("dummy hash cost %d (%v), want the real cost %d so the timing matches", cost, err, auth.DefaultBcryptCost)
	}
}

func TestLogin_SuccessDoesNotCount(t *testing.T) {
	e := newLoginEnv(t)
	e.repo.addUser(t, "ok@example.com", "correct horse battery staple")

	for i := 0; i < 3*defaultLoginMaxFailures; i++ {
		wantStatus(t, e.login(client(1), "ok@example.com", "correct horse battery staple"), http.StatusOK, fmt.Sprintf("login %d", i+1))
	}
	if len(e.repo.sessions) != 3*defaultLoginMaxFailures {
		t.Fatalf("%d sessions created, want %d", len(e.repo.sessions), 3*defaultLoginMaxFailures)
	}
	// After all that, both allowances are whole: ten failures, then 429.
	for i := 0; i < defaultLoginMaxFailures; i++ {
		wantStatus(t, e.login(client(1), "ok@example.com", "wrong"), http.StatusUnauthorized, fmt.Sprintf("failure %d", i+1))
	}
	wantStatus(t, e.login(client(1), "ok@example.com", "wrong"), http.StatusTooManyRequests, "past the allowance")
}

func TestLogin_KnownAddressGetsThroughABlockedAccount(t *testing.T) {
	e := newLoginEnv(t)
	e.repo.addUser(t, "owner@example.com", "correct horse battery staple")
	// The owner has logged in from the office before (the key is the
	// limiter's: IPv4 address, or IPv6 /64).
	e.repo.known["owner@example.com|203.0.113.5"] = true
	e.repo.known["owner@example.com|2001:db8:1:2::/64"] = true

	for i := 0; i < defaultLoginMaxFailures; i++ {
		wantStatus(t, e.login(client(100+i), "owner@example.com", "wrong"), http.StatusUnauthorized, fmt.Sprintf("stranger %d", i+1))
	}
	wantStatus(t, e.login(client(150), "owner@example.com", "correct horse battery staple"), http.StatusTooManyRequests, "stranger with the right password")
	wantStatus(t, e.login(client(5), "owner@example.com", "wrong"), http.StatusUnauthorized, "the office, wrong password: judged, not refused")
	wantStatus(t, e.login(client(5), "owner@example.com", "correct horse battery staple"), http.StatusOK, "the office, right password")
	wantStatus(t, e.login("2001:db8:1:2:aaaa::7", "owner@example.com", "correct horse battery staple"), http.StatusOK, "another address in the known /64")
	if len(e.repo.knownQ) == 0 || e.repo.knownQ[len(e.repo.knownQ)-1] != "2001:db8:1:2::/64" {
		t.Errorf("knownAddress asked about %v, want the /64 key last", e.repo.knownQ)
	}
	// The exception is for THAT account: the office is a stranger to another.
	e.repo.addUser(t, "other@example.com", "pw-other-12345")
	for i := 0; i < defaultLoginMaxFailures; i++ {
		wantStatus(t, e.login(client(100+i), "other@example.com", "wrong"), http.StatusUnauthorized, fmt.Sprintf("stranger %d on other", i+1))
	}
	wantStatus(t, e.login(client(5), "other@example.com", "pw-other-12345"), http.StatusTooManyRequests, "the office is not known to the other account")
}

func TestLogin_BlockedCorrectPasswordIsRefusedThenWorks(t *testing.T) {
	e := newLoginEnv(t)
	e.repo.addUser(t, "late@example.com", "correct horse battery staple")
	for i := 0; i < defaultLoginMaxFailures; i++ {
		wantStatus(t, e.login(client(60+i), "late@example.com", "wrong"), http.StatusUnauthorized, fmt.Sprintf("stranger %d", i+1))
	}
	wantStatus(t, e.login(client(70), "late@example.com", "correct horse battery staple"), http.StatusTooManyRequests, "blocked")
	if len(e.repo.sessions) != 0 {
		t.Fatal("a refused login created a session")
	}
	e.now = e.now.Add(loginAccountRefill - time.Second)
	wantStatus(t, e.login(client(70), "late@example.com", "correct horse battery staple"), http.StatusTooManyRequests, "a second early")
	e.now = e.now.Add(time.Second)
	wantStatus(t, e.login(client(70), "late@example.com", "correct horse battery staple"), http.StatusOK, "once the block eases")
	if len(e.repo.sessions) != 1 {
		t.Fatalf("%d sessions, want 1", len(e.repo.sessions))
	}
	if n := e.repo.auditCount("login", "success"); n != 1 {
		t.Errorf("%d success audit rows, want 1", n)
	}
}

// Sign-up counts every request, before the body is read: an unreadable body
// is refused with 429 once the allowance is gone, where a check placed after
// decoding (or after hashing) would answer 400.
func TestSignup_LimitedPerAddressBeforeHashing(t *testing.T) {
	e := newLoginEnv(t)
	before := testutil.ToFloat64(authLimited.WithLabelValues("signup", "address"))
	for i := 0; i < defaultSignupMaxAttempts; i++ {
		wantStatus(t, e.post(e.h.RegisterUser, client(1), []byte("{not json")), http.StatusBadRequest, fmt.Sprintf("request %d", i+1))
	}
	rec := e.post(e.h.RegisterUser, client(1), []byte("{not json"))
	wantStatus(t, rec, http.StatusTooManyRequests, "6th request")
	if ra, _ := strconv.Atoi(rec.Header().Get("Retry-After")); ra < 1 || ra > int(signupRefill.Seconds()) {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	valid := mustMarshal(RegisterRequest{Email: "new@example.com", Password: "longenough1", FullName: "N", WorkspaceName: "Shop"})
	wantStatus(t, e.post(e.h.RegisterUser, client(1), valid), http.StatusTooManyRequests, "a valid sign-up from the blocked address")
	wantStatus(t, e.post(e.h.RegisterUser, client(2), valid), http.StatusCreated, "a valid sign-up from another address")
	if got := testutil.ToFloat64(authLimited.WithLabelValues("signup", "address")) - before; got != 2 {
		t.Errorf("vrsky_auth_limited_total{signup,address} rose by %v, want 2", got)
	}
	if n := e.repo.auditCount("signup", "blocked"); n != 1 {
		t.Errorf("%d audit rows for the block, want 1", n)
	}
}

func TestAuthLimitsFromEnv(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	for _, tc := range []struct {
		login, signup string
		wantOff       bool // AUTH_LOGIN_MAX_FAILURES=0 → login never refused
	}{
		{"", "", false}, {"0", "", true}, {"lots", "-1", false},
	} {
		t.Setenv("AUTH_LOGIN_MAX_FAILURES", tc.login)
		t.Setenv("AUTH_SIGNUP_MAX_ATTEMPTS", tc.signup)
		e := newLoginEnv(t)
		e.h.SetAuthLimits(AuthLimitsFromEnv(quiet))
		e.h.authLimits.now = func() time.Time { return e.now }
		refused := false
		for i := 0; i < 3*defaultLoginMaxFailures; i++ {
			if e.login(client(1), "x@example.com", "wrong").Code == http.StatusTooManyRequests {
				refused = true
				break
			}
		}
		if refused == tc.wantOff {
			t.Errorf("AUTH_LOGIN_MAX_FAILURES=%q: refused=%v, want off=%v", tc.login, refused, tc.wantOff)
		}
		// Sign-up with the default (5) when the variable is unusable.
		for i := 0; i < defaultSignupMaxAttempts; i++ {
			wantStatus(t, e.post(e.h.RegisterUser, client(1), []byte("{")), http.StatusBadRequest, "signup request")
		}
		wantStatus(t, e.post(e.h.RegisterUser, client(1), []byte("{")), http.StatusTooManyRequests, "signup past the default")
	}
}
