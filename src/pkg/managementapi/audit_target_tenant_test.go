package managementapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The operator's plan and quota writes act on the workspace named in the
// path, so their audit entries belong in that workspace's log, with the
// operator as actor, whatever workspace the operator has selected (#685).

// opsWorkspace is a workspace the operator belongs to and may have selected.
const opsWorkspace = "33333333-3333-3333-3333-333333333333"

// serveAudited sends one request through AuditMiddleware and the real
// routes, chained as cmd/management-api does (TenantID → Audit → mux).
// workspace is the tenant TenantIDMiddleware would put in the context, ""
// for none. Production never puts one in for /api/v1/tenants/* paths; doing
// it here anyway is the harder case for those routes.
func serveAudited(t *testing.T, h *Handler, aw AuditWriter, method, path, workspace, bearer string, body any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if workspace != "" {
		req = req.WithContext(ContextWithTenantID(req.Context(), workspace))
	}
	if bearer != "" { // "" sends no credential at all
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	AuditMiddleware(aw, nil)(mux).ServeHTTP(rec, req)
	return rec
}

// settle is for cases that expect no write at all: nothing can be waited
// for, so it gives the middleware's asynchronous write a short while to show
// up and returns whatever did. Cases that expect a write use wait instead.
func (f *fakeAuditWriter) settle() []*AuditEntry {
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*AuditEntry(nil), f.entries...)
}

func newAuditBillingEnv(t *testing.T) *billingEnv {
	t.Helper()
	e := newBillingEnv(t)
	e.addTenant(billTenant, "shop", PlanTrial, BillingTrial, nil)
	e.addTenant(billTenantB, "other", PlanTrial, BillingTrial, nil)
	e.addTenant(opsWorkspace, "ops", PlanPaid, BillingPaid, nil)
	e.repo.roles["ops|"+opsWorkspace] = "owner"
	return e
}

func TestAudit_OperatorPlanAndQuotaWritesFileUnderPathTenant(t *testing.T) {
	quotas := map[string]any{"plan_name": "paid", "max_msg_per_sec": 9, "max_integrations": 9, "max_storage_bytes": 9}
	platformPlan := "/api/v1/platform/tenants/" + billTenant + "/plan"
	legacyPlan := "/api/v1/tenants/" + billTenant + "/plan"
	quotaPath := "/api/v1/tenants/" + billTenant + "/quotas"

	cases := []struct {
		name       string
		path       string
		workspace  string
		body       any
		wantStatus int
		wantAction string // "" = not checked
	}{
		{"platform plan, no workspace selected", platformPlan, "", map[string]string{"plan": PlanPaid}, http.StatusOK, "platform.plan.set"},
		{"platform plan, own workspace selected", platformPlan, opsWorkspace, map[string]string{"plan": PlanPaid}, http.StatusOK, "platform.plan.set"},
		{"platform plan, another workspace selected", platformPlan, billTenantB, map[string]string{"plan": PlanPaid}, http.StatusOK, "platform.plan.set"},
		{"platform plan refused, own workspace selected", platformPlan, opsWorkspace, map[string]string{"plan": "platinum"}, http.StatusBadRequest, ""},
		{"legacy plan, no workspace selected", legacyPlan, "", map[string]string{"plan": "pro"}, http.StatusOK, "tenant.plan.update"},
		{"legacy plan, own workspace selected", legacyPlan, opsWorkspace, map[string]string{"plan": "pro"}, http.StatusOK, "tenant.plan.update"},
		{"quotas, no workspace selected", quotaPath, "", quotas, http.StatusOK, "quota.update"},
		{"quotas, own workspace selected", quotaPath, opsWorkspace, quotas, http.StatusOK, "quota.update"},
		{"quotas refused, own workspace selected", quotaPath, opsWorkspace, map[string]any{"max_msg_per_sec": -1}, http.StatusBadRequest, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAuditBillingEnv(t)
			aw := &fakeAuditWriter{}
			rec := serveAudited(t, e.h, aw, http.MethodPut, c.path, c.workspace, "tokOps", c.body)
			if rec.Code != c.wantStatus {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.wantStatus, rec.Body.String())
			}
			// One request, one write: wait returns once it has landed.
			got := aw.wait(t, 1)
			if len(got) != 1 {
				t.Fatalf("want exactly one audit entry, got %d: %+v", len(got), got)
			}
			en := got[0]
			if en.TenantID != billTenant {
				t.Errorf("filed under tenant %q, want the path's %q", en.TenantID, billTenant)
			}
			if en.UserID == nil || *en.UserID != "ops" || en.ActorLabel != operatorMail {
				t.Errorf("actor = (%v, %q), want the operator (ops, %s)", en.UserID, en.ActorLabel, operatorMail)
			}
			if en.StatusCode != c.wantStatus {
				t.Errorf("status_code %d, want %d", en.StatusCode, c.wantStatus)
			}
			if c.wantAction != "" && en.Action != c.wantAction {
				t.Errorf("action %q, want %q", en.Action, c.wantAction)
			}
		})
	}
}

