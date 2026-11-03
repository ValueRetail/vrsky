package managementapi

import (
	"context"
	"errors"
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
			if err != nil {
				writeAPIKeyError(w, r, err)
				return
			}
			if tenant == nil {
				_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "invalid API key", nil)
				return
			}

			if tenant.Status != "active" {
				_ = writeError(w, http.StatusForbidden, "Forbidden", "tenant is not active", nil)
				return
			}

			recordAuditAPIKey(r.Context(), tenant)
			ctx := context.WithValue(r.Context(), requestingTenantKey, tenant)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// isAPIKeyRejection reports whether err means the server looked the key up and
// turned it down: no active key has that hash (unknown, rotated away or
// revoked), or its tenant is deleted. PostgresRepository.GetTenantByAPIKeyHash
// returns ErrAPIKeyNotFound for exactly those and wraps every other error, so
// anything else (a closed pool, a cancelled context, a lost connection) says
// nothing about the key and is not a rejection.
func isAPIKeyRejection(err error) bool {
	return errors.Is(err, ErrAPIKeyNotFound)
}

// writeAPIKeyError answers a failed API-key lookup: 401 for a rejected key,
// otherwise a logged 503 with a fixed body, so a database error does not look
// like a bad key and its text does not reach the client. Neither the key nor
// its hash is logged. 503 never lets the request through.
func writeAPIKeyError(w http.ResponseWriter, r *http.Request, err error) {
	if isAPIKeyRejection(err) {
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "invalid API key", nil)
		return
	}
	writeServiceUnavailable(w, r, "api key lookup failed", err)
}

// GetRequestingTenantFromContext retrieves the API-key-authenticated tenant from context.
func GetRequestingTenantFromContext(ctx context.Context) *Tenant {
	t, _ := ctx.Value(requestingTenantKey).(*Tenant)
	return t
}
