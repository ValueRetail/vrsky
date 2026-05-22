package managementapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Tenant members admin (#69).
//
//	GET    /api/v1/tenants/{tenant_id}/members            list
//	PUT    /api/v1/tenants/{tenant_id}/members/{user_id}  set role
//	DELETE /api/v1/tenants/{tenant_id}/members/{user_id}  remove
//
// Wired in handler.go::RegisterAuthRoutes behind session + tenant member
// + admin role; the change endpoints additionally require `owner`.

type setRoleRequest struct {
	Role string `json:"role"`
}

// HandleListMembers: GET /api/v1/tenants/{tenant_id}/members
func (h *Handler) HandleListMembers(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant_id")
	members, err := h.repo.ListTenantMembers(r.Context(), tenantID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to list members", nil)
		return
	}
	if members == nil {
		members = []*TenantMember{}
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: members})
}

// HandleSetMemberRole: PUT /api/v1/tenants/{tenant_id}/members/{user_id}
func (h *Handler) HandleSetMemberRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := r.PathValue("tenant_id")
	userID := r.PathValue("user_id")

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req setRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "request body is empty", nil)
			return
		}
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		return
	}
	req.Role = strings.ToLower(strings.TrimSpace(req.Role))
	if _, ok := roleHierarchy[req.Role]; !ok {
		_ = writeError(w, http.StatusBadRequest, "InvalidRole",
			"role must be one of: viewer, editor, admin, owner", nil)
		return
	}

	if err := h.repo.SetTenantMemberRole(ctx, tenantID, userID, req.Role); err != nil {
		if errors.Is(err, ErrLastOwner) {
			_ = writeError(w, http.StatusConflict, "LastOwner",
				"cannot demote the last owner of the tenant; promote another owner first", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}

	// Audit detail — the middleware logs the action, this enriches the row.
	SetAuditDetail(ctx, "target_user_id", userID)
	SetAuditDetail(ctx, "new_role", req.Role)
	SetAuditAction(ctx, "member.role_change")

	w.WriteHeader(http.StatusNoContent)
}

// HandleRemoveMember: DELETE /api/v1/tenants/{tenant_id}/members/{user_id}
func (h *Handler) HandleRemoveMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := r.PathValue("tenant_id")
	userID := r.PathValue("user_id")

	if err := h.repo.RemoveTenantMember(ctx, tenantID, userID); err != nil {
		if errors.Is(err, ErrLastOwner) {
			_ = writeError(w, http.StatusConflict, "LastOwner",
				"cannot remove the last owner of the tenant; promote another owner first", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}

	SetAuditDetail(ctx, "target_user_id", userID)
	SetAuditAction(ctx, "member.remove")

	w.WriteHeader(http.StatusNoContent)
}
