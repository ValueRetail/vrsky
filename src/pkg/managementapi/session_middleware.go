package managementapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// Context keys for authenticated user data
type userContextKey string

const (
	// UserContextKey is the context key for storing the authenticated user
	UserContextKey userContextKey = "authenticated_user"
	// SessionContextKey is the context key for storing the session
	SessionContextKey userContextKey = "authenticated_session"
)

// SessionAuthMiddleware validates session tokens and injects user into context.
// This middleware should be applied to protected routes that require authentication.
//
// Usage:
//
//	router.Handle("/api/v1/auth/me", SessionAuthMiddleware(repo)(handler))
func SessionAuthMiddleware(repo Repository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract bearer token from Authorization header
			token := extractBearerTokenFromHeader(r)
			if token == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing or invalid authorization header")
				return
			}

			// Hash the token for lookup
			hashedToken := auth.HashToken(token)

			// Validate the session
			session, user, err := repo.ValidateSession(r.Context(), hashedToken)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, "invalid or expired session")
				return
			}

			// Check if user can still access the system
			if err := user.CanLogin(); err != nil {
				writeAuthError(w, http.StatusForbidden, err.Error())
				return
			}

			// Add user and session to context
			ctx := context.WithValue(r.Context(), UserContextKey, user)
			ctx = context.WithValue(ctx, SessionContextKey, session)

			// Continue with the request
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// OptionalSessionMiddleware extracts session if present, but doesn't require it.
// This is useful for routes that can work with or without authentication.
func OptionalSessionMiddleware(repo Repository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract bearer token from Authorization header
			token := extractBearerTokenFromHeader(r)
			if token == "" {
				// No token - continue without authentication
				next.ServeHTTP(w, r)
				return
			}

			// Hash the token for lookup
			hashedToken := auth.HashToken(token)

			// Try to validate the session
			session, user, err := repo.ValidateSession(r.Context(), hashedToken)
			if err != nil {
				// Invalid token - continue without authentication
				next.ServeHTTP(w, r)
				return
			}

			// Add user and session to context
			ctx := context.WithValue(r.Context(), UserContextKey, user)
			ctx = context.WithValue(ctx, SessionContextKey, session)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetUserFromContext retrieves the authenticated user from the request context.
// Returns nil if no user is authenticated.
func GetUserFromContext(ctx context.Context) *User {
	user, ok := ctx.Value(UserContextKey).(*User)
	if !ok {
		return nil
	}
	return user
}

// GetSessionFromContext retrieves the session from the request context.
// Returns nil if no session is present.
func GetSessionFromContext(ctx context.Context) *Session {
	session, ok := ctx.Value(SessionContextKey).(*Session)
	if !ok {
		return nil
	}
	return session
}

// RequireAuthenticatedUser is a helper that returns the user or writes an error response.
// Use this in handlers that require authentication.
func RequireAuthenticatedUser(w http.ResponseWriter, r *http.Request) (*User, bool) {
	user := GetUserFromContext(r.Context())
	if user == nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return user, true
}

// extractBearerTokenFromHeader extracts the session token from either the
// Authorization header (Bearer scheme — used by API clients and the legacy
// localStorage-based UI) OR the vrsky_session cookie set by the OIDC and
// email/password login paths. The cookie path is preferred when both are
// present so a browser fetch carries the same identity as a navigation.
func extractBearerTokenFromHeader(r *http.Request) string {
	// Cookie first.
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return ""
	}

	return strings.TrimSpace(parts[1])
}

// writeAuthError writes an authentication error response
func writeAuthError(w http.ResponseWriter, status int, message string) {
	_ = writeError(w, status, "Unauthorized", message, nil)
}
