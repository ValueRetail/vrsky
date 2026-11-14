package sdk

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

// RestoreRunningErr against a database whose list query fails (#725): a
// failure that passes is retried on the policy's schedule and then restores
// everything; one that persists is returned as ErrRestoreList, not as
// "nothing running"; ctx ending stops the retries; a row error halfway
// through the list counts as a failed list, not a short one.
func TestRestoreRunningListFailure(t *testing.T) {
	const listQuery = `SELECT id::text, tenant_id FROM connections`
	down := &pq.Error{Code: "57P03", Message: "the database system is starting up"}
	badQuery := &pq.Error{Code: "42501", Message: "permission denied for table connections"}
	rows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "tenant_id"}).AddRow("c1", "t1").AddRow("c2", "t2")
	}
	failing := func(m sqlmock.Sqlmock) {
		for i := 0; i < 200; i++ {
			m.ExpectQuery(listQuery).WillReturnError(down)
		}
	}
	cases := []struct {
		name      string
		expect    func(m sqlmock.Sqlmock)
		giveUp    time.Duration
		cancel    bool // ctx is cancelled while it retries
		wantErr   bool
		wantBad   bool // the error is badQuery, not down
		wantStart []string
	}{
		{
			name: "fails twice then lists",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(listQuery).WillReturnError(down)
				m.ExpectQuery(listQuery).WillReturnError(down)
				m.ExpectQuery(listQuery).WillReturnRows(rows())
			},
			wantStart: []string{"c1@t1", "c2@t2"},
		},
		{
			name: "row error halfway is retried",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(listQuery).WillReturnRows(rows().RowError(1, down))
				m.ExpectQuery(listQuery).WillReturnRows(rows())
			},
			wantStart: []string{"c1@t1", "c2@t2"},
		},
		{
			name:    "keeps failing until the policy gives up",
			expect:  failing,
			giveUp:  60 * time.Millisecond,
			wantErr: true,
		},
		{
			name:    "context ends while retrying",
			expect:  failing,
			giveUp:  -1, // never: only ctx ends it
			cancel:  true,
			wantErr: true,
		},
		{
			name: "an error other than the database down is not retried",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(listQuery).WillReturnError(badQuery)
				m.ExpectQuery(listQuery).WillReturnRows(rows()) // must stay unused
			},
			wantErr: true,
			wantBad: true,
		},
		{
			name: "empty list is no error",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(listQuery).WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id"}))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			tc.expect(mock)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
			policy := RetryPolicy{Base: 5 * time.Millisecond, Max: 10 * time.Millisecond, GiveUp: tc.giveUp}
			var (
				got  []string
				n    int
				rerr error
			)
			done := make(chan struct{})
			go func() {
				defer close(done)
				n, rerr = RestoreRunningErr(ctx, db, quiet, "http", policy, func(_ context.Context, id, tenant string) {
					got = append(got, id+"@"+tenant)
				})
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("RestoreRunningErr did not return")
			}
			if tc.wantErr {
				// After a cancel the last query may have failed with the
				// context's error rather than down.
				want := error(down)
				if tc.wantBad {
					want = badQuery
				}
				if !errors.Is(rerr, ErrRestoreList) || (!tc.cancel && !errors.Is(rerr, want)) {
					t.Fatalf("err = %v, want ErrRestoreList wrapping the query error", rerr)
				}
			} else if rerr != nil {
				t.Fatalf("err = %v", rerr)
			}
			sort.Strings(got)
			if n != len(tc.wantStart) || len(got) != len(tc.wantStart) {
				t.Fatalf("restored %d: %v, want %v", n, got, tc.wantStart)
			}
			for i := range got {
				if got[i] != tc.wantStart[i] {
					t.Fatalf("restored %v, want %v", got, tc.wantStart)
				}
			}
		})
	}
}
