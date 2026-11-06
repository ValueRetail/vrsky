package managementapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ValueRetail/vrsky/pkg/auth"
	"github.com/google/uuid"
)

// ============================================
// Auth HTTP Handlers
// ============================================

// loginDummyHash is a bcrypt hash of a password nobody knows. A login with
// an unknown email is verified against it so the request takes as long as a
// wrong password does; otherwise the response time says who has an account.
// Computed once, on first use, at the real cost.
var loginDummyHash = sync.OnceValue(func() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	h, _ := auth.HashPassword(hex.EncodeToString(b[:]))
	return h
})

// verify compares a password with a hash through the test seam.
func (h *Handler) verify(hashedPassword, password string) error {
	if h.verifyPassword != nil {
		return h.verifyPassword(hashedPassword, password)
	}
	return auth.VerifyPassword(hashedPassword, password)
}

// RegisterUser handles POST /api/v1/auth/register
func (h *Handler) RegisterUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Every sign-up request hashes a password, so every request counts here,
	// before the body is read. Never refunded: many sign-ups from one address
	// is the thing being limited. See auth_limit.go.
	addr := clientAddr(r)
	if wait, ok, first := h.authLimits.signup.Take(addr, h.authLimits.now()); !ok {
		h.refuseLimited(w, r, "signup", "address", addr, "", wait, first)
		return
	}

	// Parse request body
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err == io.EOF {
			_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "request body is empty", nil)
		} else {
			_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "failed to parse request", nil)
		}
		return
	}

	// Validate email
	if req.Email == "" || !isValidEmail(req.Email) {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", "valid email is required", nil)
		return
	}

	// Validate password
	if err := auth.ValidatePassword(req.Password); err != nil {
		_ = writeError(w, http.StatusUnprocessableEntity, "ValidationError", err.Error(), nil)
		return
	}

	// Validate full name
	if req.FullName == "" {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", "full name is required", nil)
		return
	}

	// Validate workspace name
	workspaceName := strings.TrimSpace(req.WorkspaceName)
	if workspaceName == "" {
		workspaceName = req.FullName + "'s Workspace"
	} else if len(workspaceName) < 3 {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", "workspace name must be at least 3 characters", nil)
		return
	}

	// Hash password
	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to process registration", nil)
		return
	}

	// Create user
	user := NewUser(strings.ToLower(req.Email), passwordHash, req.FullName)

	// Save to database
	if err := h.repo.CreateUser(ctx, user); err != nil {
		if errors.Is(err, auth.ErrEmailExists) {
			_ = writeError(w, http.StatusConflict, "Conflict", "email already registered", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to create user", nil)
		}
		// Log the event
		h.logAuthEvent(ctx, r, nil, req.Email, "registration", "failed", stringPtr(err.Error()))
		return
	}

	// Auto-create default tenant
	slug := GenerateSlug(workspaceName)
	tenant, err := h.repo.CreateTenant(ctx, user.ID, workspaceName, slug)
	if err != nil {
		// Provide feedback to the client that the default workspace was not created,
		// but keep the registration flow successful so the user can create a tenant later.
		w.Header().Add("X-Default-Workspace-Status", "creation_failed")
		w.Header().Add("X-Default-Workspace-Message", "Default workspace could not be created; you can create one later.")
		// Log but don't fail registration — user can create a tenant later
		h.logAuthEvent(ctx, r, &user.ID, req.Email, "tenant_creation", "failed", stringPtr(err.Error()))
	}

	// Auto-verify email (no email service configured yet)
	// TODO: When email service is added, remove auto-verify and send verification email instead
	if err := h.repo.VerifyUserEmail(ctx, user.ID); err != nil {
		// Non-fatal: user can still verify later
		h.logAuthEvent(ctx, r, &user.ID, req.Email, "auto_verify", "failed", stringPtr(err.Error()))
	}

	// Log success
	h.logAuthEvent(ctx, r, &user.ID, req.Email, "registration", "success", nil)

	// Build response
	response := map[string]interface{}{
		"success": true,
		"message": "Registration successful! You can now log in.",
		"user_id": user.ID,
	}
	if tenant != nil && tenant.TrialEndsAt != nil {
		response["trial_ends_at"] = tenant.TrialEndsAt
	}
	if tenant != nil {
		response["tenant"] = TenantResponse{
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
			BillingStatus:       tenant.BillingStatus,
			TrialEndsAt:         tenant.TrialEndsAt,
			UserRole:            "owner",
			CreatedAt:           tenant.CreatedAt,
			UpdatedAt:           tenant.UpdatedAt,
		}
	}

	_ = writeJSON(w, http.StatusCreated, response)
}

