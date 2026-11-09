package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/auth"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// One password-reset or email-verification link used by several requests at
// once (#767): exactly one spends it, the rest are told it was already used,
// and a failed user write leaves the token usable. Against a real Postgres,
// since the bug lives between two statements. Skipped without
// VRSKY_TEST_POSTGRES_URL; CI sets it.

const (
	tokenRaceWorkers = 8
	tokenRaceRounds  = 25
)

func newTokenRaceRepo(t *testing.T) (*sql.DB, *PostgresRepository, *User) {
	t.Helper()
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	warmPool(t, db, tokenRaceWorkers)
	repo := NewPostgresRepository(db)
	user := NewUser("race@example.com", "old-hash", "Race")
	if err := repo.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	return db, repo, user
}

// raceTokenUse runs use once per worker, all released together, and returns
// each worker's error.
func raceTokenUse(workers int, use func(i int) error) []error {
	start := make(chan struct{})
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = use(i)
		}()
	}
	close(start)
	wg.Wait()
	return errs
}

// oneWinner fails unless exactly one error is nil and every other one is
// ErrTokenUsed, and returns the winner's index.
func oneWinner(t *testing.T, round int, errs []error) int {
	t.Helper()
	winner := -1
	for i, err := range errs {
		switch {
		case err == nil && winner >= 0:
			t.Fatalf("round %d: workers %d and %d both spent the token", round, winner, i)
		case err == nil:
			winner = i
		case !errors.Is(err, auth.ErrTokenUsed):
			t.Fatalf("round %d: worker %d got %v, want %v", round, i, err, auth.ErrTokenUsed)
		}
	}
	if winner < 0 {
		t.Fatalf("round %d: no worker spent the token: %v", round, errs)
	}
	return winner
}

