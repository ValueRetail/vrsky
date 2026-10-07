package managementapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// The login limit against the real statements. sqlmock cannot show that the
// known-address exception reads the audit rows the handler itself writes, nor
// that `ip_address <<= network` matches an IPv6 address to its /64 and nothing
// else. Skipped without VRSKY_TEST_POSTGRES_URL; CI sets it.
func TestLoginDB_KnownAddressComesFromTheAuditLog(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewPostgresRepository(db)

	now := time.Now()
	h := NewHandler(repo, NewValidator())
	h.authLimits.now = func() time.Time { return now }
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const email, password = "owner@example.com", "correct horse battery staple"
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	user := NewUser(email, string(hash), "Owner")
	if err := repo.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyUserEmail(ctx, user.ID); err != nil {
		t.Fatal(err)
	}

	login := func(client, pw string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(LoginRequest{Email: email, Password: pw})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		req.RemoteAddr = "10.244.1.5:51000"
		req.Header.Set("X-Forwarded-For", client+", 10.240.0.4")
		rec := httptest.NewRecorder()
		h.LoginUser(rec, req)
		return rec
	}
	must := func(rec *httptest.ResponseRecorder, want int, what string) {
		t.Helper()
		if rec.Code != want {
			t.Fatalf("%s: want %d, got %d %s", what, want, rec.Code, rec.Body.String())
		}
	}

	// The owner logs in from the office and from home (IPv6). The audit row
	// carries the client address, not a proxy's.
	must(login("203.0.113.5", password), http.StatusOK, "office login")
	must(login("2001:db8:1:2::1", password), http.StatusOK, "home login")
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT host(ip_address) FROM auth_audit_log
		WHERE email = $1 AND status = 'success' ORDER BY created_at LIMIT 1`, email).Scan(&stored); err != nil || stored != "203.0.113.5" {
		t.Fatalf("audit row address = %q (%v), want the client 203.0.113.5", stored, err)
	}

	// Strangers spend the account's attempts.
	for i := 0; i < defaultLoginMaxFailures; i++ {
		must(login(fmt.Sprintf("198.51.100.%d", 10+i), "wrong"), http.StatusUnauthorized, "stranger")
	}
	must(login("198.51.100.99", password), http.StatusTooManyRequests, "a stranger with the right password")
	must(login("203.0.113.5", password), http.StatusOK, "the office")
	must(login("2001:db8:1:2:ffff::9", password), http.StatusOK, "another address in the home /64")
	must(login("2001:db8:1:3::1", password), http.StatusTooManyRequests, "the neighbouring /64")

	// A success older than the window no longer vouches for its address.
	if _, err := db.ExecContext(ctx, `INSERT INTO auth_audit_log (email, event_type, status, ip_address, created_at)
		VALUES ($1, 'login', 'success', '192.0.2.44', NOW() - interval '31 days')`, email); err != nil {
		t.Fatal(err)
	}
	must(login("192.0.2.44", password), http.StatusTooManyRequests, "an address last seen 31 days ago")
	// And a failure from an address vouches for nothing.
	if _, err := db.ExecContext(ctx, `INSERT INTO auth_audit_log (email, event_type, status, ip_address)
		VALUES ($1, 'login', 'failed', '192.0.2.45')`, email); err != nil {
		t.Fatal(err)
	}
	must(login("192.0.2.45", password), http.StatusTooManyRequests, "an address that only ever failed")

	// Another user's success from an address vouches for THAT user only.
	otherHash, _ := bcrypt.GenerateFromPassword([]byte("pw-other-12345"), bcrypt.MinCost)
	other := NewUser("other@example.com", string(otherHash), "Other")
	if err := repo.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyUserEmail(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	otherBody, _ := json.Marshal(LoginRequest{Email: "other@example.com", Password: "pw-other-12345"})
	otherReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(otherBody))
	otherReq.RemoteAddr = "10.244.1.5:51000"
	otherReq.Header.Set("X-Forwarded-For", "192.0.2.60, 10.240.0.4")
	otherRec := httptest.NewRecorder()
	h.LoginUser(otherRec, otherReq)
	must(otherRec, http.StatusOK, "the other user from 192.0.2.60")
	must(login("192.0.2.60", password), http.StatusTooManyRequests, "the owner from an address only the other user is known at")

	var blocked int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM auth_audit_log WHERE email = $1 AND status = 'blocked'`, email).Scan(&blocked)
	if blocked != 1 {
		t.Errorf("%d 'blocked' audit rows, want 1 (one per block, not per refusal)", blocked)
	}
}