// VerifyEmail handles GET /api/v1/auth/verify-email?token=xxx
func (h *Handler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get token from query params
	rawToken := r.URL.Query().Get("token")
	if rawToken == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "token is required", nil)
		return
	}

	// Hash the token to look up in database
	hashedToken := auth.HashToken(rawToken)

	// Whose token it is, for the security log; the checks are in Use… below.
	var userID *string
	if tok, err := h.repo.GetEmailVerificationToken(ctx, hashedToken); err == nil {
		userID = &tok.UserID
	}
	email := h.auditEmail(ctx, userID)

	// Use the verification token (marks as used and verifies email)
	if err := h.repo.UseEmailVerificationToken(ctx, hashedToken); err != nil {
		h.logAuthEvent(ctx, r, userID, email, "email_verified", "failed", stringPtr(tokenFailure(err)))
		if errors.Is(err, auth.ErrInvalidToken) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "invalid verification link", nil)
		} else if errors.Is(err, auth.ErrTokenUsed) {
			_ = writeError(w, http.StatusGone, "Gone", "verification link already used", nil)
		} else if errors.Is(err, auth.ErrTokenExpired) {
			_ = writeError(w, http.StatusBadRequest, "Expired", "verification link has expired", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to verify email", nil)
		}
		return
	}
	h.logAuthEvent(ctx, r, userID, email, "email_verified", "success", nil)

	// Redirect to login page with success message
	// In production, use a proper redirect URL from config
	http.Redirect(w, r, "/auth/login?verified=true", http.StatusFound)
}

// LoginUser handles POST /api/v1/auth/login
func (h *Handler) LoginUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// One attempt is spent per address before the body is read, and one per
	// account once the email is known; both are handed back on success. A
	// blocked account still admits an address that has logged in to it
	// before. See auth_limit.go.
	limits := h.authLimits
	now := limits.now()
	addr := clientAddr(r)
	if wait, ok, first := limits.address.Take(addr, now); !ok {
		h.refuseLimited(w, r, "login", "address", addr, "", wait, first)
		return
	}

	// Parse request body
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err == io.EOF {
			_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "request body is empty", nil)
		} else {
			_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "failed to parse request", nil)
		}
		return
	}

	// Validate inputs
	if req.Email == "" || req.Password == "" {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", "email and password are required", nil)
		return
	}

	// The account key is the submitted email, registered or not, so the
	// answer is the same for both and says nothing about who has an account.
	account := strings.ToLower(strings.TrimSpace(req.Email))
	wait, accountSpent, firstBlock := limits.account.Take(account, now)
	if !accountSpent && !h.knownAddress(ctx, account, addr) {
		h.refuseLimited(w, r, "login", "account", account, req.Email, wait, firstBlock)
		return
	}
	refund := func() {
		limits.address.Refund(addr, now)
		if accountSpent {
			limits.account.Refund(account, now)
		}
	}

	// Get user by email
	user, err := h.repo.GetUserByEmail(ctx, account)
	if err != nil {
		// Don't reveal whether the user exists — not in the answer, and not
		// in the time it takes: an unknown email still costs one verify.
		_ = h.verify(loginDummyHash(), req.Password)
		h.logAuthEvent(ctx, r, nil, req.Email, "login", "failed", stringPtr("user not found"))
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "invalid email or password", nil)
		return
	}

	// Check if user can login
	if err := user.CanLogin(); err != nil {
		h.logAuthEvent(ctx, r, &user.ID, req.Email, "login", "failed", stringPtr(err.Error()))
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", err.Error(), nil)
		return
	}

	// Verify password
	if err := h.verify(user.PasswordHash, req.Password); err != nil {
		h.logAuthEvent(ctx, r, &user.ID, req.Email, "login", "failed", stringPtr("invalid password"))
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "invalid email or password", nil)
		return
	}

	// The password was right: this was not a failed attempt.
	refund()

	// Generate session token
	rawToken, hashedToken, err := auth.GenerateSessionToken()
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to create session", nil)
		return
	}

	// Create session
	created := time.Now().UTC()
	session := &Session{
		ID:           uuid.New().String(),
		UserID:       user.ID,
		TokenHash:    hashedToken,
		IPAddress:    optString(getClientIP(r)),
		UserAgent:    stringPtr(r.UserAgent()),
		CreatedAt:    created,
		ExpiresAt:    auth.CalculateSessionExpiry(),
		LastActivity: created,
		IsActive:     true,
	}

	if err := h.repo.CreateSession(ctx, session); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to create session", nil)
		return
	}

	// Update last login time
	_ = h.repo.UpdateUserLastLogin(ctx, user.ID)

	// Log success
	h.logAuthEvent(ctx, r, &user.ID, req.Email, "login", "success", nil)

	// Set the session cookie for browser clients. API clients can still
	// read SessionToken from the response body and send it as a Bearer
	// header — both paths are accepted by session middleware (#68).
	setSessionCookie(w, rawToken, session.ExpiresAt)

	// Return response
	_ = writeJSON(w, http.StatusOK, LoginResponse{
		Success:      true,
		SessionToken: rawToken,
		User:         user.ToResponse(),
		ExpiresAt:    session.ExpiresAt,
	})
}

