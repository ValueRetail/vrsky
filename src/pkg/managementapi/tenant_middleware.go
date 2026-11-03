package managementapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// roleHierarchy defines the permission level for each role
var roleHierarchy = map[string]int{
	"viewer": 1,
	"editor": 2,
	"admin":  3,
	"owner":  4,
}

// TenantMemberMiddleware validates that the authenticated user is a member of the
// requested tenant. It extracts the tenant_id from the URL path (expecting
// /api/v1/tenants/{tenant_id}/...) or from the X-Tenant-ID header.
// SessionAuthMiddleware MUST run before this middleware.
func TenantMemberMiddleware(repo Repository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := GetUserFromContext(r.Context())
			if user == nil {
				_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "authentication required", nil)
				return
			}

			// Extract tenant_id from URL path param
			tenantID := r.PathValue("tenant_id")
			if tenantID == "" {
				// Fall back to X-Tenant-ID header
				tenantID = r.Header.Get("X-Tenant-ID")
			}
			if tenantID == "" {
				_ = writeError(w, http.StatusBadRequest, "BadRequest", "tenant ID is required", nil)
				return
			}

			// Check membership
			role, err := repo.GetUserTenantRole(r.Context(), user.ID, tenantID)
			if err != nil {
				// A failed lookup says nothing about membership: 503, never a
				// 403 or a pass-through.
				writeServiceUnavailable(w, r, "tenant membership lookup failed", err)
				return
			}
			if role == "" {
				_ = writeError(w, http.StatusForbidden, "Forbidden", "you are not a member of this tenant", nil)
				return
			}

			// Fetch the tenant
			tenant, err := repo.GetTenantByID(r.Context(), tenantID)
			if err != nil {
				// Only a missing tenant is a 404. Any other error (a lost
				// connection, a cancelled context) must not look like a
				// deleted workspace (#662).
				if errors.Is(err, ErrTenantNotFound) || errors.Is(err, sql.ErrNoRows) {
					_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
					return
				}
				writeServiceUnavailable(w, r, "tenant lookup failed", err)
				return
			}

			// Only now, with membership checked, may the audit entry go into
			// this workspace's log (#695). Recording it earlier would let
			// anyone file entries into any workspace by naming it in the path.
			recordAuditVerifiedTenant(r.Context(), tenantID)

			// Add tenant info to context (keeps ContextWithTenantID for existing connection handlers)
			ctx := ContextWithTenantID(r.Context(), tenantID)
			ctx = ContextWithTenant(ctx, tenant, role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole returns middleware that checks the user has at least the specified role.
// Must be used after TenantMemberMiddleware.
func RequireRole(minRole string) func(http.Handler) http.Handler {
	minLevel, ok := roleHierarchy[strings.ToLower(minRole)]
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If an invalid minRole was configured, fail closed instead of granting access.
			if !ok {
				_ = writeError(w, http.StatusInternalServerError, "ServerError", "invalid minimum role configured", nil)
				return
			}
			userRole := GetTenantRoleFromContext(r.Context())
			if roleHierarchy[userRole] < minLevel {
				_ = writeError(w, http.StatusForbidden, "Forbidden", "insufficient permissions", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
