package main

import (
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
)

// CORSMiddleware adds CORS headers to responses
func CORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
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
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}

			// Preflight requests
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Tenant-ID")
				w.Header().Set("Access-Control-Max-Age", "3600")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// TenantIDMiddleware extracts and validates tenant ID from request header
func TenantIDMiddleware(tenantHeader string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID := r.Header.Get(tenantHeader)

			// Validate tenant ID is present and non-empty
			if strings.TrimSpace(tenantID) == "" {
				http.Error(w, "Missing or invalid tenant ID header", http.StatusBadRequest)
				return
			}

			// Store tenant ID in request context for handlers to use
			ctx := r.Context()
			r = r.WithContext(managementapi.ContextWithTenantID(ctx, tenantID))

			next.ServeHTTP(w, r)
		})
	}
}