// GetMe handles GET /api/v1/auth/me
func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get session and user from context (set by SessionMiddleware)
	user, session, err := h.getAuthenticatedUser(ctx, r)
	if err != nil {
		writeSessionError(w, r, err)
		return
	}

	// Fetch user's tenants. A failed lookup is not an empty list: answer 503 so
	// the UI offers a retry instead of showing a user with no workspaces (#645).
	tenants, err := h.repo.GetUserTenants(ctx, user.ID)
	if err != nil {
		writeServiceUnavailable(w, r, "tenant lookup failed", err)
		return
	}
	if tenants == nil {
		tenants = []*TenantResponse{}
	}

	var currentTenant *TenantResponse
	if len(tenants) > 0 {
		currentTenant = tenants[0]
	}

	_ = writeJSON(w, http.StatusOK, MeResponse{
		User:               user.ToResponse(),
		SessionExpiresAt:   session.ExpiresAt,
		Tenants:            tenants,
		CurrentTenant:      currentTenant,
		IsPlatformOperator: h.isOperator(user),
	})
}

// LogoutUser handles POST /api/v1/auth/logout. It runs behind
// SessionAuthMiddleware and ends the session the middleware validated: the
// one this request authenticated with, whichever of cookie or Authorization
// header carried it.
func (h *Handler) LogoutUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Always clear the cookie so browser logout leaves no cookie behind.
	clearCookie(w, sessionCookieName)

	session := GetSessionFromContext(ctx)
	if session == nil {
		// Only reachable if the route lost its middleware.
		writeAuthError(w, http.StatusUnauthorized, "missing or invalid authorization header")
		return
	}
	userID := &session.UserID
	var email string
	if user := GetUserFromContext(ctx); user != nil {
		email = user.Email
	}
	if err := h.repo.InvalidateSession(ctx, session.TokenHash); err != nil {
		h.logAuthEvent(ctx, r, userID, email, "logout", "failed", stringPtr(err.Error()))
		// Don't fail on logout errors.
		_ = writeJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "logged out"})
		return
	}
	h.logAuthEvent(ctx, r, userID, email, "logout", "success", nil)
	_ = writeJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "logged out successfully"})
}

// ForgotPassword handles POST /api/v1/auth/forgot-password
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Every request costs an attempt per address (sign-up numbers) and, once
	// the email is known, per email: this endpoint sends mail, and the limits
	// are what keep it from flooding an inbox or the SMTP quota.
	limits := h.authLimits
	now := limits.now()
	addr := clientAddr(r)
	if wait, ok, first := limits.forgot.Take(addr, now); !ok {
		h.refuseLimited(w, r, "forgot_password", "address", addr, "", wait, first)
		return
	}

	var req ForgotPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "failed to parse request", nil)
		return
	}
	if req.Email == "" {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", "email is required", nil)
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if wait, ok, first := limits.forgotEmail.Take(email, now); !ok {
		h.refuseLimited(w, r, "forgot_password", "account", email, req.Email, wait, first)
		return
	}

	// An installation that cannot send mail says so — before any lookup, so
	// the answer is the same whether or not the address has an account.
	if !h.canSendResetMail() {
		_ = writeError(w, http.StatusServiceUnavailable, "ResetUnavailable", resetUnavailableMessage, nil)
		return
	}

	// From here the answer is always the same, to prevent email enumeration.
	defer func() {
		_ = writeJSON(w, http.StatusOK, MessageResponse{
			Success: true,
			Message: "If that address has an account, a password reset link is on its way",
		})
	}()

	user, err := h.repo.GetUserByEmail(ctx, email)
	if err != nil {
		h.logAuthEvent(ctx, r, nil, req.Email, "password_reset_requested", "not_found", nil)
		return
	}
	rawToken, hashedToken, err := auth.GeneratePasswordResetToken()
	if err != nil {
		return
	}
	expiresAt := auth.CalculatePasswordResetTokenExpiry()
	if err := h.repo.CreatePasswordResetToken(ctx, user.ID, hashedToken, expiresAt); err != nil {
		return
	}
	body := resetMailBody(user.FullName, h.resetLink(rawToken), expiresAt)
	if err := h.mailer.Send(ctx, user.Email, "Reset your VRSky password", body); err != nil {
		slog.Default().Warn("password reset mail not sent", "error", err)
		h.logAuthEvent(ctx, r, &user.ID, req.Email, "password_reset_requested", "send_failed", stringPtr(err.Error()))
		return
	}
	h.logAuthEvent(ctx, r, &user.ID, req.Email, "password_reset_requested", "success", nil)
}

