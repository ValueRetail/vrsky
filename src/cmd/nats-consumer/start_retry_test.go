package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// #725: the stored-credential read at start. A database that does not
// answer is retried, and the connection comes up once it does, without
// ever going to status error. A secret that is not there is no reason to
// retry: the start ends in status error, as before, and is not tried again.
func TestStart_SecretReadFailure(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testKey)
	ext := startExternal(t, func(o *natsserver.Options) { o.Username, o.Password = "rh", tPassword })
	_, js := ext.admin(nats.UserInfo("rh", tPassword))
	addStream(t, js)
	addDurable(t, js, 30*time.Second) // a restore binds an existing durable

	ciphertext, err := crypto.Encrypt(tPassword, testKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	const secretID = "6f0b9d34-0f5c-4c8e-9a52-1a2b3c4d5e6f"
	nodes, _ := json.Marshal([]map[string]any{{
		"id": "src", "type": "consumer",
		"config": map[string]any{"type": "nats", "nats": map[string]any{
			"servers": []string{ext.url()}, "auth_type": "user_password", "username": "rh",
			"password_secret_id": secretID, "stream": tStream, "durable": tDur, "filter_subject": tSubj,
		}},
	}})

	cases := []struct {
		name      string
		secretErr error
		retried   bool // the start is retried and comes up; otherwise status error
	}{
		{"database down: retried and started", &pq.Error{Code: "57P03", Message: "the database system is starting up"}, true},
		{"secret missing: status error, not retried", sql.ErrNoRows, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(tConn, tTenant).
				WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow(nodes))
			mock.ExpectQuery("SELECT ciphertext FROM secrets").WithArgs(secretID, tTenant).
				WillReturnError(tc.secretErr)
			if tc.retried {
				// The retry: the row still running, then a lookup that works.
				mock.ExpectQuery("SELECT status FROM connections").WithArgs(tConn, tTenant).
					WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("running"))
				mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(tConn, tTenant).
					WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow(nodes))
				mock.ExpectQuery("SELECT ciphertext FROM secrets").WithArgs(secretID, tTenant).
					WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(ciphertext))
				mock.ExpectExec(`UPDATE connections SET status = \$1, started_at = NOW\(\), updated_at = NOW\(\), last_error = NULL`).
					WithArgs("running", tConn, tTenant).WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectExec(`UPDATE connections SET status = \$1, last_error = \$2`).
					WithArgs("error", sqlmock.AnyArg(), tConn, tTenant).WillReturnResult(sqlmock.NewResult(0, 1))
			}

			c := newTestConsumer(nil)
			c.db = db
			c.retryPolicy = sdk.RetryPolicy{Base: 20 * time.Millisecond, Max: 40 * time.Millisecond}
			t.Cleanup(func() {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				_ = c.Stop(ctx)
			})
			active := func() bool {
				c.mu.RLock()
				defer c.mu.RUnlock()
				_, ok := c.active[tConn]
				return ok
			}

			c.restoreConnection(context.Background(), tConn, tTenant)
			if tc.retried {
				if active() {
					t.Fatal("active after the failed first attempt")
				}
				harness.Eventually(t, wait, "the retry to start the connection", active)
				harness.Eventually(t, wait, "the running write", func() bool { return mock.ExpectationsWereMet() == nil })
				return
			}
			harness.Eventually(t, wait, "the error write", func() bool { return mock.ExpectationsWereMet() == nil })
			// Several retry delays later: no retry read the row, nothing started.
			time.Sleep(10 * c.retryPolicy.Max)
			if active() {
				t.Fatal("a start whose secret is missing came up")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
