package managementapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ValueRetail/vrsky/pkg/auth"
	"github.com/ValueRetail/vrsky/pkg/notify"
)

// The password-reset round trip and the change-password limit
// (plans/auth-leftovers.md), on the login test environment.

type sentMail struct{ to, subject, body string }

// resetRepo adds what the reset flow needs to loginRepo: stored reset
// tokens, a password that actually changes, sessions that validate.
type resetRepo struct {
	*loginRepo
	tokens      map[string]*PasswordResetToken // by hash
	invalidated []string                       // user ids
	updated     *User                          // the user UpdateUserPassword last changed
}

func (r *resetRepo) CreatePasswordResetToken(_ context.Context, userID, tokenHash string, expiresAt time.Time) error {
	r.tokens[tokenHash] = &PasswordResetToken{UserID: userID, TokenHash: tokenHash, ExpiresAt: expiresAt, CreatedAt: time.Now()}
	return nil
}
func (r *resetRepo) GetPasswordResetToken(_ context.Context, tokenHash string) (*PasswordResetToken, error) {
	if t, ok := r.tokens[tokenHash]; ok {
		return t, nil
	}
	return nil, auth.ErrInvalidToken
}
func (r *resetRepo) UsePasswordResetToken(ctx context.Context, tokenHash, newPasswordHash string) error {
	t, err := r.GetPasswordResetToken(ctx, tokenHash)
	if err != nil {
		return err
	}
	if t.UsedAt != nil {
		return auth.ErrTokenUsed
	}
	now := time.Now()
	t.UsedAt = &now
	// As the real repository does: new password, token spent, every session out.
	if err := r.UpdateUserPassword(ctx, t.UserID, newPasswordHash); err != nil {
		return err
	}
	return r.InvalidateAllUserSessions(ctx, t.UserID)
}
func (r *resetRepo) UpdateUserPassword(_ context.Context, userID, hash string) error {
	for _, u := range r.users {
		if u.ID == userID {
			u.PasswordHash = hash
			r.updated = u
			return nil
		}
	}
	return errors.New("no such user")
}
func (r *resetRepo) InvalidateAllUserSessions(_ context.Context, userID string) error {
	r.invalidated = append(r.invalidated, userID)
	return nil
}
func (r *resetRepo) ValidateSession(_ context.Context, tokenHash string) (*Session, *User, error) {
	for _, s := range r.sessions {
		if s.TokenHash != tokenHash {
			continue
		}
		for _, u := range r.users {
			if u.ID == s.UserID {
				return s, u, nil
			}
		}
	}
	return nil, nil, &NotFoundError{ResourceType: "Session", ResourceID: tokenHash}
}

type resetEnv struct {
	*loginEnv
	t       *testing.T
	repo    *resetRepo
	mails   []sentMail
	changes int // successful change-password requests so far
}

// newResetEnv is a loginEnv that can send mail to https://vrsky.example.
func newResetEnv(t *testing.T) *resetEnv {
	t.Helper()
	base := newLoginEnv(t)
	e := &resetEnv{loginEnv: base, t: t, repo: &resetRepo{loginRepo: base.repo, tokens: map[string]*PasswordResetToken{}}}
	e.h.repo = e.repo
	e.h.mailer = notify.MailerFunc(func(_ context.Context, to, subject, body string) error {
		e.mails = append(e.mails, sentMail{to, subject, body})
		return nil
	})
	if err := e.h.SetPublicURL("https://vrsky.example"); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *resetEnv) forgot(client, email string) *httptest.ResponseRecorder {
	return e.post(e.h.ForgotPassword, client, mustMarshal(ForgotPasswordRequest{Email: email}))
}
func (e *resetEnv) reset(token, password string) *httptest.ResponseRecorder {
	return e.post(e.h.ResetPassword, client(1), mustMarshal(ResetPasswordRequest{Token: token, NewPassword: password}))
}
func (e *resetEnv) change(sessionToken, current, next string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password",
		strings.NewReader(string(mustMarshal(ChangePasswordRequest{CurrentPassword: current, NewPassword: next}))))
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.h.ChangePassword(rec, req)
	if rec.Code == http.StatusOK {
		e.changes++
		e.cheapen(next)
	}
	return rec
}

// cheapen re-stores a password ChangePassword just changed at bcrypt.MinCost.
// ChangePassword hashes at cost 12, and every later login and change verifies
// against that hash, seconds each under -race. The first change in a test
// keeps the hash the handler stored, so the next login still checks that the
// real path stored a hash of the new password.
func (e *resetEnv) cheapen(password string) {
	e.t.Helper()
	u := e.repo.updated
	if e.changes == 1 || u == nil {
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		e.t.Fatal(err)
	}
	u.PasswordHash = string(hash)
}

