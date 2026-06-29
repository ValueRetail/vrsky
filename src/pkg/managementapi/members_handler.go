package managementapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// Tenant members admin (#69).
//
//	GET    /api/v1/tenants/{tenant_id}/members            list
//	POST   /api/v1/tenants/{tenant_id}/members            add by email (#130)
//	PUT    /api/v1/tenants/{tenant_id}/members/{user_id}  set role
//	DELETE /api/v1/tenants/{tenant_id}/members/{user_id}  remove
//
// Wired in handler.go::RegisterAuthRoutes behind session + tenant member
// + admin role; the change endpoints additionally require `owner`.

type setRoleRequest struct {
	Role string `json:"role"`
}

type addMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// HandleAddMember: POST /api/v1/tenants/{tenant_id}/members
// Adds an already-registered user (looked up by email) to the tenant. This is
// the interim "add by email" path (#130) — there is no email-invite/accept
// round trip yet, so the invitee must already have an account.
func (h *Handler) HandleAddMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := r.PathValue("tenant_id")

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req addMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "request body is empty", nil)
			return
		}
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Role = strings.ToLower(strings.TrimSpace(req.Role))
	if req.Email == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "email is required", nil)
		return
	}
	if _, ok := roleHierarchy[req.Role]; !ok {
		_ = writeError(w, http.StatusBadRequest, "InvalidRole",
			"role must be one of: viewer, editor, admin, owner", nil)
		return
	}

	user, err := h.repo.GetUserByEmail(ctx, req.Email)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			_ = writeError(w, http.StatusNotFound, "UserNotFound",
				"no registered user with that email; they must create an account first", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to look up user", nil)
		return
	}

	if err := h.repo.AddTenantMember(ctx, tenantID, user.ID, req.Role); err != nil {
		if errors.Is(err, ErrAlreadyMember) {
			_ = writeError(w, http.StatusConflict, "AlreadyMember",
				"that user is already a member of this workspace", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}

	SetAuditDetail(ctx, "target_user_id", user.ID)
	SetAuditDetail(ctx, "new_role", req.Role)
	SetAuditAction(ctx, "member.add")

	_ = writeJSON(w, http.StatusCreated, SuccessResponse{Data: &TenantMember{
		UserID:   user.ID,
		TenantID: tenantID,
		Email:    user.Email,
		FullName: user.FullName,
		Role:     req.Role,
	}})
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
