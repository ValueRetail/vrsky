package managementapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

// CreateTenantHandler handles POST /api/v1/tenants
func (h *Handler) CreateTenantHandler(w http.ResponseWriter, r *http.Request) {
	user := GetUserFromContext(r.Context())
	if user == nil {
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "authentication required", nil)
		return
	}

	var req CreateTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "failed to parse request", nil)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) < 3 {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", "workspace name must be at least 3 characters", nil)
		return
	}
	if len(name) > 255 {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", "workspace name must not exceed 255 characters", nil)
		return
	}

	slug := GenerateSlug(name)

	tenant, err := h.repo.CreateTenant(r.Context(), user.ID, name, slug)
	if err != nil {
		if err == ErrSlugAlreadyExists {
			_ = writeError(w, http.StatusConflict, "Conflict", "a workspace with a similar name already exists", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to create workspace", nil)
		return
	}

	// Enqueue NATS provisioning job if provisioner is available
	var jobID string
	if h.tenantProvisioner != nil {
		job, err := h.repo.CreateProvisioningJob(r.Context(), tenant.ID)
		if err == nil {
			jobID = job.ID
			_ = h.tenantProvisioner.Enqueue(ProvisionJob{
				TenantID:   tenant.ID,
				TenantSlug: tenant.Slug,
				JobID:      job.ID,
			})
		}
	}

	resp := map[string]interface{}{
		"tenant": TenantResponse{
			ID:                  tenant.ID,
			Name:                tenant.Name,
			Slug:                tenant.Slug,
			OwnerID:             tenant.OwnerID,
			SubscriptionPlan:    tenant.SubscriptionPlan,
			IsVerified:          tenant.IsVerified,
			MaxIntegrations:     tenant.MaxIntegrations,
			MaxMessagesPerMonth: tenant.MaxMessagesPerMonth,
			Status:              tenant.Status,
			NATSSlug:            tenant.NATSSlug,
			UserRole:            "owner",
			CreatedAt:           tenant.CreatedAt,
			UpdatedAt:           tenant.UpdatedAt,
		},
	}
	if jobID != "" {
		resp["job_id"] = jobID
	}
	_ = writeJSON(w, http.StatusCreated, resp)
}

// ListTenantsHandler handles GET /api/v1/tenants
func (h *Handler) ListTenantsHandler(w http.ResponseWriter, r *http.Request) {
	user := GetUserFromContext(r.Context())
	if user == nil {
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "authentication required", nil)
		return
	}

	tenants, err := h.repo.GetUserTenants(r.Context(), user.ID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to list workspaces", nil)
		return
	}
	if tenants == nil {
		tenants = []*TenantResponse{}
	}

	_ = writeJSON(w, http.StatusOK, map[string]interface{}{
		"tenants": tenants,
	})
}

// GetTenantHandler handles GET /api/v1/tenants/{tenant_id}
func (h *Handler) GetTenantHandler(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}
	role := GetTenantRoleFromContext(r.Context())

	_ = writeJSON(w, http.StatusOK, TenantResponse{
		ID:                  tenant.ID,
		Name:                tenant.Name,
		Slug:                tenant.Slug,
		OwnerID:             tenant.OwnerID,
		SubscriptionPlan:    tenant.SubscriptionPlan,
		IsVerified:          tenant.IsVerified,
		MaxIntegrations:     tenant.MaxIntegrations,
		MaxMessagesPerMonth: tenant.MaxMessagesPerMonth,
		Status:              tenant.Status,
		NATSSlug:            tenant.NATSSlug,
		UserRole:            role,
		CreatedAt:           tenant.CreatedAt,
		UpdatedAt:           tenant.UpdatedAt,
	})
}

// DeleteTenantHandler handles DELETE /api/v1/tenants/{tenant_id}
func (h *Handler) DeleteTenantHandler(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	if err := h.repo.DeleteTenant(r.Context(), tenant.ID); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to delete workspace", nil)
		return
	}

	_ = writeJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "workspace deleted"})
}
