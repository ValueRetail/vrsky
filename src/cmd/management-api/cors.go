package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
)

// CORSMiddleware adds CORS headers to responses
// tenantHeader is the name of the header used for tenant identification (e.g., "X-Tenant-ID")
func CORSMiddleware(allowedOrigins []string, tenantHeader string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Check if origin is allowed
			isAllowed := false
			for _, allowed := range allowedOrigins {
				if allowed == "*" || origin == allowed {
					isAllowed = true
					break
				}
			}

			if isAllowed {
				// Only set the header if origin is non-empty
				// For wildcard with no origin, don't set the header (browser will reject anyway)
				// For specific origin match, always set it
				if origin != "" {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
			}

			// Preflight requests
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")

				// Use requested headers from the browser's preflight request
				requestedHeaders := r.Header.Get("Access-Control-Request-Headers")
				if strings.TrimSpace(requestedHeaders) != "" {
					// Echo back the requested headers
					w.Header().Set("Access-Control-Allow-Headers", requestedHeaders)
				} else {
					// Fallback to default headers including configured tenant header
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, "+tenantHeader)
				}
				w.Header().Set("Access-Control-Max-Age", "3600")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// TenantIDMiddleware extracts and validates tenant ID from request header
// Skips validation for routes that don't require tenant context (auth, health checks)
func TenantIDMiddleware(tenantHeader string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip tenant validation for auth routes, tenant routes, and health checks
			// Auth and tenant management are session-scoped, not tenant-header-scoped.
			// The OAuth callback is also exempt: it's a top-level browser redirect
			// from the provider, so it can't carry the X-Tenant-ID header — it
			// derives the tenant from the signed state cookie set at StartOAuth.
			if strings.HasPrefix(r.URL.Path, "/api/v1/auth/") ||
				strings.HasPrefix(r.URL.Path, "/api/v1/tenants") ||
				r.URL.Path == "/api/v1/oauth/callback" ||
				r.URL.Path == "/api/v1/alerts/webhook" ||
				r.URL.Path == "/metrics" ||
				r.URL.Path == "/openapi.json" ||
				r.URL.Path == "/docs" ||
				r.URL.Path == "/status" ||
				r.URL.Path == "/status.json" ||
				r.URL.Path == "/health" ||
				r.URL.Path == "/healthz" ||
				r.URL.Path == "/ready" ||
				r.URL.Path == "/readyz" {
				next.ServeHTTP(w, r)
				return
			}

			tenantID := r.Header.Get(tenantHeader)

			// Validate tenant ID is present and non-empty
			if strings.TrimSpace(tenantID) == "" {
				// Return JSON error response consistent with API handlers
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error":   "BadRequest",
					"message": "Missing or invalid tenant ID header",
					"status":  http.StatusBadRequest,
				})
				return
			}

			// Store tenant ID in request context for handlers to use
			ctx := r.Context()
			r = r.WithContext(managementapi.ContextWithTenantID(ctx, tenantID))

			next.ServeHTTP(w, r)
		})
	}
}
