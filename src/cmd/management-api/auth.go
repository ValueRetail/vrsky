package main

import (
	"context"
	"net/http"
)

// TODO: Future JWT/RBAC implementation
// For now, this is a stub that passes through requests

// AuthMiddleware validates JWT tokens (FUTURE: implement this)
// For now, this is a placeholder
func AuthMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// TODO: Implement JWT validation here
			// TODO: Extract claims from token
			// TODO: Verify signature
			// TODO: Check expiration
			// TODO: Store claims in request context

			// For MVP: just pass through
			next.ServeHTTP(w, r)
		})
	}
}

// Context key for tenant ID
type contextKey string

const TenantIDKey contextKey = "tenant_id"

// ContextWithTenantID adds tenant ID to request context
func ContextWithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, TenantIDKey, tenantID)
}

// GetTenantIDFromContext retrieves tenant ID from request context
func GetTenantIDFromContext(ctx context.Context) string {
	tenantID := ctx.Value(TenantIDKey)
	if tenantID == nil {
		return ""
	}
	return tenantID.(string)
}
