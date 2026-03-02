package managementapi

import "context"

// TenantIDKey is the context key for storing tenant ID
type contextKey string

const TenantIDKey contextKey = "tenant_id"

// ContextWithTenantID adds tenant ID to request context
func ContextWithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, TenantIDKey, tenantID)
}

// GetTenantIDFromContext retrieves tenant ID from request context
func GetTenantIDFromContext(ctx context.Context) (string, error) {
	tenantID, ok := ctx.Value(TenantIDKey).(string)
	if !ok || tenantID == "" {
		return "", &BadRequestError{Message: "tenant ID is missing"}
	}
	return tenantID, nil
}