// ResetPassword handles POST /api/v1/auth/reset-password
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse request body
	var req ResetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "failed to parse request", nil)
		return
	}

	// Validate inputs
	if req.Token == "" {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", "token is required", nil)
		return
	}

	if err := auth.ValidatePassword(req.NewPassword); err != nil {
		_ = writeError(w, http.StatusUnprocessableEntity, "ValidationError", err.Error(), nil)
		return
	}

	// Hash the new password
	newPasswordHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to process request", nil)
		return
	}

	// Hash the token to look up in database
	hashedToken := auth.HashToken(req.Token)

	// Whose token it is, for the security log; the checks are in Use… below.
	var userID *string
	if tok, err := h.repo.GetPasswordResetToken(ctx, hashedToken); err == nil {
		userID = &tok.UserID
	}
	email := h.auditEmail(ctx, userID)

	// Use the reset token (validates, updates password, invalidates sessions)
	if err := h.repo.UsePasswordResetToken(ctx, hashedToken, newPasswordHash); err != nil {
		h.logAuthEvent(ctx, r, userID, email, "password_reset", "failed", stringPtr(tokenFailure(err)))
		if errors.Is(err, auth.ErrInvalidToken) {
			_ = writeError(w, http.StatusBadRequest, "InvalidToken", "invalid or expired reset link", nil)
		} else if errors.Is(err, auth.ErrTokenUsed) {
			_ = writeError(w, http.StatusBadRequest, "TokenUsed", "reset link already used", nil)
		} else if errors.Is(err, auth.ErrTokenExpired) {
			_ = writeError(w, http.StatusBadRequest, "TokenExpired", "reset link has expired", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to reset password", nil)
		}
		return
	}
	h.logAuthEvent(ctx, r, userID, email, "password_reset", "success", nil)

	_ = writeJSON(w, http.StatusOK, MessageResponse{
		Success: true,
		Message: "Password reset successful. Please login with your new password.",
	})
}

// ChangePassword handles POST /api/v1/auth/change-password
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get authenticated user
	user, _, err := h.getAuthenticatedUser(ctx, r)
	if err != nil {
		writeSessionError(w, r, err)
		return
	}

	// Parse request body
	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "failed to parse request", nil)
		return
	}

	// Validate new password
	if err := auth.ValidatePassword(req.NewPassword); err != nil {
		_ = writeError(w, http.StatusUnprocessableEntity, "ValidationError", err.Error(), nil)
		return
	}

	// A wrong current password costs the user an attempt (login account
	// numbers); a right one hands it back. Without this a stolen session
	// could grind the password out of this endpoint at bcrypt speed.
	limits := h.authLimits
	now := limits.now()
	if wait, ok, first := limits.changePassword.Take(user.ID, now); !ok {
		h.refuseLimited(w, r, "change_password", "account", user.ID, user.Email, wait, first)
		return
	}
	if err := h.verify(user.PasswordHash, req.CurrentPassword); err != nil {
		h.logAuthEvent(ctx, r, &user.ID, user.Email, "password_change", "failed", stringPtr("current password is incorrect"))
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "current password is incorrect", nil)
		return
	}
	limits.changePassword.Refund(user.ID, now)

	// Hash new password
	newPasswordHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to process request", nil)
		return
	}

	// Update password
	if err := h.repo.UpdateUserPassword(ctx, user.ID, newPasswordHash); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to update password", nil)
		return
	}

	// Invalidate all sessions (force re-login everywhere)
	_ = h.repo.InvalidateAllUserSessions(ctx, user.ID)

	// Log success
	h.logAuthEvent(ctx, r, &user.ID, user.Email, "password_changed", "success", nil)

	_ = writeJSON(w, http.StatusOK, MessageResponse{
		Success: true,
		Message: "Password changed. You've been logged out everywhere. Please login again.",
	})
}

