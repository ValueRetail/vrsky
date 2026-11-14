package sdk_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// The row is the record a retry checks before each attempt (#725): a stop
// command can be missed, but a row that no longer says "running", or is
// gone, ends the retries without another attempt. Against the real table;
// skipped without VRSKY_TEST_POSTGRES_URL (CI sets it).
func TestStartRetrying_RowCheck(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	var owner, tenant, other string
	if err := db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('retry-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	for _, dst := range []*string{&tenant, &other} {
		if err := db.QueryRowContext(ctx, `INSERT INTO tenants (name, slug, owner_id)
			VALUES ('retry', 'retry-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	add := func(status string) string {
		id := uuid.New().String()
		if _, err := db.ExecContext(ctx, `INSERT INTO connections (id, tenant_id, name, status, nodes, edges)
			VALUES ($1, $2, $3, $4, '[]'::jsonb, '[]'::jsonb)`, id, tenant, "c-"+id, status); err != nil {
			t.Fatal(err)
		}
		return id
	}

	t.Run("ConnectionRunning", func(t *testing.T) {
		running, stopped := add("running"), add("stopped")
		for _, tc := range []struct {
			name, id, tenant string
			want             bool
		}{
			{"running", running, tenant, true},
			{"stopped", stopped, tenant, false},
			{"another tenant's", running, other, false},
			{"gone", uuid.New().String(), tenant, false},
		} {
			got, err := sdk.ConnectionRunning(ctx, db, tc.id, tc.tenant)
			if err != nil || got != tc.want {
				t.Errorf("%s: ConnectionRunning = (%v, %v), want (%v, nil)", tc.name, got, err, tc.want)
			}
		}
	})

	cases := []struct {
		name string
		// change is what happens to the row after the first failure; nil
		// leaves it running.
		change       string
		wantAttempts int32
		wantActive   bool
	}{
		{"row still running: retried and started", "", 2, true},
		{"row stopped: retries end", `UPDATE connections SET status = 'stopped' WHERE id = $1`, 1, false},
		{"row gone: retries end", `DELETE FROM connections WHERE id = $1`, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := add("running")
			o := newRTOwner(t, failFirst(1, sdk.TransientStart(errDBDown)))
			o.db = db
			o.policy = sdk.RetryPolicy{Base: time.Second, Max: time.Second}
			o.startConnection(context.Background(), id, tenant)
			if sdk.PendingRetriesForTest(&o.g, &o.mu) != 1 {
				t.Fatal("no retry pending after a transient failure")
			}
			if tc.change != "" {
				if _, err := db.ExecContext(ctx, tc.change, id); err != nil {
					t.Fatal(err)
				}
			}
			if !sdk.WaitRetriesForTest(&o.g, 5*time.Second) {
				t.Fatal("the retry loop did not end")
			}
			if got := o.attempts.Load(); got != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", got, tc.wantAttempts)
			}
			if got := o.activations.Load() == 1; got != tc.wantActive {
				t.Errorf("activated = %v, want %v", got, tc.wantActive)
			}
		})
	}
}