var linkRe = regexp.MustCompile(`https?://\S+`)

func TestForgotPassword_SendsALinkThatResets(t *testing.T) {
	e := newResetEnv(t)
	u := e.repo.addUser(t, "owner@example.com", "correct horse battery staple")
	wantStatus(t, e.forgot(client(1), "Owner@Example.com"), http.StatusOK, "forgot")
	if len(e.mails) != 1 || e.mails[0].to != "owner@example.com" || !strings.Contains(e.mails[0].subject, "Reset") {
		t.Fatalf("mails = %+v", e.mails)
	}
	link := linkRe.FindString(e.mails[0].body)
	parsed, err := url.Parse(link)
	if err != nil || parsed.Scheme+"://"+parsed.Host != "https://vrsky.example" || parsed.Path != "/reset-password" {
		t.Fatalf("link %q (%v), want https://vrsky.example/reset-password?token=…", link, err)
	}
	token := parsed.Query().Get("token")
	if token == "" || e.repo.tokens[auth.HashToken(token)] == nil {
		t.Fatalf("the link's token %q is not the one stored (hashed)", token)
	}

	wantStatus(t, e.reset(token, "a brand new passphrase"), http.StatusOK, "reset")
	if err := auth.VerifyPassword(u.PasswordHash, "a brand new passphrase"); err != nil {
		t.Fatal("the password did not change")
	}
	if len(e.repo.invalidated) != 1 || e.repo.invalidated[0] != u.ID {
		t.Fatalf("sessions invalidated for %v, want %s", e.repo.invalidated, u.ID)
	}
	wantStatus(t, e.reset(token, "yet another passphrase"), http.StatusBadRequest, "reused link")
	wantStatus(t, e.login(client(2), "owner@example.com", "a brand new passphrase"), http.StatusOK, "login with the new password")
}

func TestForgotPassword_UnknownEmailAnswersTheSameAndSendsNothing(t *testing.T) {
	e := newResetEnv(t)
	e.repo.addUser(t, "owner@example.com", "correct horse battery staple")
	known, unknown := e.forgot(client(1), "owner@example.com"), e.forgot(client(1), "nobody@example.com")
	if known.Code != http.StatusOK || unknown.Code != known.Code || unknown.Body.String() != known.Body.String() {
		t.Fatalf("known: %d %s\nunknown: %d %s", known.Code, known.Body.String(), unknown.Code, unknown.Body.String())
	}
	if len(e.mails) != 1 || e.mails[0].to != "owner@example.com" {
		t.Fatalf("mails = %+v, want exactly one, to the account that exists", e.mails)
	}
	if len(e.repo.tokens) != 1 {
		t.Fatalf("%d reset tokens stored, want 1", len(e.repo.tokens))
	}
}

func TestForgotPassword_WithoutMailIs503ForEveryone(t *testing.T) {
	for _, missing := range []string{"mailer", "public url"} {
		e := newResetEnv(t)
		e.repo.addUser(t, "owner@example.com", "correct horse battery staple")
		if missing == "mailer" {
			e.h.mailer = nil
		} else {
			_ = e.h.SetPublicURL("")
		}
		known, unknown := e.forgot(client(1), "owner@example.com"), e.forgot(client(1), "nobody@example.com")
		if known.Code != http.StatusServiceUnavailable || unknown.Code != known.Code || unknown.Body.String() != known.Body.String() {
			t.Fatalf("no %s: known %d %s / unknown %d %s", missing, known.Code, known.Body.String(), unknown.Code, unknown.Body.String())
		}
		if !strings.Contains(known.Body.String(), "not available") {
			t.Fatalf("no %s: body %s should say reset is not available", missing, known.Body.String())
		}
		if e.repo.lookups != 0 || len(e.repo.tokens) != 0 || len(e.mails) != 0 {
			t.Fatalf("no %s: %d lookups, %d tokens, %d mails; the answer must be decided before any lookup", missing, e.repo.lookups, len(e.repo.tokens), len(e.mails))
		}
	}
}

func TestForgotPassword_LinkUsesPublicURLNotHost(t *testing.T) {
	e := newResetEnv(t)
	e.repo.addUser(t, "owner@example.com", "correct horse battery staple")
	req := httptest.NewRequest(http.MethodPost, "https://evil.example/api/v1/auth/forgot-password",
		strings.NewReader(string(mustMarshal(ForgotPasswordRequest{Email: "owner@example.com"}))))
	req.Host = "evil.example"
	req.Header.Set("X-Forwarded-Host", "evil.example")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.h.ForgotPassword(rec, req)
	wantStatus(t, rec, http.StatusOK, "forgot")
	if len(e.mails) != 1 || strings.Contains(e.mails[0].body, "evil.example") || !strings.Contains(e.mails[0].body, "https://vrsky.example/reset-password?token=") {
		t.Fatalf("mail body:\n%s", e.mails[0].body)
	}
}

