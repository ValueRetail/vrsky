package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// Paid plans against the real statements (plans/paid-plans.md): a new
// workspace's trial and quota row, the operator's one write, the sweep's
// query, the one-open-request rule and the stopped-by-billing bookkeeping.
func TestBillingDB_TrialPlansAndRequests(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewPostgresRepository(db)

	var owner string
	if err := db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('bill-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// --- A new workspace starts on trial, with the trial tier's limits. ---
	before := time.Now()
	ten, err := repo.CreateTenant(ctx, owner, "Shop", "shop-"+before.Format("150405.000"))
	if err != nil {
		t.Fatal(err)
	}
	if ten.SubscriptionPlan != PlanTrial || ten.BillingStatus != BillingTrial || ten.TrialEndsAt == nil ||
		ten.TrialEndsAt.Before(before.Add(TrialLength-time.Minute)) || ten.TrialEndsAt.After(before.Add(TrialLength+time.Minute)) {
		t.Fatalf("new tenant = plan %s, status %s, trial_ends_at %v; want a trial ending in %v", ten.SubscriptionPlan, ten.BillingStatus, ten.TrialEndsAt, TrialLength)
	}
	q, err := repo.GetTenantQuotas(ctx, ten.ID)
	if err != nil || q.PlanName != PlanTrial || q.MaxIntegrations != 2 || q.MaxMsgPerSec != 25 || q.MaxStorageBytes != 1<<30 {
		t.Fatalf("new tenant quotas = %+v (%v), want the trial row", q, err)
	}
	got, _ := repo.GetTenantByID(ctx, ten.ID)
	if got.BillingStatus != BillingTrial || got.TrialEndsAt == nil {
		t.Fatalf("GetTenantByID drops billing fields: %+v", got)
	}
	mine, _ := repo.GetUserTenants(ctx, owner)
	if len(mine) != 1 || mine[0].BillingStatus != BillingTrial || mine[0].TrialEndsAt == nil {
		t.Fatalf("GetUserTenants drops billing fields: %+v", mine)
	}

	// --- Only one open request per workspace; the second insert is refused. ---
	if err := repo.CreatePlanRequest(ctx, &PlanRequest{TenantID: ten.ID, RequestedPlan: PlanPaid, Message: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreatePlanRequest(ctx, &PlanRequest{TenantID: ten.ID, RequestedPlan: PlanEnterprise}); err == nil {
		t.Fatal("a second open request for the same workspace was accepted")
	}
	if err := repo.CreatePlanRequest(ctx, &PlanRequest{TenantID: ten.ID, RequestedPlan: "platinum"}); err == nil {
		t.Fatal("a request for a plan that is not a tier was accepted")
	}

	// --- The sweep's query sees the trial only once its clock has run out. ---
	if exp, _ := repo.ListExpiredTrials(ctx, time.Now()); len(exp) != 0 {
		t.Fatalf("a live trial listed as expired: %+v", exp)
	}
	if exp, _ := repo.ListExpiredTrials(ctx, time.Now().Add(TrialLength+time.Hour)); len(exp) != 1 || exp[0].ID != ten.ID {
		t.Fatalf("an expired trial not listed: %+v", exp)
	}

	// --- Stopped-by-billing bookkeeping is tenant-scoped. ---
	other, err := repo.CreateTenant(ctx, owner, "Other", "other-"+before.Format("150405.000"))
	if err != nil {
		t.Fatal(err)
	}
	conn := &Connection{ID: uuid.New().String(), TenantID: ten.ID, Name: "p", Status: "stopped",
		Nodes: []*Node{{ID: "n1", Type: "consumer", Config: json.RawMessage(`{"type":"http"}`)}}}
	if err := repo.CreateConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetConnectionStoppedByBilling(ctx, other.ID, conn.ID, true); err != nil {
		t.Fatal(err)
	}
	if list, _ := repo.ListConnectionsStoppedByBilling(ctx, ten.ID); len(list) != 0 {
		t.Fatal("another workspace's id flagged this workspace's pipeline")
	}
	if err := repo.SetConnectionStoppedByBilling(ctx, ten.ID, conn.ID, true); err != nil {
		t.Fatal(err)
	}
	if list, _ := repo.ListConnectionsStoppedByBilling(ctx, ten.ID); len(list) != 1 || list[0].ID != conn.ID || len(list[0].Nodes) != 1 {
		t.Fatalf("flagged pipeline not listed with its graph: %+v", list)
	}
	if list, _ := repo.ListConnectionsStoppedByBilling(ctx, other.ID); len(list) != 0 {
		t.Fatal("a flagged pipeline leaked into another workspace's list")
	}

	// --- The operator's write: plan, status, limits and the request move together. ---
	if err := repo.SetTenantPlan(ctx, ten.ID, TenantPlanChange{Plan: "platinum"}, BillingPaid, nil); err == nil {
		t.Fatal("an unknown plan was accepted")
	}
	note := "invoice 1"
	if err := repo.SetTenantPlan(ctx, ten.ID, TenantPlanChange{Plan: PlanPaid, Note: &note}, BillingPaid, &owner); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetTenantByID(ctx, ten.ID)
	q, _ = repo.GetTenantQuotas(ctx, ten.ID)
	open, _ := repo.GetOpenPlanRequest(ctx, ten.ID)
	if got.SubscriptionPlan != PlanPaid || got.BillingStatus != BillingPaid || got.TrialEndsAt != nil || got.BillingNote == nil || *got.BillingNote != note ||
		q.PlanName != PlanPaid || q.MaxIntegrations != 20 || q.MaxMsgPerSec != 200 || q.MaxStorageBytes != 100<<30 || open != nil {
		t.Fatalf("after activation: tenant=%+v quotas=%+v open=%+v", got, q, open)
	}
	var outcome string
	_ = db.QueryRowContext(ctx, `SELECT outcome FROM plan_requests WHERE tenant_id = $1`, ten.ID).Scan(&outcome)
	if outcome != "accepted" {
		t.Errorf("request outcome = %q, want accepted", outcome)
	}
	// Back to a trial with a chosen end: the clock is set, the limits shrink.
	ends := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	if err := repo.SetTenantPlan(ctx, ten.ID, TenantPlanChange{Plan: PlanTrial, TrialEndsAt: &ends}, BillingTrial, &owner); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetTenantByID(ctx, ten.ID)
	q, _ = repo.GetTenantQuotas(ctx, ten.ID)
	if got.BillingStatus != BillingTrial || got.TrialEndsAt == nil || !got.TrialEndsAt.Equal(ends) || q.MaxIntegrations != 2 {
		t.Fatalf("back on trial: tenant=%+v quotas=%+v", got, q)
	}
	if err := repo.SetTenantBillingStatus(ctx, ten.ID, BillingSuspended); err != nil {
		t.Fatal(err)
	}

	// --- The operator's list: both workspaces, owner email, state, request. ---
	if err := repo.CreatePlanRequest(ctx, &PlanRequest{TenantID: other.ID, RequestedPlan: PlanEnterprise, Message: "big"}); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListTenantsForPlatform(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]*PlatformTenant{}
	for _, r := range rows {
		seen[r.ID] = r
	}
	if a, b := seen[ten.ID], seen[other.ID]; a == nil || b == nil ||
		a.BillingStatus != BillingSuspended || a.OwnerEmail == "" || a.OpenRequest != nil ||
		b.OpenRequest == nil || b.OpenRequest.RequestedPlan != PlanEnterprise || b.BillingStatus != BillingTrial {
		t.Fatalf("platform rows: %+v %+v", a, b)
	}
	if rows[0].ID != other.ID {
		t.Errorf("the workspace with an open request should sort first; got %s", rows[0].Name)
	}
	if inbox, _ := repo.ListOpenPlanRequests(ctx); len(inbox) != 1 || inbox[0].TenantID != other.ID {
		t.Fatalf("inbox = %+v", inbox)
	}
}
