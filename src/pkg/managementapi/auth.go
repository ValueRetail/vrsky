package managementapi

import "context"

// TenantIDKey is the context key for storing tenant ID
type contextKey string

const (
	TenantIDKey   contextKey = "tenant_id"
	TenantKey     contextKey = "tenant"
	TenantRoleKey contextKey = "tenant_role"
)

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

// ContextWithTenant adds tenant and role to request context
func ContextWithTenant(ctx context.Context, tenant *Tenant, role string) context.Context {
	ctx = context.WithValue(ctx, TenantKey, tenant)
	ctx = context.WithValue(ctx, TenantRoleKey, role)
	return ctx
}

// GetTenantFromContext retrieves the tenant from request context
func GetTenantFromContext(ctx context.Context) *Tenant {
	t, _ := ctx.Value(TenantKey).(*Tenant)
	return t
}

// GetTenantRoleFromContext retrieves the user's role in the tenant from context
func GetTenantRoleFromContext(ctx context.Context) string {
	role, _ := ctx.Value(TenantRoleKey).(string)
	return role
}
