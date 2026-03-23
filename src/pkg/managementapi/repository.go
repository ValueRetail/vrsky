package managementapi

import (
	"context"
	"time"
)

// Repository defines the interface for connection persistence operations
type Repository interface {
	// ============================================
	// Connection Operations
	// ============================================

	// CreateConnection creates a new connection in the database
	CreateConnection(ctx context.Context, connection *Connection) error

	// GetConnection retrieves a connection by ID
	GetConnection(ctx context.Context, id string) (*Connection, error)

	// ListConnections retrieves connections for a tenant with optional filtering
	ListConnections(ctx context.Context, tenantID string, filters *ListFilters) ([]*Connection, int64, error)

	// UpdateConnection updates an existing connection
	UpdateConnection(ctx context.Context, connection *Connection) error

	// DeleteConnection deletes a connection and its related events
	DeleteConnection(ctx context.Context, id string) error

	// UpdateConnectionStatus updates the status of a connection
	UpdateConnectionStatus(ctx context.Context, id string, status string, lastError *string) error

	// CreateConnectionEvent creates a new connection event
	CreateConnectionEvent(ctx context.Context, event *ConnectionEvent) error

	// GetConnectionEvents retrieves events for a connection
	GetConnectionEvents(ctx context.Context, connectionID string) ([]*ConnectionEvent, error)

	// ============================================
	// User Operations (Phase 1 Auth)
	// ============================================

	// CreateUser creates a new user in the database
	CreateUser(ctx context.Context, user *User) error

	// GetUserByID retrieves a user by ID
	GetUserByID(ctx context.Context, id string) (*User, error)

	// GetUserByEmail retrieves a user by email
	GetUserByEmail(ctx context.Context, email string) (*User, error)

	// UpdateUserLastLogin updates the last login timestamp
	UpdateUserLastLogin(ctx context.Context, userID string) error

	// UpdateUserPassword updates a user's password hash
	UpdateUserPassword(ctx context.Context, userID, passwordHash string) error

	// VerifyUserEmail marks a user's email as verified
	VerifyUserEmail(ctx context.Context, userID string) error

	// ============================================
	// Session Operations (Phase 1 Auth)
	// ============================================

	// CreateSession creates a new session in the database
	CreateSession(ctx context.Context, session *Session) error

	// GetSessionByTokenHash retrieves a session by token hash
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error)

	// ValidateSession checks if a session is valid and returns the session and user
	ValidateSession(ctx context.Context, tokenHash string) (*Session, *User, error)

	// UpdateSessionActivity updates the last activity timestamp
	UpdateSessionActivity(ctx context.Context, sessionID string) error

	// InvalidateSession marks a session as inactive
	InvalidateSession(ctx context.Context, tokenHash string) error

	// InvalidateAllUserSessions invalidates all sessions for a user
	InvalidateAllUserSessions(ctx context.Context, userID string) error

	// ============================================
	// Token Operations (Phase 1 Auth)
	// ============================================

	// CreateEmailVerificationToken creates a new email verification token
	CreateEmailVerificationToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error

	// GetEmailVerificationToken retrieves a token by its hash
	GetEmailVerificationToken(ctx context.Context, tokenHash string) (*EmailVerificationToken, error)

	// UseEmailVerificationToken marks a token as used and verifies the user
	UseEmailVerificationToken(ctx context.Context, tokenHash string) error

	// CreatePasswordResetToken creates a new password reset token
	CreatePasswordResetToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error

	// GetPasswordResetToken retrieves a password reset token by its hash
	GetPasswordResetToken(ctx context.Context, tokenHash string) (*PasswordResetToken, error)

	// UsePasswordResetToken validates, uses the token, and updates the password
	UsePasswordResetToken(ctx context.Context, tokenHash, newPasswordHash string) error

	// ============================================
	// Audit Log Operations (Phase 1 Auth)
	// ============================================

	// CreateAuthAuditLog creates an auth audit log entry
	CreateAuthAuditLog(ctx context.Context, log *AuthAuditLog) error

	// ============================================
	// Lifecycle
	// ============================================

	// Close closes the repository connection
	Close() error
}

// ListFilters provides filtering options for listing connections
type ListFilters struct {
	Status string // Optional: filter by status (stopped, running, error)
	Search string // Optional: search by name
	Limit  int    // Default: 20, max: 100
	Offset int    // Default: 0
}

// ListResult contains the result of a list operation
type ListResult struct {
	Total       int64
	Connections []*Connection
}
