package managementapi

import (
	"context"
	"net/http"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// requestingTenantKey is a distinct context key for API-key-authenticated tenants.
const requestingTenantKey contextKey = "requesting_tenant"

// TenantAPIKeyMiddleware authenticates inbound data requests from external tenants.
// It extracts a Bearer token (format: vrsky_{slug}_{hex}), hashes it, looks up the
// matching tenant via tenant_api_keys, and injects *Tenant into the context.
// This is separate from SessionAuthMiddleware which handles human user sessions.
func TenantAPIKeyMiddleware(repo Repository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearerTokenFromHeader(r)
			if token == "" {
				_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "missing or invalid API key", nil)
				return
			}

			keyHash := auth.HashToken(token)

			tenant, err := repo.GetTenantByAPIKeyHash(r.Context(), keyHash)
			if err != nil || tenant == nil {
				_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "invalid API key", nil)
				return
			}

			if tenant.Status != "active" {
				_ = writeError(w, http.StatusForbidden, "Forbidden", "tenant is not active", nil)
				return
			}

			ctx := context.WithValue(r.Context(), requestingTenantKey, tenant)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetRequestingTenantFromContext retrieves the API-key-authenticated tenant from context.
func GetRequestingTenantFromContext(ctx context.Context) *Tenant {
	t, _ := ctx.Value(requestingTenantKey).(*Tenant)
	return t
}
