package managementapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Quota admin endpoints (Phase 1I / #74).
//
//	GET /api/v1/tenants/{tenant_id}/quotas   — any member
//	PUT /api/v1/tenants/{tenant_id}/quotas   — owner

type quotaUpsertRequest struct {
	PlanName        string `json:"plan_name"`
	MaxMsgPerSec    int    `json:"max_msg_per_sec"`
	MaxIntegrations int    `json:"max_integrations"`
	MaxStorageBytes int64  `json:"max_storage_bytes"`
}

// HandleGetQuotas: GET /api/v1/tenants/{tenant_id}/quotas
func (h *Handler) HandleGetQuotas(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant_id")
	q, err := h.repo.GetTenantQuotas(r.Context(), tenantID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to load quotas", nil)
		return
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: q})
}

// HandleUpdateQuotas: PUT /api/v1/tenants/{tenant_id}/quotas
func (h *Handler) HandleUpdateQuotas(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := r.PathValue("tenant_id")

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req quotaUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "request body is empty", nil)
			return
		}
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		return
	}

	if req.MaxMsgPerSec < 0 || req.MaxIntegrations < 0 || req.MaxStorageBytes < 0 {
		_ = writeError(w, http.StatusBadRequest, "ValidationError",
			"quota values must be non-negative (0 means unlimited)", nil)
		return
	}

	q := &TenantQuotas{
		TenantID:        tenantID,
		PlanName:        req.PlanName,
		MaxMsgPerSec:    req.MaxMsgPerSec,
		MaxIntegrations: req.MaxIntegrations,
		MaxStorageBytes: req.MaxStorageBytes,
	}
	if q.PlanName == "" {
		q.PlanName = "free"
	}
	if err := h.repo.UpdateTenantQuotas(ctx, q); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to save quotas", nil)
		return
	}

	SetAuditDetail(ctx, "plan_name", q.PlanName)
	SetAuditDetail(ctx, "max_msg_per_sec", q.MaxMsgPerSec)
	SetAuditDetail(ctx, "max_integrations", q.MaxIntegrations)
	SetAuditDetail(ctx, "max_storage_bytes", q.MaxStorageBytes)
	SetAuditAction(ctx, "quota.update")

	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: q})
}
