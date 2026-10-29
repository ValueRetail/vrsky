package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// Retrying failed starts (#503) against a real database: the connection
// lookup, the status check and the "error" write go to Postgres, Front
// Systems is the fake from unregister_test.go. Skipped without
// VRSKY_TEST_POSTGRES_URL; CI sets it.

type retryPGEnv struct {
	t  *testing.T
	db *sql.DB
	fs *fakeFS
	c  *frontSystemsConsumer
}

func newRetryPGEnv(t *testing.T) *retryPGEnv {
	t.Helper()
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fs := newFakeFS(t)
	c := &frontSystemsConsumer{
		db:         db,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: fs.srv.Client(),
		retryBase:  10 * time.Millisecond,
		retryMax:   40 * time.Millisecond,
		publish:    func(context.Context, *envelope.Envelope) error { return nil },
	}
	c.resolveTenant = c.getConnectionTenant
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.Stop(ctx)
	})
	return &retryPGEnv{t: t, db: db, fs: fs, c: c}
}

// connection inserts a Front Systems connection with the given status and
// returns it.
func (e *retryPGEnv) connection(status string) urConn {
	e.t.Helper()
	var owner, tenant string
	if err := e.db.QueryRow(`INSERT INTO users (email, password_hash, status)
		VALUES ('fs-retry-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		e.t.Fatal(err)
	}
	if err := e.db.QueryRow(`INSERT INTO tenants (name, slug, owner_id)
		VALUES ('fs-retry', 'fs-retry-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(&tenant); err != nil {
		e.t.Fatal(err)
	}
	u := urConn{id: uuid.New().String(), tenant: tenant}
	u.events = []string{"SaleCreated"}
	if _, err := e.db.Exec(`INSERT INTO connections (id, tenant_id, name, status, nodes, edges)
		VALUES ($1, $2, $3, $4, $5::jsonb, '[]'::jsonb)`, u.id, u.tenant, "fs-"+u.id, status, u.nodes(e.fs.srv.URL)); err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *retryPGEnv) row(u urConn) (status string, lastError sql.NullString) {
	e.t.Helper()
	if err := e.db.QueryRow(`SELECT status, last_error FROM connections WHERE id = $1 AND tenant_id = $2`,
		u.id, u.tenant).Scan(&status, &lastError); err != nil {
		e.t.Fatal(err)
	}
	return status, lastError
}

func TestFrontSystemsRetryPG(t *testing.T) {
	t.Run("transient failures, then running", func(t *testing.T) {
		e := newRetryPGEnv(t)
		u := e.connection("running")
		e.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusServiceUnavailable, 2 })
		e.c.startConnection(context.Background(), u.id, u.tenant)
		harness.Eventually(t, urWait, "the retried start to activate", func() bool {
			_, ok := activeTenant(e.c, u.id)
			return ok
		})
		if status, lastErr := e.row(u); status != "running" || lastErr.Valid {
			t.Errorf("row = %q, %v; want running with no last_error", status, lastErr)
		}
		if code := postEvent(e.c, u.id, `{"event":"SaleCreated"}`); code != http.StatusAccepted {
			t.Errorf("event once active: status %d, want 202", code)
		}
	})

	t.Run("permanent failure marks the row error", func(t *testing.T) {
		e := newRetryPGEnv(t)
		u := e.connection("running")
		e.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusUnauthorized, 1000 })
		e.c.startConnection(context.Background(), u.id, u.tenant)
		status, lastErr := e.row(u)
		if status != "error" {
			t.Errorf("status = %q, want error", status)
		}
		if !strings.Contains(lastErr.String, "HTTP 401") || strings.Contains(lastErr.String, "body-must-not-reach") {
			t.Errorf("last_error = %q, want the status and no response body", lastErr.String)
		}
		if code := postEvent(e.c, u.id, `{}`); code != http.StatusGone {
			t.Errorf("event: status %d, want 410", code)
		}
	})

	t.Run("a row already stopped keeps stopped", func(t *testing.T) {
		// A stop wrote "stopped" before the failing start finished; the start's
		// "error" must not land on top of it.
		e := newRetryPGEnv(t)
		u := e.connection("stopped")
		e.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusForbidden, 1000 })
		e.c.startConnection(context.Background(), u.id, u.tenant)
		if status, lastErr := e.row(u); status != "stopped" || lastErr.Valid {
			t.Errorf("row = %q, %v; want stopped with no last_error", status, lastErr)
		}
	})

	t.Run("a row stopped during the backoff ends the retries", func(t *testing.T) {
		// The stop command never arrived here; the row says stopped.
		e := newRetryPGEnv(t)
		u := e.connection("running")
		e.fs.set(func(fs *fakeFS) { fs.postErr, fs.postErrLeft = http.StatusServiceUnavailable, 1000 })
		e.c.startConnection(context.Background(), u.id, u.tenant)
		if _, err := e.db.Exec(`UPDATE connections SET status = 'stopped' WHERE id = $1 AND tenant_id = $2`, u.id, u.tenant); err != nil {
			t.Fatal(err)
		}
		harness.Eventually(t, urWait, "the retries to end", func() bool { return retryLoopsFor(e.c) == 0 })
		e.fs.mu.Lock()
		posts := e.fs.posts
		e.fs.mu.Unlock()
		time.Sleep(10 * e.c.retryMax)
		e.fs.mu.Lock()
		after := e.fs.posts
		e.fs.mu.Unlock()
		if after != posts {
			t.Errorf("creates after the row was stopped: %d, want none", after-posts)
		}
		if status, lastErr := e.row(u); status != "stopped" || lastErr.Valid {
			t.Errorf("row = %q, %v; want stopped with no last_error", status, lastErr)
		}
		if code := postEvent(e.c, u.id, `{}`); code != http.StatusGone {
			t.Errorf("event: status %d, want 410", code)
		}
	})
}