func TestTokenRaceDB_PasswordResetSpentOnce(t *testing.T) {
	db, repo, user := newTokenRaceRepo(t)
	ctx := context.Background()
	for round := range tokenRaceRounds {
		_, hashed, err := auth.GeneratePasswordResetToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CreatePasswordResetToken(ctx, user.ID, hashed, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		hashes := make([]string, tokenRaceWorkers)
		for i := range hashes {
			hashes[i] = fmt.Sprintf("hash-round-%d-worker-%d", round, i)
		}
		errs := raceTokenUse(tokenRaceWorkers, func(i int) error {
			return repo.UsePasswordResetToken(ctx, hashed, hashes[i])
		})
		winner := oneWinner(t, round, errs)

		// The password is the winner's: no losing request wrote one.
		var stored string
		if err := db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = $1`, user.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != hashes[winner] {
			t.Fatalf("round %d: stored password %q, want the winner's %q", round, stored, hashes[winner])
		}
	}
}

func TestTokenRaceDB_EmailVerificationSpentOnce(t *testing.T) {
	_, repo, user := newTokenRaceRepo(t)
	ctx := context.Background()
	for round := range tokenRaceRounds {
		_, hashed, err := auth.GenerateVerificationToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CreateEmailVerificationToken(ctx, user.ID, hashed, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		errs := raceTokenUse(tokenRaceWorkers, func(int) error {
			return repo.UseEmailVerificationToken(ctx, hashed)
		})
		oneWinner(t, round, errs)
	}
	got, err := repo.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.EmailVerified {
		t.Fatal("the user's email is not verified")
	}
}

// refuseUserUpdates makes every UPDATE on users fail until the test drops it.
func refuseUserUpdates(t *testing.T, db *sql.DB) (drop func()) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		CREATE OR REPLACE FUNCTION test_refuse_user_update() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'user update refused by test'; END $$;
		CREATE TRIGGER test_refuse_user_update BEFORE UPDATE ON users
			FOR EACH ROW EXECUTE FUNCTION test_refuse_user_update();`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := db.ExecContext(ctx, `DROP TRIGGER test_refuse_user_update ON users`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTokenRaceDB_FailedUserWriteKeepsTheToken(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		create func(repo *PostgresRepository, userID, hashed string) error
		use    func(repo *PostgresRepository, hashed string) error
		usedAt string // query for the token's used_at by hash
	}{
		{
			name: "password reset",
			create: func(repo *PostgresRepository, userID, hashed string) error {
				return repo.CreatePasswordResetToken(ctx, userID, hashed, time.Now().Add(time.Hour))
			},
			use: func(repo *PostgresRepository, hashed string) error {
				return repo.UsePasswordResetToken(ctx, hashed, "new-hash")
			},
			usedAt: `SELECT used_at FROM password_reset_tokens WHERE token_hash = $1`,
		},
		{
			name: "email verification",
			create: func(repo *PostgresRepository, userID, hashed string) error {
				return repo.CreateEmailVerificationToken(ctx, userID, hashed, time.Now().Add(time.Hour))
			},
			use: func(repo *PostgresRepository, hashed string) error {
				return repo.UseEmailVerificationToken(ctx, hashed)
			},
			usedAt: `SELECT used_at FROM email_verification_tokens WHERE token_hash = $1`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, repo, user := newTokenRaceRepo(t)
			hashed := auth.HashToken("token-" + c.name)
			if err := c.create(repo, user.ID, hashed); err != nil {
				t.Fatal(err)
			}

			drop := refuseUserUpdates(t, db)
			err := c.use(repo, hashed)
			if err == nil || errors.Is(err, auth.ErrTokenUsed) || errors.Is(err, auth.ErrTokenExpired) ||
				errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("use with the user write refused: %v, want a database error", err)
			}
			var usedAt sql.NullTime
			if err := db.QueryRowContext(ctx, c.usedAt, hashed).Scan(&usedAt); err != nil {
				t.Fatal(err)
			}
			if usedAt.Valid {
				t.Fatalf("the failed use spent the token (used_at %v)", usedAt.Time)
			}
			drop()

			if err := c.use(repo, hashed); err != nil {
				t.Fatalf("retry after the failure: %v", err)
			}
			if err := c.use(repo, hashed); !errors.Is(err, auth.ErrTokenUsed) {
				t.Fatalf("second use: %v, want %v", err, auth.ErrTokenUsed)
			}
		})
	}
}

// auditCounts is how many rows eventType has per status and reason.
func auditCounts(t *testing.T, e *authEventsEnv, eventType string) map[string]int {
	t.Helper()
	rows, err := e.db.QueryContext(e.ctx, `SELECT status || '/' || coalesce(error_reason, ''), count(*)
		FROM auth_audit_log WHERE event_type = $1 GROUP BY 1`, eventType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			t.Fatal(err)
		}
		out[key] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// codeCounts tallies HTTP status codes.
func codeCounts(codes []int) map[int]int {
	out := map[int]int{}
	for _, c := range codes {
		out[c]++
	}
	return out
}

// warmPool opens n connections and leaves them idle in the pool, so a race
// that follows is not spread out by connection setup.
func warmPool(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	db.SetMaxIdleConns(n)
	for _, err := range raceTokenUse(n, func(int) error {
		_, err := db.Exec(`SELECT pg_sleep(0.05)`)
		return err
	}) {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// The handlers answer and log as for sequential requests: one success, and
// for every other request the "already used" answer and a failed row (#766).
func TestTokenRaceDB_HandlersLogOneSuccess(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		workers   int
		rounds    int
		token     func(e *authEventsEnv) string
		send      func(e *authEventsEnv, raw string, i int) int
		won, lost int // HTTP answers
	}{
		{
			// bcrypt at cost 12 per request: fewer workers and rounds.
			name: "password reset", eventType: "password_reset", workers: 4, rounds: 3,
			token: func(e *authEventsEnv) string { return e.resetToken(time.Now().Add(time.Hour)) },
			send: func(e *authEventsEnv, raw string, i int) int {
				body, _ := json.Marshal(ResetPasswordRequest{Token: raw, NewPassword: fmt.Sprintf("brand new password %d", i)})
				rec := httptest.NewRecorder()
				e.h.ResetPassword(rec, e.request(http.MethodPost, "/api/v1/auth/reset-password", body))
				return rec.Code
			},
			won: http.StatusOK, lost: http.StatusBadRequest,
		},
		{
			name: "email verification", eventType: "email_verified", workers: tokenRaceWorkers, rounds: tokenRaceRounds,
			token: func(e *authEventsEnv) string { return e.verifyToken(time.Now().Add(time.Hour)) },
			send: func(e *authEventsEnv, raw string, _ int) int {
				rec := httptest.NewRecorder()
				e.h.VerifyEmail(rec, e.request(http.MethodGet, "/api/v1/auth/verify-email?token="+url.QueryEscape(raw), nil))
				return rec.Code
			},
			won: http.StatusFound, lost: http.StatusGone,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAuthEventsEnv(t)
			warmPool(t, e.db, c.workers*2)
			for round := range c.rounds {
				raw := c.token(e)
				codes := make([]int, c.workers)
				raceTokenUse(c.workers, func(i int) error {
					codes[i] = c.send(e, raw, i)
					return nil
				})
				want := map[int]int{c.won: 1, c.lost: c.workers - 1}
				if got := codeCounts(codes); fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("round %d: answers %v, want %v", round, got, want)
				}
				// Rows so far: one success per round, the rest "already used".
				wantRows := map[string]int{"success/": round + 1, "failed/token already used": (round + 1) * (c.workers - 1)}
				if got := auditCounts(t, e, c.eventType); fmt.Sprint(got) != fmt.Sprint(wantRows) {
					t.Fatalf("round %d: %s rows %v, want %v", round, c.eventType, got, wantRows)
				}
			}
		})
	}
}
