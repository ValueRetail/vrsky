package managementapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/auth"
	"github.com/ValueRetail/vrsky/pkg/notify"
)

// billingEnv: an RBAC mock with tenants the billing code can read and write,
// a fake orchestrator that records what it was told, and captured alerts.
type billingEnv struct {
	h      *Handler
	repo   *rbacMock
	orch   *billingOrchestrator
	alerts []*notify.Alert
}

type billingOrchestrator struct {
	mu               sync.Mutex
	started, stopped []string
	failStart        map[string]bool
}

func (f *billingOrchestrator) StartPipeline(_ context.Context, c *Connection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failStart[c.ID] {
		return errContext("deploy refused")
	}
	f.started = append(f.started, c.ID)
	return nil
}
func (f *billingOrchestrator) StopPipeline(_ context.Context, c *Connection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, c.ID)
	return nil
}

type errContext string

func (e errContext) Error() string { return string(e) }

const (
	billTenant   = "11111111-1111-1111-1111-111111111111"
	billTenantB  = "22222222-2222-2222-2222-222222222222"
	operatorMail = "ops@example.com"
)

func newBillingEnv(t *testing.T) *billingEnv {
	t.Helper()
	repo := newRBACMock()
	repo.tenants = map[string]*Tenant{}
	e := &billingEnv{repo: repo, orch: &billingOrchestrator{failStart: map[string]bool{}}}
	e.h = NewHandler(repo, NewValidator())
	e.h.SetOrchestratorFactory(func(*Connection) PipelineOrchestrator { return e.orch })
	e.h.notifyPlatform = func(_ context.Context, a *notify.Alert) { e.alerts = append(e.alerts, a) }
	e.h.SetPlatformOperators([]string{" " + operatorMail + " "})
	// Sessions: an owner and an editor of the tenant, an owner of tenant B, and
	// the operator (a member of nothing).
	repo.addUserSession("tokOwner", "owner", billTenant, "owner")
	repo.addUserSession("tokEditor", "editor", billTenant, "editor")
	repo.addUserSession("tokOwnerB", "ownerB", billTenantB, "owner")
	repo.addUserSession("tokOps", "ops", "", "")
	repo.sessions[hashOf("tokOps")].user.Email = operatorMail
	return e
}

func hashOf(raw string) string { return auth.HashToken(raw) }

func (e *billingEnv) addTenant(id, name, plan, status string, trialEnds *time.Time) *Tenant {
	t := &Tenant{ID: id, Name: name, Slug: name, SubscriptionPlan: plan, BillingStatus: status, TrialEndsAt: trialEnds, Status: "active"}
	e.repo.tenants[id] = t
	return t
}

func (e *billingEnv) addConnection(id, tenantID, status string, flagged bool) *Connection {
	c := &Connection{ID: id, TenantID: tenantID, Name: id, Status: status, Nodes: []*Node{{ID: "n1", Type: "consumer"}}}
	e.repo.connections[id] = c
	if flagged {
		_ = e.repo.SetConnectionStoppedByBilling(context.Background(), tenantID, id, true)
	}
	return c
}

func decodeData(t *testing.T, body []byte, v any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("decode data: %v (%s)", err, env.Data)
	}
}

func TestPlan_OwnerCannotSetPlanOrQuotas_OperatorCan(t *testing.T) {
	e := newBillingEnv(t)
	e.addTenant(billTenant, "shop", PlanTrial, BillingTrial, nil)
	quotas := map[string]any{"plan_name": "paid", "max_msg_per_sec": 999, "max_integrations": 99, "max_storage_bytes": 1}

	if w := runHTTPAs(t, e.h, http.MethodPut, "/api/v1/tenants/"+billTenant+"/quotas", billTenant, "tokOwner", quotas); w.Code != http.StatusForbidden {
		t.Fatalf("owner set quotas: want 403, got %d %s", w.Code, w.Body.String())
	}
	if w := runHTTPAs(t, e.h, http.MethodPut, "/api/v1/tenants/"+billTenant+"/plan", billTenant, "tokOwner", map[string]string{"plan": "paid"}); w.Code != http.StatusForbidden {
		t.Fatalf("owner set plan: want 403, got %d %s", w.Code, w.Body.String())
	}
	if w := runHTTPAs(t, e.h, http.MethodPut, "/api/v1/tenants/"+billTenant+"/quotas", billTenant, "tokOps", quotas); w.Code != http.StatusOK {
		t.Fatalf("operator set quotas: want 200, got %d %s", w.Code, w.Body.String())
	}
	if q, _ := e.repo.GetTenantQuotas(context.Background(), billTenant); q == nil || q.MaxMsgPerSec != 999 {
		t.Fatalf("operator's quota write did not land: %+v", q)
	}
}

