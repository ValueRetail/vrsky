package managementapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Member-invitation endpoints (#130). These extend the "add member by email"
// path (which only works for already-registered users) with a pending-invite
// flow for emails that have not signed up yet: create, list, resend, revoke,
// and accept.
//
// The invite persistence is accessed via the narrow InviteStore interface,
// type-asserted from h.repo, so the broad Repository interface and its mocks
// stay untouched.

type createInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type acceptInviteRequest struct {
	Token string `json:"token"`
}

// inviteStore returns the InviteStore backing this handler, or false if the
// repository doesn't support invites (e.g. a narrow test mock).
func (h *Handler) inviteStore() (InviteStore, bool) {
	is, ok := h.repo.(InviteStore)
	return is, ok
}

// HandleCreateInvite: POST /api/v1/tenants/{tenant_id}/invites
// If the email already belongs to a registered user, they're added to the
// workspace directly (the existing add path). Otherwise a pending invite is
// created and returned with its accept token.
func (h *Handler) HandleCreateInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := r.PathValue("tenant_id")

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req createInviteRequest
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
	if req.Email == "" || !isValidEmail(req.Email) {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "a valid email is required", nil)
		return
	}
	if _, ok := roleHierarchy[req.Role]; !ok {
		_ = writeError(w, http.StatusBadRequest, "InvalidRole", "role must be one of: viewer, editor, admin, owner", nil)
		return
	}

	// If the email is already a registered user, add them directly — no invite
	// round trip needed.
	if user, err := h.repo.GetUserByEmail(ctx, req.Email); err == nil && user != nil {
		if aerr := h.repo.AddTenantMember(ctx, tenantID, user.ID, req.Role); aerr != nil {
			if errors.Is(aerr, ErrAlreadyMember) {
				_ = writeError(w, http.StatusConflict, "AlreadyMember", "that user is already a member of this workspace", nil)
				return
			}
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", aerr.Error(), nil)
			return
		}
		SetAuditAction(ctx, "member.add")
		SetAuditDetail(ctx, "target_user_id", user.ID)
		_ = writeJSON(w, http.StatusCreated, SuccessResponse{Data: map[string]any{
			"added_member": &TenantMember{UserID: user.ID, TenantID: tenantID, Email: user.Email, FullName: user.FullName, Role: req.Role},
		}})
		return
	}

	store, ok := h.inviteStore()
	if !ok {
		_ = writeError(w, http.StatusNotImplemented, "Unsupported", "invites are not supported by this server", nil)
		return
	}

	var invitedBy string
	if u := GetUserFromContext(ctx); u != nil {
		invitedBy = u.ID
	}

	inv, err := store.CreateInvite(ctx, tenantID, req.Email, req.Role, invitedBy)
	if err != nil {
		if errors.Is(err, ErrInvitePending) {
			_ = writeError(w, http.StatusConflict, "InvitePending", "a pending invite already exists for that email", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}

	SetAuditAction(ctx, "member.invite")
	SetAuditDetail(ctx, "invite_email", req.Email)
	_ = writeJSON(w, http.StatusCreated, SuccessResponse{Data: inv})
}

// HandleListInvites: GET /api/v1/tenants/{tenant_id}/invites
func (h *Handler) HandleListInvites(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant_id")
	store, ok := h.inviteStore()
	if !ok {
		_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: []*TenantInvite{}})
		return
	}
	invites, err := store.ListInvites(r.Context(), tenantID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}
	// Never expose tokens in the list — only create/resend return them.
	for _, inv := range invites {
		inv.Token = ""
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: invites})
}

// HandleResendInvite: POST /api/v1/tenants/{tenant_id}/invites/{invite_id}/resend
func (h *Handler) HandleResendInvite(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant_id")
	inviteID := r.PathValue("invite_id")
	store, ok := h.inviteStore()
	if !ok {
		_ = writeError(w, http.StatusNotImplemented, "Unsupported", "invites are not supported by this server", nil)
		return
	}
	inv, err := store.ResendInvite(r.Context(), tenantID, inviteID)
	if err != nil {
		if errors.Is(err, ErrInviteNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "no pending invite with that id", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}
	SetAuditAction(r.Context(), "member.invite.resend")
	SetAuditDetail(r.Context(), "invite_id", inviteID)
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: inv})
}

// HandleRevokeInvite: DELETE /api/v1/tenants/{tenant_id}/invites/{invite_id}
func (h *Handler) HandleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant_id")
	inviteID := r.PathValue("invite_id")
	store, ok := h.inviteStore()
	if !ok {
		_ = writeError(w, http.StatusNotImplemented, "Unsupported", "invites are not supported by this server", nil)
		return
	}
	if err := store.RevokeInvite(r.Context(), tenantID, inviteID); err != nil {
		if errors.Is(err, ErrInviteNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "no pending invite with that id", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}
	SetAuditAction(r.Context(), "member.invite.revoke")
	SetAuditDetail(r.Context(), "invite_id", inviteID)
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: map[string]string{"status": "revoked"}})
}

// HandleAcceptInvite: POST /api/v1/invites/accept  { "token": "..." }
// Authenticated (session) but NOT tenant-scoped — the token is the capability.
// The logged-in user's email must match the invite; on success they're added
// to the tenant and the invite is marked accepted.
func (h *Handler) HandleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	store, ok := h.inviteStore()
	if !ok {
		_ = writeError(w, http.StatusNotImplemented, "Unsupported", "invites are not supported by this server", nil)
		return
	}

	user := GetUserFromContext(ctx)
	if user == nil {
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "sign in to accept an invite", nil)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var req acceptInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "invalid request body", nil)
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "token is required", nil)
		return
	}

	inv, err := store.GetInviteByToken(ctx, req.Token)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "InvalidInvite", "invite not found or already used", nil)
		return
	}
	if inv.Status != "pending" {
		_ = writeError(w, http.StatusConflict, "InviteUsed", "this invite has already been "+inv.Status, nil)
		return
	}
	if time.Now().After(inv.ExpiresAt) {
		_ = writeError(w, http.StatusGone, "InviteExpired", "this invite has expired; ask an admin to resend it", nil)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(user.Email), strings.TrimSpace(inv.Email)) {
		_ = writeError(w, http.StatusForbidden, "EmailMismatch", "this invite was sent to a different email address", nil)
		return
	}

	if aerr := h.repo.AddTenantMember(ctx, inv.TenantID, user.ID, inv.Role); aerr != nil && !errors.Is(aerr, ErrAlreadyMember) {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", aerr.Error(), nil)
		return
	}
	if merr := store.MarkInviteAccepted(ctx, inv.ID); merr != nil {
		// Membership already granted; log-and-continue rather than fail the user.
		SetAuditDetail(ctx, "invite_accept_mark_error", merr.Error())
	}

	SetAuditAction(ctx, "member.invite.accept")
	SetAuditDetail(ctx, "tenant_id", inv.TenantID)
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: &TenantMember{
		UserID: user.ID, TenantID: inv.TenantID, Email: user.Email, FullName: user.FullName, Role: inv.Role,
	}})
}
