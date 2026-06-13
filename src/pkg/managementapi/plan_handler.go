package managementapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Subscription plan (Phase 3G / #90).
//
//	PUT /api/v1/tenants/{tenant_id}/plan   — owner
//
// The plan drives the gateway's per-tenant edge rate limit. After persisting it,
// HandlePlanUpdate triggers a gateway config refresh (gatewaySync) so the new
// limit takes effect within Traefik's file-watch interval — no restart.

// validPlans are the subscription tiers that map to gateway rate-limit middlewares.
var validPlans = map[string]bool{"free": true, "pro": true, "enterprise": true}

type planUpdateRequest struct {
	Plan string `json:"plan"`
}

// HandlePlanUpdate: PUT /api/v1/tenants/{tenant_id}/plan
func (h *Handler) HandlePlanUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := r.PathValue("tenant_id")

	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var req planUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "request body is empty", nil)
			return
		}
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		return
	}
	if !validPlans[req.Plan] {
		_ = writeError(w, http.StatusBadRequest, "ValidationError",
			"plan must be one of: free, pro, enterprise", nil)
		return
	}

	if err := h.repo.UpdateTenantPlan(ctx, tenantID, req.Plan); err != nil {
		if errors.Is(err, ErrTenantNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to update plan", nil)
		return
	}

	SetAuditDetail(ctx, "plan", req.Plan)
	SetAuditAction(ctx, "tenant.plan.update")

	// Refresh the gateway's per-tenant rate-limit config so the new plan's limit
	// takes effect at the edge. The plan is already persisted, so a sync failure
	// must not fail the request — the next change or a restart reconciles it.
	if h.gatewaySync != nil {
		if err := h.gatewaySync(ctx); err != nil {
			SetAuditDetail(ctx, "gateway_sync_error", err.Error())
		}
	}

	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: map[string]string{
		"tenant_id": tenantID,
		"plan":      req.Plan,
	}})
}
