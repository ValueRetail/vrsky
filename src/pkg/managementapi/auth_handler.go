package managementapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/auth"
	"github.com/google/uuid"
)

// ============================================
// Auth HTTP Handlers
// ============================================

// RegisterUser handles POST /api/v1/auth/register
func (h *Handler) RegisterUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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
		// Log but don't fail registration — user can create a tenant later
		h.logAuthEvent(ctx, r, &user.ID, req.Email, "tenant_creation", "failed", stringPtr(err.Error()))
	}

	// Generate email verification token
	rawToken, hashedToken, err := auth.GenerateVerificationToken()
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to generate verification token", nil)
		return
	}

	// Save verification token
	expiresAt := auth.CalculateVerificationTokenExpiry()
	if err := h.repo.CreateEmailVerificationToken(ctx, user.ID, hashedToken, expiresAt); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to create verification token", nil)
		return
	}

	// Log success
	h.logAuthEvent(ctx, r, &user.ID, req.Email, "registration", "success", nil)

	// Build response
	response := map[string]interface{}{
		"success":            true,
		"message":            "Check your email to verify your account",
		"user_id":            user.ID,
		"verification_token": rawToken, // TODO: Remove in production - only for testing
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

	// Use the verification token (marks as used and verifies email)
	if err := h.repo.UseEmailVerificationToken(ctx, hashedToken); err != nil {
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

	// Redirect to login page with success message
	// In production, use a proper redirect URL from config
	http.Redirect(w, r, "/auth/login?verified=true", http.StatusFound)
}

// LoginUser handles POST /api/v1/auth/login
func (h *Handler) LoginUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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

	// Get user by email
	user, err := h.repo.GetUserByEmail(ctx, strings.ToLower(req.Email))
	if err != nil {
		// Don't reveal if user exists or not
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
	if err := auth.VerifyPassword(user.PasswordHash, req.Password); err != nil {
		h.logAuthEvent(ctx, r, &user.ID, req.Email, "login", "failed", stringPtr("invalid password"))
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "invalid email or password", nil)
		return
	}

	// Generate session token
	rawToken, hashedToken, err := auth.GenerateSessionToken()
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to create session", nil)
		return
	}

	// Create session
	now := time.Now().UTC()
	session := &Session{
		ID:           uuid.New().String(),
		UserID:       user.ID,
		TokenHash:    hashedToken,
		IPAddress:    stringPtr(getClientIP(r)),
		UserAgent:    stringPtr(r.UserAgent()),
		CreatedAt:    now,
		ExpiresAt:    auth.CalculateSessionExpiry(),
		LastActivity: now,
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
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", err.Error(), nil)
		return
	}

	// Fetch user's tenants
	tenants, err := h.repo.GetUserTenants(ctx, user.ID)
	if err != nil {
		tenants = []*TenantResponse{}
	}
	if tenants == nil {
		tenants = []*TenantResponse{}
	}

	var currentTenant *TenantResponse
	if len(tenants) > 0 {
		currentTenant = tenants[0]
	}

	_ = writeJSON(w, http.StatusOK, MeResponse{
		User:             user.ToResponse(),
		SessionExpiresAt: session.ExpiresAt,
		Tenants:          tenants,
		CurrentTenant:    currentTenant,
	})
}

// LogoutUser handles POST /api/v1/auth/logout
func (h *Handler) LogoutUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get session token from header
	token := extractBearerToken(r)
	if token == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "no session token provided", nil)
		return
	}

	// Hash the token
	hashedToken := auth.HashToken(token)

	// Invalidate the session
	if err := h.repo.InvalidateSession(ctx, hashedToken); err != nil {
		// Don't fail on logout errors
		_ = writeJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "logged out"})
		return
	}

	_ = writeJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "logged out successfully"})
}

// ForgotPassword handles POST /api/v1/auth/forgot-password
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse request body
	var req ForgotPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "failed to parse request", nil)
		return
	}

	if req.Email == "" {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", "email is required", nil)
		return
	}

	// Always return success to prevent email enumeration
	defer func() {
		_ = writeJSON(w, http.StatusOK, MessageResponse{
			Success: true,
			Message: "If the email exists, a password reset link will be sent",
		})
	}()

	// Get user by email
	user, err := h.repo.GetUserByEmail(ctx, strings.ToLower(req.Email))
	if err != nil {
		h.logAuthEvent(ctx, r, nil, req.Email, "password_reset_requested", "not_found", nil)
		return
	}

	// Generate password reset token
	rawToken, hashedToken, err := auth.GeneratePasswordResetToken()
	if err != nil {
		return
	}

	// Save reset token
	expiresAt := auth.CalculatePasswordResetTokenExpiry()
	if err := h.repo.CreatePasswordResetToken(ctx, user.ID, hashedToken, expiresAt); err != nil {
		return
	}

	// Log success
	h.logAuthEvent(ctx, r, &user.ID, req.Email, "password_reset_requested", "success", nil)

	// TODO: Send password reset email
	// For now, we just log the token (in production, send via email)
	_ = rawToken // Use this token in the email link
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

	// Use the reset token (validates, updates password, invalidates sessions)
	if err := h.repo.UsePasswordResetToken(ctx, hashedToken, newPasswordHash); err != nil {
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
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", err.Error(), nil)
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

	// Verify current password
	if err := auth.VerifyPassword(user.PasswordHash, req.CurrentPassword); err != nil {
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "current password is incorrect", nil)
		return
	}

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

// ============================================
// Helper Functions
// ============================================

// getAuthenticatedUser extracts and validates the session from the request
func (h *Handler) getAuthenticatedUser(ctx context.Context, r *http.Request) (*User, *Session, error) {
	token := extractBearerToken(r)
	if token == "" {
		return nil, nil, errors.New("no session token provided")
	}

	hashedToken := auth.HashToken(token)
	session, user, err := h.repo.ValidateSession(ctx, hashedToken)
	if err != nil {
		return nil, nil, err
	}

	return user, session, nil
}

// extractBearerToken extracts the bearer token from the Authorization header
func extractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return ""
	}

	return parts[1]
}

// getClientIP extracts the client IP address from the request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header first (for proxied requests)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}

	// Check X-Real-IP header
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return xrip
	}

	// Fall back to RemoteAddr
	return strings.Split(r.RemoteAddr, ":")[0]
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

// logAuthEvent logs an authentication event to the audit log
func (h *Handler) logAuthEvent(ctx context.Context, r *http.Request, userID *string, email, eventType, status string, errorReason *string) {
	log := &AuthAuditLog{
		UserID:      userID,
		Email:       email,
		EventType:   eventType,
		Status:      status,
		ErrorReason: errorReason,
		IPAddress:   stringPtr(getClientIP(r)),
		UserAgent:   stringPtr(r.UserAgent()),
	}
	// Ignore errors - audit logging should not fail operations
	_ = h.repo.CreateAuthAuditLog(ctx, log)
}