func TestOperator_IsByEmailListOnly(t *testing.T) {
	e := newBillingEnv(t)
	e.addTenant(billTenant, "shop", PlanTrial, BillingTrial, nil)
	// A workspace owner is not an operator, whatever their role.
	if w := runHTTPAs(t, e.h, http.MethodGet, "/api/v1/platform/tenants", "", "tokOwner", nil); w.Code != http.StatusForbidden {
		t.Fatalf("owner on the platform list: want 403, got %d", w.Code)
	}
	if w := runHTTPAs(t, e.h, http.MethodGet, "/api/v1/platform/tenants", "", "tokOps", nil); w.Code != http.StatusOK {
		t.Fatalf("operator on the platform list: want 200, got %d %s", w.Code, w.Body.String())
	}
	// Case does not matter; an empty list means nobody.
	e.h.SetPlatformOperators([]string{"OPS@Example.COM"})
	if w := runHTTPAs(t, e.h, http.MethodGet, "/api/v1/platform/tenants", "", "tokOps", nil); w.Code != http.StatusOK {
		t.Fatalf("operator matched case-insensitively: want 200, got %d", w.Code)
	}
	e.h.SetPlatformOperators(nil)
	if w := runHTTPAs(t, e.h, http.MethodGet, "/api/v1/platform/tenants", "", "tokOps", nil); w.Code != http.StatusForbidden {
		t.Fatalf("with no operators configured: want 403, got %d", w.Code)
	}
	// Unauthenticated: 401, not 403 — the session check comes first.
	if w := runHTTPAs(t, e.h, http.MethodGet, "/api/v1/platform/tenants", "", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: want 401, got %d", w.Code)
	}
}

