package managementapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// RBAC for tenant-header-scoped routes (Phase 1D / #69).
//
// The connection-management routes authenticate via `X-Tenant-ID` plus a
// Bearer credential. The credential is either:
//
//   - a session token (issued by /auth/login or the OIDC callback), or
//   - a tenant API key (machine-to-machine).
//
// Existing TenantIDMiddleware only checks that the header is non-empty.
// This module adds the missing identity → role resolution and a
// minimum-role gate that wraps every mutating handler.

// roleContextKey stores the resolved role of the principal calling the
// current request.
type roleContextKey struct{}

// withPrincipalRole stamps the role on the request context.
func withPrincipalRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleContextKey{}, role)
}

// RequireTenantRoleFromHeader returns a middleware that resolves the
// principal and rejects with 403 if the principal's role in the tenant
// (taken from the X-Tenant-ID header) is below minRole.
//
// API key callers are treated as `admin`. This is deliberately strong
// enough to let CI/CD systems deploy pipelines, but it stops short of
// `owner` so machine accounts cannot transfer ownership or change
// tenant-wide settings without a human signoff.
func RequireTenantRoleFromHeader(repo Repository, minRole string) func(http.Handler) http.Handler {
	minLevel, ok := roleHierarchy[strings.ToLower(minRole)]
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !ok {
				_ = writeError(w, http.StatusInternalServerError, "ServerError",
					"invalid minimum role configured", nil)
				return
			}

			tenantID, err := GetTenantIDFromContext(r.Context())
			if err != nil || tenantID == "" {
				_ = writeError(w, http.StatusBadRequest, "InvalidTenant",
					"X-Tenant-ID is required", nil)
				return
			}

			ctx, role, ok, err := resolvePrincipalRole(r.Context(), r, repo, tenantID)
			if err != nil {
				var keyErr *apiKeyLookupError
				if errors.As(err, &keyErr) {
					writeServiceUnavailable(w, r, "api key lookup failed", err)
					return
				}
				writeSessionError(w, r, err)
				return
			}
			if !ok {
				_ = writeError(w, http.StatusUnauthorized, "Unauthorized",
					"valid session or API key required", nil)
				return
			}
			if roleHierarchy[role] < minLevel {
				_ = writeError(w, http.StatusForbidden, "Forbidden",
					"insufficient permissions in this workspace", nil)
				return
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// apiKeyLookupError marks a failed (not rejected) API-key lookup, so the caller
// logs it as that and not as a session failure.
type apiKeyLookupError struct{ err error }

func (e *apiKeyLookupError) Error() string { return e.err.Error() }
func (e *apiKeyLookupError) Unwrap() error { return e.err }

// resolvePrincipalRole identifies the caller and returns the role they
// hold in the requested tenant.
//
// Resolution order:
//  1. Bearer session token (from cookie or Authorization header) → look
//     up the user, then their role in the tenant.
//  2. Tenant API key → role is "admin" if the key's tenant matches the
//     requested tenant.
//
// Returning ok=false means the request is anonymous and should be
// rejected with 401. A non-nil error means the lookup itself failed (the
// database, not the credential), and the caller answers 503 through
// writeSessionError; the request is not let through.
func resolvePrincipalRole(ctx context.Context, r *http.Request, repo Repository, tenantID string) (context.Context, string, bool, error) {
	token := extractBearerTokenFromHeader(r)
	if token == "" {
		return ctx, "", false, nil
	}
	hashed := auth.HashToken(token)

	// 1. Session?
	session, user, err := repo.ValidateSession(ctx, hashed)
	if err == nil && user != nil && session != nil {
		// Who tried is recorded even when the role check below refuses them;
		// the audit tenant does not depend on it (#695).
		recordAuditUser(ctx, user)
		role, err := repo.GetUserTenantRole(ctx, user.ID, tenantID)
		if err != nil {
			return ctx, "", false, err
		}
		if role == "" {
			return ctx, "", false, nil
		}
		ctx = context.WithValue(ctx, UserContextKey, user)
		ctx = context.WithValue(ctx, SessionContextKey, session)
		return withPrincipalRole(ctx, role), role, true, nil
	}
	if err != nil && !isSessionRejection(err) {
		return ctx, "", false, err
	}

	// 2. Tenant API key?
	tenant, err := repo.GetTenantByAPIKeyHash(ctx, hashed)
	if err != nil && !isAPIKeyRejection(err) {
		return ctx, "", false, &apiKeyLookupError{err: err}
	}
	if err == nil && tenant != nil && tenant.Status == "active" {
		recordAuditAPIKey(ctx, tenant)
	}
	if err == nil && tenant != nil && tenant.ID == tenantID && tenant.Status == "active" {
		ctx = context.WithValue(ctx, requestingTenantKey, tenant)
		return withPrincipalRole(ctx, "admin"), "admin", true, nil
	}

	return ctx, "", false, nil
}
