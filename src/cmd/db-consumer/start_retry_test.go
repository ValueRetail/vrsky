package main

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// #725: a start whose connection lookup fails because the management
// database does not answer is retried in the background and comes up once
// the database answers; a lookup that fails for another reason is not
// retried; a stop while the retry waits ends it, and nothing starts after.
func TestStartRetry_LookupFailure(t *testing.T) {
	dbDown := &pq.Error{Code: "57P03", Message: "the database system is starting up"}
	badQuery := &pq.Error{Code: "42P01", Message: "relation does not exist"}
	cases := []struct {
		name       string
		lookupErr  error
		stop       bool // a stop command lands while the retry waits
		wantActive int
	}{
		{"database down: retried and started", dbDown, false, 1},
		{"other database error: not retried", badQuery, false, 0},
		{"stop while the retry waits: nothing starts", dbDown, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSRFixture(t)
			wait := 20 * time.Millisecond
			if tc.stop {
				wait = 300 * time.Millisecond
			}
			f.c.retryPolicy = sdk.RetryPolicy{Base: wait, Max: wait}
			f.expectLookup(tc.lookupErr)
			// What a retry would find: the row still running, the lookup
			// working. Unmet when no retry may run.
			f.mock.ExpectQuery("SELECT status FROM connections").WithArgs(srConn, srTenant).
				WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("running"))
			f.expectLookup(nil)
			f.expectRunning()
			f.src.ExpectQuery("SELECT \\* FROM widgets").WillReturnRows(sqlmock.NewRows([]string{"id"}))

			f.c.startConnection(context.Background(), srConn, srTenant)
			if n := f.activeCount(); n != 0 {
				t.Fatalf("active after the failed start = %d, want 0", n)
			}
			if tc.stop {
				f.c.handleStopCommand(srStopMsg())
			}
			if tc.wantActive == 1 {
				harness.Eventually(t, srWait, "the retry to start the connection", func() bool { return f.activeCount() == 1 })
				if err := f.mock.ExpectationsWereMet(); err != nil {
					t.Errorf("management db: %v", err)
				}
				return
			}
			time.Sleep(4 * wait)
			if n := f.activeCount(); n != 0 {
				t.Fatalf("active = %d, want 0: a start that must not be retried came up", n)
			}
			if f.mock.ExpectationsWereMet() == nil {
				t.Error("the retry's queries ran, but no retry may run here")
			}
		})
	}
}
