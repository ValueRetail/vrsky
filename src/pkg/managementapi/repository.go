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
	// Tenant Operations (Phase 1 Refactor)
	// ============================================

	// CreateTenant creates a new tenant and assigns the user as owner
	CreateTenant(ctx context.Context, userID, name, slug string) (*Tenant, error)

	// GetTenantByID fetches a tenant by ID
	GetTenantByID(ctx context.Context, tenantID string) (*Tenant, error)

	// GetUserTenants returns all tenants a user has access to
	GetUserTenants(ctx context.Context, userID string) ([]*TenantResponse, error)

	// GetUserTenantRole returns the user's role in a tenant
	GetUserTenantRole(ctx context.Context, userID, tenantID string) (string, error)

	// DeleteTenant soft-deletes a tenant
	DeleteTenant(ctx context.Context, tenantID string) error

	// ============================================
	// Tenant Provisioning (Phase 2)
	// ============================================

	// UpdateTenantStatus updates the status and optional NATS slug
	UpdateTenantStatus(ctx context.Context, tenantID, status string, natsSlug *string) error

	// CreateProvisioningJob creates a new provisioning job record
	CreateProvisioningJob(ctx context.Context, tenantID string) (*ProvisioningJob, error)

	// UpdateProvisioningJob updates job progress
	UpdateProvisioningJob(ctx context.Context, jobID, status string, progress int, step, errMsg string) error

	// UpdateProvisioningJobCompleted sets the completed_at timestamp
	UpdateProvisioningJobCompleted(ctx context.Context, jobID string, completedAt *time.Time) error

	// GetLatestProvisioningJob returns the most recent job for a tenant
	GetLatestProvisioningJob(ctx context.Context, tenantID string) (*ProvisioningJob, error)

	// UpsertTenantAPIKey creates or replaces an API key for a tenant
	UpsertTenantAPIKey(ctx context.Context, tenantID, keyHash string) (*TenantAPIKey, error)

	// GetTenantAPIKey retrieves the current API key metadata for a tenant
	GetTenantAPIKey(ctx context.Context, tenantID string) (*TenantAPIKey, error)

	// ============================================
	// Data Sharing Operations (Phase 3)
	// ============================================

	// CreateConnectionRequest creates a new tenant-to-tenant connection request
	CreateConnectionRequest(ctx context.Context, req *DataConnectionRequest) error

	// GetConnectionRequest retrieves a connection request by ID
	GetConnectionRequest(ctx context.Context, requestID string) (*DataConnectionRequest, error)

	// ListIncomingConnectionRequests returns pending requests targeting a tenant
	ListIncomingConnectionRequests(ctx context.Context, targetTenantID string) ([]*DataConnectionRequest, error)

	// ListOutgoingConnectionRequests returns requests sent by a tenant
	ListOutgoingConnectionRequests(ctx context.Context, requesterTenantID string) ([]*DataConnectionRequest, error)

	// ApproveConnectionRequest approves a request and creates an active data connection
	ApproveConnectionRequest(ctx context.Context, requestID string, allowedFields, deniedFields, sharedConnectionIDs []string) (*TenantDataConnection, error)

	// DenyConnectionRequest denies a pending request
	DenyConnectionRequest(ctx context.Context, requestID string) error

	// ListDataConnections returns data connections involving a tenant
	ListDataConnections(ctx context.Context, tenantID string) ([]*TenantDataConnection, error)

	// GetDataConnectionByID retrieves a data connection by ID
	GetDataConnectionByID(ctx context.Context, id string) (*TenantDataConnection, error)

	// GetActiveDataConnection finds an active connection between two tenants
	GetActiveDataConnection(ctx context.Context, requesterID, targetID string) (*TenantDataConnection, error)

	// GetSharedConnectionsForTenant returns shared connection IDs for a data connection
	GetSharedConnectionsForTenant(ctx context.Context, requesterID, targetID string) ([]string, error)

	// RevokeDataConnection revokes an active data connection
	RevokeDataConnection(ctx context.Context, connectionID string) error

	// CreateDataAccessLog records a data access event
	CreateDataAccessLog(ctx context.Context, entry *DataAccessLogEntry) error

	// ListDataAccessLog returns paginated audit log entries for a tenant
	ListDataAccessLog(ctx context.Context, targetTenantID string, filters *ListFilters) ([]*DataAccessLogEntry, int64, error)

	// PauseConnectionsByDataConnection stops pipeline connections using a revoked data connection
	PauseConnectionsByDataConnection(ctx context.Context, tenantID, dataConnectionID string) (int64, error)

	// GetTenantByAPIKeyHash retrieves a tenant by the hash of its API key
	GetTenantByAPIKeyHash(ctx context.Context, keyHash string) (*Tenant, error)

	// ============================================
	// Tenant quotas (Phase 1I — #74)
	// ============================================

	// GetTenantQuotas returns the quota row, auto-creating a default one
	// when the tenant has none yet.
	GetTenantQuotas(ctx context.Context, tenantID string) (*TenantQuotas, error)

	// UpdateTenantQuotas overwrites configurable quota fields. Owner-only.
	UpdateTenantQuotas(ctx context.Context, q *TenantQuotas) error

	// SetTenantStorageUsage is called by the hourly storage job. Flips
	// storage_exceeded based on the new usage vs the configured ceiling.
	SetTenantStorageUsage(ctx context.Context, tenantID string, bytes int64) error

	// CountActiveIntegrations returns how many connections the tenant
	// has, used by the integration-count quota check on create paths.
	CountActiveIntegrations(ctx context.Context, tenantID string) (int, error)

	// ============================================
	// Tenant members (Phase 1D — #69)
	// ============================================

	// ListTenantMembers returns every (user, role) tuple for one tenant.
	ListTenantMembers(ctx context.Context, tenantID string) ([]*TenantMember, error)

	// SetTenantMemberRole changes a user's role. Returns ErrLastOwner if
	// the change would leave the tenant with zero owners.
	SetTenantMemberRole(ctx context.Context, tenantID, userID, newRole string) error

	// RemoveTenantMember deletes the membership. Returns ErrLastOwner if
	// removal would leave zero owners.
	RemoveTenantMember(ctx context.Context, tenantID, userID string) error

	// ============================================
	// OIDC / SSO (Phase 1C — #68)
	// ============================================

	// GetOIDCConfigByTenantID returns the OIDC config for a tenant or
	// ErrOIDCConfigNotFound if SSO is not configured.
	GetOIDCConfigByTenantID(ctx context.Context, tenantID string) (*OIDCConfig, error)

	// GetOIDCConfigByTenantSlug resolves a tenant slug to its OIDC config.
	GetOIDCConfigByTenantSlug(ctx context.Context, slug string) (*OIDCConfig, error)

	// UpsertOIDCConfig stores or replaces a tenant's OIDC config.
	UpsertOIDCConfig(ctx context.Context, c *OIDCConfig) error

	// DeleteOIDCConfig removes a tenant's OIDC config.
	DeleteOIDCConfig(ctx context.Context, tenantID string) error

	// GetUserByOIDCSubject finds a previously-linked user. Returns
	// (nil, nil) when not yet linked — callers auto-provision.
	GetUserByOIDCSubject(ctx context.Context, provider, subject string) (*User, error)

	// LinkUserOIDC sets oidc_provider + oidc_subject on an existing user.
	LinkUserOIDC(ctx context.Context, userID, provider, subject string) error

	// ============================================
	// Audit Log Operations (Phase 1G — #72)
	// ============================================

	// CreateAuditEntry appends an immutable audit record.
	CreateAuditEntry(ctx context.Context, e *AuditEntry) error

	// ListAuditEntries returns paginated audit entries for one tenant.
	ListAuditEntries(ctx context.Context, tenantID string, f AuditFilters, limit, offset int) ([]*AuditEntry, int64, error)

	// StreamAuditEntries iterates entries (oldest first) and calls emit
	// for each — used by the JSONL export so the entire result set never
	// lives in memory.
	StreamAuditEntries(ctx context.Context, tenantID string, f AuditFilters, emit func(*AuditEntry) error) error

	// ============================================
	// Secrets Operations (Phase 1A — #66)
	// ============================================

	// CreateSecret persists a new ciphertext for a tenant and returns the metadata.
	CreateSecret(ctx context.Context, tenantID, name, ciphertext string) (*Secret, error)

	// GetSecret returns the metadata for one secret (no ciphertext).
	GetSecret(ctx context.Context, tenantID, id string) (*Secret, error)

	// GetSecretCiphertext returns the raw ciphertext (used by workers at startup).
	GetSecretCiphertext(ctx context.Context, tenantID, id string) (string, error)

	// ListSecrets returns metadata for all secrets owned by a tenant.
	ListSecrets(ctx context.Context, tenantID string, limit, offset int) ([]*Secret, error)

	// UpdateSecret rewrites name and/or ciphertext for one secret.
	UpdateSecret(ctx context.Context, tenantID, id, name, ciphertext string) (*Secret, error)

	// DeleteSecret removes a secret. Returns referencing connection IDs if any
	// (and does NOT delete in that case).
	DeleteSecret(ctx context.Context, tenantID, id string) ([]string, error)

	// ============================================
	// User Account Deletion
	// ============================================

	// DeleteUser soft-deletes a user and invalidates all their sessions
	DeleteUser(ctx context.Context, userID string) error

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
