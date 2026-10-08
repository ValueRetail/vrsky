package managementapi

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// A missing quota row — a workspace older than the quota table, or a row
// removed by hand — is rebuilt from the tenant's plan, never from the table's
// pre-billing column defaults (plans/hygiene-fixes.md).
func TestQuotasDB_AutoCreateFollowsThePlan(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewPostgresRepository(db)

	var owner string
	if err := db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('quota-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	ten, err := repo.CreateTenant(ctx, owner, "Shop", "shop-"+time.Now().Format("150405.000"))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		plan                 string
		wantPlan             string
		msgPerSec, integrate int
		storage              int64
	}{
		{"paid", "paid", 200, 20, 100 << 30},
		{"enterprise", "enterprise", 0, 0, 0},
		{"legacy-pro", "trial", 25, 2, 1 << 30}, // no limits row for that name: trial's, not the column defaults
	}
	for _, tc := range cases {
		if _, err := db.ExecContext(ctx, `DELETE FROM tenant_quotas WHERE tenant_id = $1`, ten.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE tenants SET subscription_plan = $2 WHERE id = $1`, ten.ID, tc.plan); err != nil {
			t.Fatal(err)
		}
		q, err := repo.GetTenantQuotas(ctx, ten.ID)
		if err != nil {
			t.Fatalf("plan %s: %v", tc.plan, err)
		}
		if q.PlanName != tc.wantPlan || q.MaxMsgPerSec != tc.msgPerSec || q.MaxIntegrations != tc.integrate || q.MaxStorageBytes != tc.storage {
			t.Fatalf("plan %s: auto-created quotas = %s %d/%d/%d, want %s %d/%d/%d",
				tc.plan, q.PlanName, q.MaxMsgPerSec, q.MaxIntegrations, q.MaxStorageBytes,
				tc.wantPlan, tc.msgPerSec, tc.integrate, tc.storage)
		}
	}
}

// A tenant that does not exist gets an error, promptly. The old lookup
// inserted (which failed on the foreign key, unnoticed) and called itself
// again, for as long as the context lasted.
func TestQuotasDB_UnknownTenantErrorsInsteadOfRecursing(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err = NewPostgresRepository(db).GetTenantQuotas(ctx, uuid.NewString())
	if !errors.Is(err, ErrTenantNotFound) || time.Since(start) > 2*time.Second {
		t.Fatalf("unknown tenant: err %v after %v; want ErrTenantNotFound at once", err, time.Since(start))
	}
}