func TestForgotPassword_IsLimitedPerAddressAndPerEmail(t *testing.T) {
	e := newResetEnv(t)
	e.repo.addUser(t, "owner@example.com", "correct horse battery staple")
	// Per email: three mails, then 429 from any address.
	for i := 0; i < forgotEmailMax; i++ {
		wantStatus(t, e.forgot(client(10+i), "owner@example.com"), http.StatusOK, "request")
	}
	wantStatus(t, e.forgot(client(20), "owner@example.com"), http.StatusTooManyRequests, "fourth for the same email")
	if len(e.mails) != forgotEmailMax {
		t.Fatalf("%d mails sent, want %d", len(e.mails), forgotEmailMax)
	}
	// Per address: the sign-up allowance, whatever the emails.
	for i := 0; i < defaultSignupMaxAttempts; i++ {
		e.forgot(client(1), "x"+string(rune('a'+i))+"@example.com")
	}
	wantStatus(t, e.forgot(client(1), "another@example.com"), http.StatusTooManyRequests, "past the address allowance")
	wantStatus(t, e.forgot(client(2), "another@example.com"), http.StatusOK, "a different address")
}

func TestSetPublicURL_AcceptsOriginsOnly(t *testing.T) {
	h := NewHandler(NewMockRepository(), NewValidator())
	for _, ok := range []string{"https://vrsky.example", "https://vrsky.example/", " http://localhost:5173 "} {
		if err := h.SetPublicURL(ok); err != nil || strings.HasSuffix(h.publicURL, "/") || h.publicURL == "" {
			t.Errorf("%q: err %v, stored %q", ok, err, h.publicURL)
		}
	}
	for _, bad := range []string{"vrsky.example", "ftp://x", "https://user:pw@x", "https://vrsky.example/app", "https://vrsky.example?x=1"} {
		if err := h.SetPublicURL(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	if err := h.SetPublicURL(""); err != nil || h.publicURL != "" {
		t.Errorf("empty: err %v, stored %q", err, h.publicURL)
	}
}

// session logs in and returns the raw session token.
func (e *resetEnv) session(t *testing.T, email, password string) string {
	t.Helper()
	rec := e.login(client(1), email, password)
	wantStatus(t, rec, http.StatusOK, "login")
	var resp LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.SessionToken == "" {
		t.Fatalf("login response: %v %s", err, rec.Body.String())
	}
	return resp.SessionToken
}

func TestChangePassword_BlocksAfterTenWrongCurrentPasswords(t *testing.T) {
	e := newResetEnv(t)
	e.repo.addUser(t, "owner@example.com", "correct horse battery staple")
	tok := e.session(t, "owner@example.com", "correct horse battery staple")
	for i := 0; i < defaultLoginMaxFailures; i++ {
		wantStatus(t, e.change(tok, "wrong guess", "a brand new passphrase"), http.StatusUnauthorized, "wrong current password")
	}
	blocked := e.change(tok, "correct horse battery staple", "a brand new passphrase")
	wantStatus(t, blocked, http.StatusTooManyRequests, "blocked, even with the right password")
	if blocked.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	// One attempt back per refill interval.
	e.now = e.now.Add(loginAccountRefill)
	wantStatus(t, e.change(tok, "correct horse battery staple", "a brand new passphrase"), http.StatusOK, "after the refill")
}

func TestChangePassword_SuccessRefunds(t *testing.T) {
	e := newResetEnv(t)
	e.repo.addUser(t, "owner@example.com", "pw number one!!")
	// Many correct changes in a row cost nothing: the allowance is whole after.
	current := "pw number one!!"
	for i := 0; i < 2*defaultLoginMaxFailures; i++ {
		next := fmt.Sprintf("pw number %d!!", i+2)
		wantStatus(t, e.change(e.session(t, "owner@example.com", current), current, next), http.StatusOK, "right current password")
		current = next
	}
	tok := e.session(t, "owner@example.com", current)
	for i := 0; i < defaultLoginMaxFailures; i++ {
		wantStatus(t, e.change(tok, "wrong", "pw number zero!!"), http.StatusUnauthorized, "wrong current password")
	}
	wantStatus(t, e.change(tok, "wrong", "pw number zero!!"), http.StatusTooManyRequests, "past the allowance")
}
