package managementapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func newPlanTestHandler() (*Handler, *MockRepository) {
	repo := &MockRepository{}
	h := NewHandler(repo, NewValidator())
	return h, repo
}

func TestHandlePlanUpdate_Valid(t *testing.T) {
	h, repo := newPlanTestHandler()
	hookCalled := false
	h.SetGatewaySync(func(context.Context) error { hookCalled = true; return nil })

	req := httptest.NewRequest("PUT", "/api/v1/tenants/t1/plan", strings.NewReader(`{"plan":"pro"}`))
	req.SetPathValue("tenant_id", "t1")
	rec := httptest.NewRecorder()

	h.HandlePlanUpdate(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if repo.tenantPlans["t1"] != "pro" {
		t.Errorf("plan not persisted: %v", repo.tenantPlans)
	}
	if !hookCalled {
		t.Error("gateway sync hook was not called on plan change")
	}
}

func TestHandlePlanUpdate_InvalidPlan(t *testing.T) {
	h, repo := newPlanTestHandler()
	h.SetGatewaySync(func(context.Context) error { t.Fatal("hook must not fire on invalid plan"); return nil })

	req := httptest.NewRequest("PUT", "/api/v1/tenants/t1/plan", strings.NewReader(`{"plan":"platinum"}`))
	req.SetPathValue("tenant_id", "t1")
	rec := httptest.NewRecorder()

	h.HandlePlanUpdate(rec, req)

	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if _, ok := repo.tenantPlans["t1"]; ok {
		t.Error("invalid plan should not be persisted")
	}
}

func TestHandlePlanUpdate_TenantNotFound(t *testing.T) {
	h, _ := newPlanTestHandler()

	req := httptest.NewRequest("PUT", "/api/v1/tenants/missing/plan", strings.NewReader(`{"plan":"pro"}`))
	req.SetPathValue("tenant_id", "missing")
	rec := httptest.NewRecorder()

	h.HandlePlanUpdate(rec, req)

	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404 for unknown tenant", rec.Code)
	}
}

func TestHandlePlanUpdate_NoHookIsFine(t *testing.T) {
	h, repo := newPlanTestHandler() // no gateway sync wired (non-gateway deployment)

	req := httptest.NewRequest("PUT", "/api/v1/tenants/t1/plan", strings.NewReader(`{"plan":"enterprise"}`))
	req.SetPathValue("tenant_id", "t1")
	rec := httptest.NewRecorder()

	h.HandlePlanUpdate(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if repo.tenantPlans["t1"] != "enterprise" {
		t.Errorf("plan not persisted: %v", repo.tenantPlans)
	}
}