// Only a caller the operator gate let through may file entries in the path's
// workspace: a refused owner of another workspace must not reach its log.
func TestAudit_NonOperatorCannotFileIntoPathTenant(t *testing.T) {
	cases := []struct {
		name, path, workspace string
		body                  any
	}{
		{"platform plan, own workspace selected", "/api/v1/platform/tenants/" + billTenant + "/plan", billTenantB, map[string]string{"plan": PlanPaid}},
		{"legacy plan, own workspace selected", "/api/v1/tenants/" + billTenant + "/plan", billTenantB, map[string]string{"plan": "pro"}},
		{"quotas, own workspace selected", "/api/v1/tenants/" + billTenant + "/quotas", billTenantB, map[string]any{"max_msg_per_sec": 1}},
		{"quotas, no workspace selected", "/api/v1/tenants/" + billTenant + "/quotas", "", map[string]any{"max_msg_per_sec": 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAuditBillingEnv(t)
			aw := &fakeAuditWriter{}
			rec := serveAudited(t, e.h, aw, http.MethodPut, c.path, c.workspace, "tokOwnerB", c.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body.String())
			}
			// Not under the path's workspace, and since #695 not under the
			// selected one either: the caller was verified against neither.
			if got := aw.settle(); len(got) != 0 {
				t.Fatalf("a refused non-operator filed entries: %+v", got)
			}
		})
	}
}

// Every other route keeps filing under the caller's context tenant, the
// operator's ordinary work included.
func TestAudit_OtherRoutesKeepContextTenant(t *testing.T) {
	cases := []struct {
		name, method, path, workspace, bearer string
		body                                  any
	}{
		{"owner creates a connection", http.MethodPost, "/api/v1/connections", billTenant, "tokOwner", map[string]any{"name": ""}},
		{"operator creates a connection in own workspace", http.MethodPost, "/api/v1/connections", opsWorkspace, "tokOps", map[string]any{"name": ""}},
		// Owner writes under /api/v1/tenants/{tenant_id}/* go to the path's
		// workspace once the membership check passed (#695); see
		// TestAudit_OwnerTenantWritesFileUnderPathTenant.
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAuditBillingEnv(t)
			aw := &fakeAuditWriter{}
			serveAudited(t, e.h, aw, c.method, c.path, c.workspace, c.bearer, c.body)
			got := aw.wait(t, 1)
			if len(got) != 1 || got[0].TenantID != c.workspace {
				t.Fatalf("want one entry under the context tenant %q, got %+v", c.workspace, got)
			}
		})
	}
}

// The workspace whose plan changed sees the entry in its own audit list; the
// operator's workspace does not.
func TestAudit_TargetTenantListsOperatorPlanChange(t *testing.T) {
	e := newAuditBillingEnv(t)
	aw := &fakeAuditWriter{}
	if rec := serveAudited(t, e.h, aw, http.MethodPut, "/api/v1/platform/tenants/"+billTenant+"/plan", opsWorkspace, "tokOps",
		map[string]string{"plan": PlanPaid}); rec.Code != http.StatusOK {
		t.Fatalf("set plan: %d %s", rec.Code, rec.Body.String())
	}
	// Store what the middleware wrote in the repository the list reads.
	for _, en := range aw.wait(t, 1) {
		_ = e.repo.CreateAuditEntry(context.Background(), en)
	}

	list := func(workspace, bearer string) []AuditEntry {
		t.Helper()
		rec := runHTTPAs(t, e.h, http.MethodGet, "/api/v1/audit", workspace, bearer, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list as %s: %d %s", bearer, rec.Code, rec.Body.String())
		}
		var out struct {
			Data []AuditEntry `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data
	}
	got := list(billTenant, "tokOwner")
	if len(got) != 1 || got[0].Action != "platform.plan.set" || got[0].ActorLabel != operatorMail {
		t.Fatalf("the workspace's own audit list = %+v, want the operator's platform.plan.set", got)
	}
	if got := list(opsWorkspace, "tokOps"); len(got) != 0 {
		t.Fatalf("the operator's workspace lists the change: %+v", got)
	}
}
