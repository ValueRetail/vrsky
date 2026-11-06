package managementapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"github.com/ValueRetail/vrsky/pkg/auth"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// Password-reset completion, logout and email verification in the security
// log (#747), against the real table: the rows the handlers write, who they
// name, and that no token, session or password material is in them. Skipped
// without VRSKY_TEST_POSTGRES_URL; CI sets it.

const (
	eventsClientIP  = "203.0.113.9"
	eventsUserAgent = "auth-events-test/1"
)

type authEventRow struct {
	UserID, Email, Status, Reason, IP, UserAgent sql.NullString
}

type authEventsEnv struct {
	t    *testing.T
	ctx  context.Context
	db   *sql.DB
	repo *PostgresRepository
	h    *Handler
	user *User
	// secrets are values that must never appear in auth_audit_log.
	secrets []string
	seen    map[string]int // rows already checked, per event type
}

func newAuthEventsEnv(t *testing.T) *authEventsEnv {
	t.Helper()
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := NewPostgresRepository(db)
	hash, err := bcrypt.GenerateFromPassword([]byte("old password 12345"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user := NewUser("owner@example.com", string(hash), "Owner")
	if err := repo.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	return &authEventsEnv{t: t, ctx: ctx, db: db, repo: repo, h: NewHandler(repo, NewValidator()),
		user: user, seen: map[string]int{}}
}

// request comes from eventsClientIP through our own proxies, as in production.
func (e *authEventsEnv) request(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.RemoteAddr = "10.244.1.5:51000"
	req.Header.Set("X-Forwarded-For", eventsClientIP+", 10.240.0.4")
	req.Header.Set("User-Agent", eventsUserAgent)
	return req
}

func (e *authEventsEnv) resetToken(expiresAt time.Time) string {
	e.t.Helper()
	raw, hashed, err := auth.GeneratePasswordResetToken()
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.repo.CreatePasswordResetToken(e.ctx, e.user.ID, hashed, expiresAt); err != nil {
		e.t.Fatal(err)
	}
	e.secrets = append(e.secrets, raw, hashed)
	return raw
}

func (e *authEventsEnv) verifyToken(expiresAt time.Time) string {
	e.t.Helper()
	raw, hashed, err := auth.GenerateVerificationToken()
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.repo.CreateEmailVerificationToken(e.ctx, e.user.ID, hashed, expiresAt); err != nil {
		e.t.Fatal(err)
	}
	e.secrets = append(e.secrets, raw, hashed)
	return raw
}

// next is the one row eventType has gained since the last call.
func (e *authEventsEnv) next(t *testing.T, eventType string) authEventRow {
	t.Helper()
	rows, err := e.db.QueryContext(e.ctx, `SELECT user_id::text, email, status, error_reason,
		host(ip_address), user_agent FROM auth_audit_log WHERE event_type = $1 ORDER BY created_at`, eventType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var all []authEventRow
	for rows.Next() {
		var r authEventRow
		if err := rows.Scan(&r.UserID, &r.Email, &r.Status, &r.Reason, &r.IP, &r.UserAgent); err != nil {
			t.Fatal(err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got := len(all) - e.seen[eventType]; got != 1 {
		t.Fatalf("%d new %q rows, want 1", got, eventType)
	}
	e.seen[eventType] = len(all)
	return all[len(all)-1]
}

// expect checks a row: status and reason, the user and email when the token
// or session named one, and the client address and user agent always.
func (e *authEventsEnv) expect(t *testing.T, r authEventRow, status, reason string, knownUser bool) {
	t.Helper()
	if r.Status.String != status {
		t.Errorf("status %q, want %q", r.Status.String, status)
	}
	if reason == "" && r.Reason.Valid {
		t.Errorf("reason %q, want none", r.Reason.String)
	}
	if reason != "" && r.Reason.String != reason {
		t.Errorf("reason %q, want %q", r.Reason.String, reason)
	}
	if knownUser {
		if r.UserID.String != e.user.ID || r.Email.String != e.user.Email {
			t.Errorf("user %q %q, want %q %q", r.UserID.String, r.Email.String, e.user.ID, e.user.Email)
		}
	} else if r.UserID.Valid || r.Email.String != "" {
		t.Errorf("user %q %q, want none for an unknown token", r.UserID.String, r.Email.String)
	}
	if r.IP.String != eventsClientIP || r.UserAgent.String != eventsUserAgent {
		t.Errorf("client %q %q, want %q %q", r.IP.String, r.UserAgent.String, eventsClientIP, eventsUserAgent)
	}
}

// noSecrets fails if any secret appears anywhere in auth_audit_log.
func (e *authEventsEnv) noSecrets(t *testing.T) {
	t.Helper()
	var dump string
	if err := e.db.QueryRowContext(e.ctx, `SELECT coalesce(string_agg(row_to_json(a)::text, E'\n'), '')
		FROM auth_audit_log a`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	if dump == "" {
		t.Fatal("auth_audit_log is empty")
	}
	for _, s := range e.secrets {
		if s != "" && strings.Contains(dump, s) {
			t.Errorf("auth_audit_log holds a secret %q:\n%s", s, dump)
		}
	}
}

func TestAuthEventsDB_ResetPassword(t *testing.T) {
	e := newAuthEventsEnv(t)
	const newPassword = "brand new password 98765"
	e.secrets = append(e.secrets, newPassword)
	reset := func(token string) int {
		body, _ := json.Marshal(ResetPasswordRequest{Token: token, NewPassword: newPassword})
		rec := httptest.NewRecorder()
		e.h.ResetPassword(rec, e.request(http.MethodPost, "/api/v1/auth/reset-password", body))
		return rec.Code
	}

	valid := e.resetToken(time.Now().Add(time.Hour))
	expired := e.resetToken(time.Now().Add(-time.Minute))
	unknown := "not-a-real-reset-token"
	e.secrets = append(e.secrets, unknown, auth.HashToken(unknown))

	cases := []struct {
		name, token string
		code        int
		status      string
		reason      string
		knownUser   bool
	}{
		{"success", valid, http.StatusOK, "success", "", true},
		{"used token", valid, http.StatusBadRequest, "failed", "token already used", true},
		{"expired token", expired, http.StatusBadRequest, "failed", "token expired", true},
		{"unknown token", unknown, http.StatusBadRequest, "failed", "invalid token", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code := reset(c.token); code != c.code {
				t.Fatalf("reset answered %d, want %d", code, c.code)
			}
			e.expect(t, e.next(t, "password_reset"), c.status, c.reason, c.knownUser)
		})
	}

	// The stored hash of the new password is not in the log either.
	var stored string
	if err := e.db.QueryRowContext(e.ctx, `SELECT password_hash FROM users WHERE id = $1`, e.user.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(stored), []byte(newPassword)) != nil {
		t.Fatal("the reset did not store the new password")
	}
	e.secrets = append(e.secrets, stored)
	e.noSecrets(t)
}

func TestAuthEventsDB_Logout(t *testing.T) {
	e := newAuthEventsEnv(t)
	raw, hashed, err := auth.GenerateSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sess := &Session{ID: uuid.NewString(), UserID: e.user.ID, TokenHash: hashed, CreatedAt: now,
		ExpiresAt: now.Add(time.Hour), LastActivity: now, IsActive: true}
	if err := e.repo.CreateSession(e.ctx, sess); err != nil {
		t.Fatal(err)
	}
	unknown := "not-a-real-session-token"
	e.secrets = append(e.secrets, raw, hashed, sess.ID, unknown, auth.HashToken(unknown))

	logout := func(cookie, bearer string) int {
		req := e.request(http.MethodPost, "/api/v1/auth/logout", nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		e.h.LogoutUser(rec, req)
		return rec.Code
	}

	if code := logout(raw, ""); code != http.StatusOK {
		t.Fatalf("logout answered %d", code)
	}
	e.expect(t, e.next(t, "logout"), "success", "", true)
	var active bool
	if err := e.db.QueryRowContext(e.ctx, `SELECT is_active FROM sessions WHERE id = $1`, sess.ID).Scan(&active); err != nil || active {
		t.Fatalf("session still active (%v)", err)
	}

	if code := logout("", unknown); code != http.StatusOK {
		t.Fatalf("logout of an unknown session answered %d", code)
	}
	e.expect(t, e.next(t, "logout"), "failed", "unknown session", false)

	// No token at all names nobody and writes nothing.
	if code := logout("", ""); code != http.StatusBadRequest {
		t.Fatalf("logout without a token answered %d", code)
	}
	var n int
	if err := e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM auth_audit_log WHERE event_type = 'logout'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d logout rows (%v), want 2", n, err)
	}
	e.noSecrets(t)
}

func TestAuthEventsDB_VerifyEmail(t *testing.T) {
	e := newAuthEventsEnv(t)
	verify := func(token string) int {
		rec := httptest.NewRecorder()
		e.h.VerifyEmail(rec, e.request(http.MethodGet, "/api/v1/auth/verify-email?token="+url.QueryEscape(token), nil))
		return rec.Code
	}

	valid := e.verifyToken(time.Now().Add(time.Hour))
	expired := e.verifyToken(time.Now().Add(-time.Minute))
	unknown := "not-a-real-verification-token"
	e.secrets = append(e.secrets, unknown, auth.HashToken(unknown))

	cases := []struct {
		name, token string
		code        int
		status      string
		reason      string
		knownUser   bool
	}{
		{"success", valid, http.StatusFound, "success", "", true},
		{"used token", valid, http.StatusGone, "failed", "token already used", true},
		{"expired token", expired, http.StatusBadRequest, "failed", "token expired", true},
		{"unknown token", unknown, http.StatusNotFound, "failed", "invalid token", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code := verify(c.token); code != c.code {
				t.Fatalf("verify answered %d, want %d", code, c.code)
			}
			e.expect(t, e.next(t, "email_verified"), c.status, c.reason, c.knownUser)
		})
	}
	e.noSecrets(t)
}
