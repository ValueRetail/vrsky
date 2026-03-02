package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
)

// JWTConfig holds JWT configuration from environment
type JWTConfig struct {
	Enabled  bool
	Secret   string
	Issuer   string
	Audience string
}

// JWTClaims represents JWT token claims (custom implementation)
type JWTClaims struct {
	TenantID  string   `json:"tenant_id"`
	UserID    string   `json:"user_id"`
	Email     string   `json:"email"`
	Roles     []string `json:"roles"` // admin, operator, viewer
	ExpiresAt int64    `json:"exp"`
	IssuedAt  int64    `json:"iat"`
	Issuer    string   `json:"iss"`
	Audience  string   `json:"aud"`
}

// Context keys
type contextKey string

const (
	TenantIDKey contextKey = "tenant_id"
	UserIDKey   contextKey = "user_id"
	RolesKey    contextKey = "roles"
	EmailKey    contextKey = "email"
)

// LoadJWTConfig loads JWT configuration from environment
func LoadJWTConfig() *JWTConfig {
	return &JWTConfig{
		Enabled:  os.Getenv("JWT_ENABLED") == "true",
		Secret:   os.Getenv("JWT_SECRET"),
		Issuer:   os.Getenv("JWT_ISSUER"),
		Audience: os.Getenv("JWT_AUDIENCE"),
	}
}

// GetUserIDFromContext retrieves user ID from request context
func GetUserIDFromContext(ctx context.Context) string {
	userID := ctx.Value(UserIDKey)
	if userID == nil {
		return ""
	}
	if str, ok := userID.(string); ok {
		return str
	}
	return ""
}

// GetRolesFromContext retrieves roles from request context
func GetRolesFromContext(ctx context.Context) []string {
	roles := ctx.Value(RolesKey)
	if roles == nil {
		return []string{}
	}
	if roleList, ok := roles.([]string); ok {
		return roleList
	}
	return []string{}
}

// HasRole checks if the request has a specific role
func HasRole(ctx context.Context, role string) bool {
	roles := GetRolesFromContext(ctx)
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// ValidateJWT validates a JWT token and returns claims
// Implements HMAC-SHA256 signature validation without external JWT library
func ValidateJWT(tokenString string, jwtConfig *JWTConfig) (*JWTClaims, error) {
	// Split token into header.payload.signature
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token format")
	}

	// Decode payload
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid payload encoding: %w", err)
	}

	// Parse claims
	claims := &JWTClaims{}
	if err := json.Unmarshal(payload, claims); err != nil {
		return nil, fmt.Errorf("invalid claims: %w", err)
	}

	// Verify signature
	message := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("invalid signature encoding: %w", err)
	}

	expectedSignature := hmac.New(sha256.New, []byte(jwtConfig.Secret))
	expectedSignature.Write([]byte(message))
	expectedSig := expectedSignature.Sum(nil)

	if !hmac.Equal(signature, expectedSig) {
		return nil, fmt.Errorf("invalid signature")
	}

	// Validate expiration
	if claims.ExpiresAt != 0 && claims.ExpiresAt < time.Now().Unix() {
		return nil, fmt.Errorf("token expired")
	}

	// Validate issuer if configured
	if jwtConfig.Issuer != "" && claims.Issuer != jwtConfig.Issuer {
		return nil, fmt.Errorf("invalid issuer")
	}

	// Validate audience if configured
	if jwtConfig.Audience != "" && claims.Audience != jwtConfig.Audience {
		return nil, fmt.Errorf("invalid audience")
	}

	// Ensure tenant_id is present
	if claims.TenantID == "" {
		return nil, fmt.Errorf("missing tenant_id claim")
	}

	return claims, nil
}

// AuthMiddleware validates JWT tokens and extracts claims into context
// If JWT_ENABLED is false, uses default tenant from DEFAULT_TENANT_ID environment variable
func AuthMiddleware(jwtConfig *JWTConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// If JWT is disabled, use default tenant from environment
			if !jwtConfig.Enabled {
				tenantID := os.Getenv("DEFAULT_TENANT_ID")
				if tenantID == "" {
					tenantID = "default-tenant"
				}
				ctx = managementapi.ContextWithTenantID(ctx, tenantID)
				ctx = context.WithValue(ctx, UserIDKey, "anonymous")
				ctx = context.WithValue(ctx, RolesKey, []string{"viewer"})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Extract Bearer token from Authorization header
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeErrorResponse(w, http.StatusUnauthorized, "Unauthorized", "missing authorization header")
				return
			}

			// Parse Bearer token
			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || parts[0] != "Bearer" {
				writeErrorResponse(w, http.StatusUnauthorized, "Unauthorized", "invalid authorization header format")
				return
			}

			tokenString := parts[1]

			// Validate JWT token
			claims, err := ValidateJWT(tokenString, jwtConfig)
			if err != nil {
				writeErrorResponse(w, http.StatusUnauthorized, "Unauthorized", fmt.Sprintf("invalid token: %v", err))
				return
			}

			// Add claims to context
			ctx = managementapi.ContextWithTenantID(ctx, claims.TenantID)
			ctx = context.WithValue(ctx, UserIDKey, claims.UserID)
			ctx = context.WithValue(ctx, RolesKey, claims.Roles)
			ctx = context.WithValue(ctx, EmailKey, claims.Email)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RBACMiddleware enforces role-based access control
// Requires user to have at least one of the specified roles
func RBACMiddleware(requiredRoles []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Check if user has any of the required roles
			userRoles := GetRolesFromContext(ctx)
			hasRequiredRole := false

			for _, role := range userRoles {
				for _, required := range requiredRoles {
					if role == required {
						hasRequiredRole = true
						break
					}
				}
				if hasRequiredRole {
					break
				}
			}

			if !hasRequiredRole {
				writeErrorResponse(w, http.StatusForbidden, "Forbidden", fmt.Sprintf("required roles: %v", requiredRoles))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// writeErrorResponse writes a JSON error response
func writeErrorResponse(w http.ResponseWriter, status int, errorType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   errorType,
		"message": message,
		"status":  status,
	})
}