func TestOperator_SetPlanCopiesLimitsAndRestartsStoppedPipelines(t *testing.T) {
	e := newBillingEnv(t)
	e.addTenant(billTenant, "shop", PlanTrial, BillingSuspended, nil)
	e.addConnection("c-stopped-by-billing", billTenant, "stopped", true)
	e.addConnection("c-stopped-by-user", billTenant, "stopped", false)
	e.addConnection("c-wont-start", billTenant, "stopped", true)
	e.orch.failStart["c-wont-start"] = true
	// Tenant B has a pipeline the suspension of A must not touch, flagged or not.
	e.addTenant(billTenantB, "other", PlanPaid, BillingPaid, nil)
	e.addConnection("c-other-tenant", billTenantB, "stopped", true)
	_ = e.repo.CreatePlanRequest(context.Background(), &PlanRequest{TenantID: billTenant, RequestedPlan: PlanPaid})

	w := runHTTPAs(t, e.h, http.MethodPut, "/api/v1/platform/tenants/"+billTenant+"/plan", "", "tokOps",
		map[string]any{"plan": "paid", "note": "invoice 2026-041"})
	if w.Code != http.StatusOK {
		t.Fatalf("set plan: want 200, got %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Plan          string `json:"plan"`
		BillingStatus string `json:"billing_status"`
		Resumed       int    `json:"pipelines_resumed"`
	}
	decodeData(t, w.Body.Bytes(), &out)
	if out.Plan != PlanPaid || out.BillingStatus != BillingPaid || out.Resumed != 1 {
		t.Fatalf("response = %+v, want paid/paid with 1 resumed", out)
	}
	ten := e.repo.tenants[billTenant]
	if ten.BillingStatus != BillingPaid || ten.TrialEndsAt != nil || ten.BillingNote == nil || *ten.BillingNote != "invoice 2026-041" {
		t.Errorf("tenant after activation = %+v", ten)
	}
	q, _ := e.repo.GetTenantQuotas(context.Background(), billTenant)
	if q.PlanName != PlanPaid || q.MaxIntegrations != 20 || q.MaxMsgPerSec != 200 {
		t.Errorf("quotas did not follow the plan: %+v", q)
	}
	if c := e.repo.connections["c-stopped-by-billing"]; c.Status != "running" || e.repo.resumable[c.ID] {
		t.Errorf("the pipeline the suspension stopped was not started: status=%s flagged=%v", c.Status, e.repo.resumable[c.ID])
	}
	if c := e.repo.connections["c-stopped-by-user"]; c.Status != "stopped" {
		t.Errorf("a pipeline the user stopped was started: %s", c.Status)
	}
	if c := e.repo.connections["c-wont-start"]; c.Status != "stopped" || !e.repo.resumable[c.ID] {
		t.Errorf("a pipeline that would not start should stay stopped and flagged: status=%s flagged=%v", c.Status, e.repo.resumable[c.ID])
	}
	if c := e.repo.connections["c-other-tenant"]; c.Status != "stopped" {
		t.Errorf("another workspace's pipeline was started: %s", c.Status)
	}
	if open, _ := e.repo.GetOpenPlanRequest(context.Background(), billTenant); open != nil {
		t.Errorf("the open request was not closed")
	}
	// Setting a trial without a date gives a fresh clock.
	w = runHTTPAs(t, e.h, http.MethodPut, "/api/v1/platform/tenants/"+billTenant+"/plan", "", "tokOps", map[string]any{"plan": "trial"})
	if w.Code != http.StatusOK || ten.BillingStatus != BillingTrial || ten.TrialEndsAt == nil ||
		ten.TrialEndsAt.Before(time.Now().Add(TrialLength-time.Minute)) {
		t.Fatalf("set trial: %d, tenant=%+v", w.Code, ten)
	}
	// Unknown plans and unknown tenants are refused before anything changes.
	if w := runHTTPAs(t, e.h, http.MethodPut, "/api/v1/platform/tenants/"+billTenant+"/plan", "", "tokOps", map[string]any{"plan": "platinum"}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown plan: want 400, got %d", w.Code)
	}
	if w := runHTTPAs(t, e.h, http.MethodPut, "/api/v1/platform/tenants/nope/plan", "", "tokOps", map[string]any{"plan": "paid"}); w.Code != http.StatusNotFound {
		t.Errorf("unknown tenant: want 404, got %d", w.Code)
	}
}

func TestSweep_SuspendsExpiredTrialsAndStopsPipelines(t *testing.T) {
	e := newBillingEnv(t)
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	e.addTenant(billTenant, "expired", PlanTrial, BillingTrial, &past)
	e.addTenant(billTenantB, "still-trying", PlanTrial, BillingTrial, &future)
	e.addTenant("33333333-3333-3333-3333-333333333333", "paying", PlanPaid, BillingPaid, nil)
	e.addConnection("c-run-1", billTenant, "running", false)
	e.addConnection("c-run-2", billTenant, "running", false)
	e.addConnection("c-already-stopped", billTenant, "stopped", false)
	e.addConnection("c-b-running", billTenantB, "running", false)

	sweep := e.h.NewBillingSweep(nil)
	sweep.now = func() time.Time { return now }
	if n := sweep.RunOnce(context.Background()); n != 1 {
		t.Fatalf("suspended %d workspaces, want 1", n)
	}
	if e.repo.tenants[billTenant].BillingStatus != BillingSuspended {
		t.Error("the expired trial was not suspended")
	}
	if e.repo.tenants[billTenantB].BillingStatus != BillingTrial || e.repo.tenants["33333333-3333-3333-3333-333333333333"].BillingStatus != BillingPaid {
		t.Error("a workspace that was not an expired trial changed state")
	}
	for _, id := range []string{"c-run-1", "c-run-2"} {
		if c := e.repo.connections[id]; c.Status != "stopped" || !e.repo.resumable[id] {
			t.Errorf("%s: status=%s flagged=%v, want stopped and flagged", id, c.Status, e.repo.resumable[id])
		}
	}
	if len(e.orch.stopped) != 2 {
		t.Errorf("orchestrator was told to stop %v, want the two running pipelines", e.orch.stopped)
	}
	if e.repo.resumable["c-already-stopped"] {
		t.Error("a pipeline the user had stopped was flagged as stopped by billing")
	}
	if c := e.repo.connections["c-b-running"]; c.Status != "running" {
		t.Error("another workspace's pipeline was stopped")
	}
	if len(e.alerts) != 1 || e.alerts[0].Name != notify.TrialExpiredName || e.alerts[0].TenantID != "" {
		t.Fatalf("alerts = %+v, want one TrialExpired for the platform", e.alerts)
	}
	// Two stop events, one per pipeline, naming the reason.
	events := 0
	for _, ev := range e.repo.events {
		if ev.EventType == "stopped" && ev.TenantID == billTenant {
			events++
		}
	}
	if events != 2 {
		t.Errorf("%d stop events recorded, want 2", events)
	}
	// Running again changes nothing: a suspended workspace is not a trial.
	if n := sweep.RunOnce(context.Background()); n != 0 || len(e.alerts) != 1 {
		t.Fatalf("second run suspended %d and alerts=%d; want 0 and 1", n, len(e.alerts))
	}
}

func TestGate_SuspendedTenant402(t *testing.T) {
	e := newBillingEnv(t)
	e.addTenant(billTenant, "shop", PlanTrial, BillingSuspended, nil)
	e.addConnection("c1", billTenant, "stopped", true)

	checks := []struct {
		name, method, path string
		body               any
	}{
		{"create pipeline", http.MethodPost, "/api/v1/connections", map[string]any{"name": "x"}},
		{"start pipeline", http.MethodPost, "/api/v1/connections/c1/start", nil},
		{"connection request", http.MethodPost, "/api/v1/tenants/" + billTenant + "/connection-requests", map[string]any{"target_tenant_id": billTenantB}},
	}
	for _, c := range checks {
		w := runHTTPAs(t, e.h, c.method, c.path, billTenant, "tokOwner", c.body)
		if w.Code != http.StatusPaymentRequired {
			t.Fatalf("%s while suspended: want 402, got %d %s", c.name, w.Code, w.Body.String())
		}
		var body ErrorResponse
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if body.Error != "PlanRequired" || body.Details["request_plan_url"] != "/settings/plan" {
			t.Errorf("%s: 402 body = %+v", c.name, body)
		}
	}
	// Reading is never gated.
	if w := runHTTPAs(t, e.h, http.MethodGet, "/api/v1/connections", billTenant, "tokOwner", nil); w.Code != http.StatusOK {
		t.Fatalf("list while suspended: want 200, got %d %s", w.Code, w.Body.String())
	}
	if w := runHTTPAs(t, e.h, http.MethodGet, "/api/v1/tenants/"+billTenant+"/billing", billTenant, "tokOwner", nil); w.Code != http.StatusOK {
		t.Fatalf("billing while suspended: want 200, got %d %s", w.Code, w.Body.String())
	}
	// Once paid, the gate opens (the start then proceeds to the real handler).
	_ = e.repo.SetTenantBillingStatus(context.Background(), billTenant, BillingPaid)
	if w := runHTTPAs(t, e.h, http.MethodPost, "/api/v1/connections/c1/start", billTenant, "tokOwner", nil); w.Code == http.StatusPaymentRequired {
		t.Fatalf("start after activation still 402: %s", w.Body.String())
	}
}

func TestPlanRequest_OwnerOnlyAndNotifiesPlatform(t *testing.T) {
	e := newBillingEnv(t)
	e.addTenant(billTenant, "shop", PlanTrial, BillingTrial, nil)
	path := "/api/v1/tenants/" + billTenant + "/plan-requests"

	if w := runHTTPAs(t, e.h, http.MethodPost, path, billTenant, "tokEditor", map[string]string{"plan": "paid"}); w.Code != http.StatusForbidden {
		t.Fatalf("editor: want 403, got %d", w.Code)
	}
	if w := runHTTPAs(t, e.h, http.MethodPost, path, billTenant, "tokOwner", map[string]string{"plan": "trial"}); w.Code != http.StatusBadRequest {
		t.Fatalf("asking for trial: want 400, got %d", w.Code)
	}
	w := runHTTPAs(t, e.h, http.MethodPost, path, billTenant, "tokOwner", map[string]string{"plan": "paid", "message": "30 tills, invoice to HQ"})
	if w.Code != http.StatusCreated {
		t.Fatalf("owner: want 201, got %d %s", w.Code, w.Body.String())
	}
	var first PlanRequest
	decodeData(t, w.Body.Bytes(), &first)
	if len(e.alerts) != 1 || e.alerts[0].Name != notify.PlanRequestedName || e.alerts[0].TenantID != "" ||
		e.alerts[0].Labels["workspace"] != "shop" || e.alerts[0].Labels["plan"] != "paid" {
		t.Fatalf("alerts = %+v, want one PlanRequested for the platform, labelled with the workspace", e.alerts)
	}
	// Asking again returns the open request; the operator is not told twice.
	w = runHTTPAs(t, e.h, http.MethodPost, path, billTenant, "tokOwner", map[string]string{"plan": "enterprise"})
	var again PlanRequest
	decodeData(t, w.Body.Bytes(), &again)
	if w.Code != http.StatusOK || again.ID != first.ID || len(e.alerts) != 1 {
		t.Fatalf("second ask: %d id=%s (first %s) alerts=%d", w.Code, again.ID, first.ID, len(e.alerts))
	}
	// The workspace sees its own request on the billing view.
	w = runHTTPAs(t, e.h, http.MethodGet, "/api/v1/tenants/"+billTenant+"/billing", billTenant, "tokEditor", nil)
	var b TenantBilling
	decodeData(t, w.Body.Bytes(), &b)
	if w.Code != http.StatusOK || b.Plan != PlanTrial || b.OpenRequest == nil || b.OpenRequest.ID != first.ID || len(b.Plans) != 3 || b.Limits == nil {
		t.Fatalf("billing view: %d %+v", w.Code, b)
	}
}

func TestIsolation_BillingAndRequestsAreTenantScoped(t *testing.T) {
	e := newBillingEnv(t)
	e.addTenant(billTenant, "shop", PlanTrial, BillingTrial, nil)
	e.addTenant(billTenantB, "other", PlanPaid, BillingPaid, nil)
	// The owner of A on B's routes.
	if w := runHTTPAs(t, e.h, http.MethodGet, "/api/v1/tenants/"+billTenantB+"/billing", billTenantB, "tokOwner", nil); w.Code != http.StatusForbidden {
		t.Fatalf("A reads B's billing: want 403, got %d", w.Code)
	}
	if w := runHTTPAs(t, e.h, http.MethodPost, "/api/v1/tenants/"+billTenantB+"/plan-requests", billTenantB, "tokOwner", map[string]string{"plan": "paid"}); w.Code != http.StatusForbidden {
		t.Fatalf("A asks on B's behalf: want 403, got %d", w.Code)
	}
	if n, _ := e.repo.ListOpenPlanRequests(context.Background()); len(n) != 0 {
		t.Fatalf("a request was recorded for B: %+v", n)
	}
}

func TestPlatformList_ShowsEveryWorkspaceAndItsRequest(t *testing.T) {
	e := newBillingEnv(t)
	e.addTenant(billTenant, "shop", PlanTrial, BillingTrial, nil)
	e.addTenant(billTenantB, "other", PlanPaid, BillingPaid, nil)
	_ = e.repo.CreatePlanRequest(context.Background(), &PlanRequest{TenantID: billTenant, RequestedPlan: PlanEnterprise, Message: "hi"})

	w := runHTTPAs(t, e.h, http.MethodGet, "/api/v1/platform/tenants", "", "tokOps", nil)
	var rows []PlatformTenant
	decodeData(t, w.Body.Bytes(), &rows)
	if w.Code != http.StatusOK || len(rows) != 2 {
		t.Fatalf("platform list: %d, %d rows", w.Code, len(rows))
	}
	withReq := 0
	for _, r := range rows {
		if r.OpenRequest != nil && r.ID == billTenant && r.OpenRequest.RequestedPlan == PlanEnterprise {
			withReq++
		}
	}
	if withReq != 1 {
		t.Errorf("the open request is not on its workspace's row: %+v", rows)
	}
	w = runHTTPAs(t, e.h, http.MethodGet, "/api/v1/platform/plan-requests", "", "tokOps", nil)
	var reqs []PlanRequest
	decodeData(t, w.Body.Bytes(), &reqs)
	if w.Code != http.StatusOK || len(reqs) != 1 || reqs[0].TenantID != billTenant {
		t.Fatalf("inbox: %d %+v", w.Code, reqs)
	}
}

func TestMe_SaysWhoOperatesThePlatform(t *testing.T) {
	e := newBillingEnv(t)
	for tok, want := range map[string]bool{"tokOps": true, "tokOwner": false} {
		w := runHTTPAs(t, e.h, http.MethodGet, "/api/v1/auth/me", "", tok, nil)
		var me MeResponse
		if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || w.Code != http.StatusOK {
			t.Fatalf("me as %s: %d %s", tok, w.Code, w.Body.String())
		}
		if me.IsPlatformOperator != want {
			t.Errorf("is_platform_operator for %s = %v, want %v", tok, me.IsPlatformOperator, want)
		}
	}
}