// DeleteAccount handles DELETE /api/v1/auth/me
func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get authenticated user
	user, _, err := h.getAuthenticatedUser(ctx, r)
	if err != nil {
		writeSessionError(w, r, err)
		return
	}

	// Soft-delete user and invalidate all sessions
	if err := h.repo.DeleteUser(ctx, user.ID); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to delete account", nil)
		return
	}

	// Log the event
	h.logAuthEvent(ctx, r, &user.ID, user.Email, "account_deleted", "success", nil)

	_ = writeJSON(w, http.StatusOK, MessageResponse{
		Success: true,
		Message: "Account deleted successfully",
	})
}

// ============================================
// Helper Functions
// ============================================

// getAuthenticatedUser extracts and validates the session from the request.
// It uses the cookie-aware extractor so handlers that re-derive auth (GetMe,
// ChangePassword, DeleteAccount) honor the vrsky_session cookie set by the
// email/password and OIDC login paths — matching SessionAuthMiddleware, which
// already accepts that cookie. Using the header-only extractor here made these
// endpoints 401 ("no session token provided") for cookie-only sessions even
// though the shared middleware had already authenticated the request.
func (h *Handler) getAuthenticatedUser(ctx context.Context, r *http.Request) (*User, *Session, error) {
	token := extractBearerTokenFromHeader(r)
	if token == "" {
		return nil, nil, errNoSessionToken
	}

	hashedToken := auth.HashToken(token)
	session, user, err := h.repo.ValidateSession(ctx, hashedToken)
	if err != nil {
		return nil, nil, err
	}

	return user, session, nil
}

// getClientIP is the client address for audit rows and sessions (see
// clientIPAddr); empty when unknown.
func getClientIP(r *http.Request) string {
	return clientIPString(r)
}

// optString is stringPtr for a value that may be absent: nil for "".
func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// isValidEmail performs a basic email validation
func isValidEmail(email string) bool {
	// Basic validation - contains @ and at least one dot after @
	atIdx := strings.Index(email, "@")
	if atIdx < 1 || atIdx >= len(email)-1 {
		return false
	}
	domain := email[atIdx+1:]
	return strings.Contains(domain, ".") && !strings.HasSuffix(domain, ".")
}

// stringPtr returns a pointer to a string
func stringPtr(s string) *string {
	return &s
}

// auditEmail is the email of the user a token or session belongs to, for a
// security-log row; empty when the user is unknown or cannot be read.
func (h *Handler) auditEmail(ctx context.Context, userID *string) string {
	if userID == nil {
		return ""
	}
	user, err := h.repo.GetUserByID(ctx, *userID)
	if err != nil {
		return ""
	}
	return user.Email
}

// tokenFailure is the security-log reason for a token that could not be
// used: a fixed phrase for the known cases, so nothing from the request ends
// up in the row.
func tokenFailure(err error) string {
	switch {
	case errors.Is(err, auth.ErrInvalidToken):
		return "invalid token"
	case errors.Is(err, auth.ErrTokenUsed):
		return "token already used"
	case errors.Is(err, auth.ErrTokenExpired):
		return "token expired"
	default:
		return err.Error()
	}
}

// Column sizes in auth_audit_log (000005_auth_system.up.sql). Postgres
// counts characters, not bytes.
const (
	auditEmailMax     = 255
	auditUserAgentMax = 500
)

// auditText makes a client-supplied value storable in auth_audit_log: valid
// UTF-8, no NUL (Postgres rejects both), at most max characters (0: no
// limit). Without it a long user agent or an email with a NUL in it makes
// the insert fail, and the event leaves no row.
func auditText(s string, max int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "\x00", "")
	if max > 0 && utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

// logAuthEvent logs an authentication event to the audit log
func (h *Handler) logAuthEvent(ctx context.Context, r *http.Request, userID *string, email, eventType, status string, errorReason *string) {
	if errorReason != nil {
		errorReason = stringPtr(auditText(*errorReason, 0))
	}
	log := &AuthAuditLog{
		UserID:      userID,
		Email:       auditText(email, auditEmailMax),
		EventType:   eventType,
		Status:      status,
		ErrorReason: errorReason,
		IPAddress:   optString(getClientIP(r)),
		UserAgent:   stringPtr(auditText(r.UserAgent(), auditUserAgentMax)),
	}
	// Audit logging never fails the request, but a lost row is logged. Only
	// the event and the error: the row's contents stay out of the log.
	if err := h.repo.CreateAuthAuditLog(ctx, log); err != nil {
		slog.Default().Warn("auth audit row not written",
			"event_type", eventType, "status", status, "error", err)
	}
}
