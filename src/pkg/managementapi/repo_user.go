package managementapi

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// ============================================
// User Repository Methods
// ============================================

// CreateUser creates a new user in the database
func (r *PostgresRepository) CreateUser(ctx context.Context, user *User) error {
	query := `
		INSERT INTO users (
			id, email, password_hash, full_name, status, 
			email_verified, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`

	_, err := r.db.ExecContext(
		ctx, query,
		user.ID, user.Email, user.PasswordHash, user.FullName, user.Status,
		user.EmailVerified, user.CreatedAt, user.UpdatedAt,
	)

	if err != nil {
		// Check for unique constraint violation (email already exists)
		if isDuplicateKeyError(err) {
			return auth.ErrEmailExists
		}
		return fmt.Errorf("failed to create user: %w", err)
	}

	return nil
}

// GetUserByID retrieves a user by ID
func (r *PostgresRepository) GetUserByID(ctx context.Context, id string) (*User, error) {
	query := `
		SELECT id, email, password_hash, full_name, status,
			email_verified, email_verified_at, 
			created_at, updated_at, last_login_at, deleted_at
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
	`

	user := &User{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.FullName, &user.Status,
		&user.EmailVerified, &user.EmailVerifiedAt,
		&user.CreatedAt, &user.UpdatedAt, &user.LastLoginAt, &user.DeletedAt,
	)

	if err == sql.ErrNoRows {
		return nil, auth.ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	return user, nil
}

// GetUserByEmail retrieves a user by email
func (r *PostgresRepository) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	query := `
		SELECT id, email, password_hash, full_name, status,
			email_verified, email_verified_at, 
			created_at, updated_at, last_login_at, deleted_at
		FROM users
		WHERE email = $1 AND deleted_at IS NULL
	`

	user := &User{}
	err := r.db.QueryRowContext(ctx, query, email).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.FullName, &user.Status,
		&user.EmailVerified, &user.EmailVerifiedAt,
		&user.CreatedAt, &user.UpdatedAt, &user.LastLoginAt, &user.DeletedAt,
	)

	if err == sql.ErrNoRows {
		return nil, auth.ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}

	return user, nil
}

// UpdateUserLastLogin updates the last login timestamp
func (r *PostgresRepository) UpdateUserLastLogin(ctx context.Context, userID string) error {
	query := `UPDATE users SET last_login_at = $1 WHERE id = $2`
	_, err := r.db.ExecContext(ctx, query, time.Now().UTC(), userID)
	if err != nil {
		return fmt.Errorf("failed to update last login: %w", err)
	}
	return nil
}

// UpdateUserPassword updates a user's password hash
func (r *PostgresRepository) UpdateUserPassword(ctx context.Context, userID, passwordHash string) error {
	query := `UPDATE users SET password_hash = $1, updated_at = $2 WHERE id = $3`
	_, err := r.db.ExecContext(ctx, query, passwordHash, time.Now().UTC(), userID)
	if err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}
	return nil
}

// VerifyUserEmail marks a user's email as verified and activates the account
func (r *PostgresRepository) VerifyUserEmail(ctx context.Context, userID string) error {
	now := time.Now().UTC()
	query := `
		UPDATE users 
		SET email_verified = true, email_verified_at = $1, status = $2, updated_at = $3 
		WHERE id = $4
	`
	_, err := r.db.ExecContext(ctx, query, now, UserStatusActive, now, userID)
	if err != nil {
		return fmt.Errorf("failed to verify email: %w", err)
	}
	return nil
}

// ============================================
// Session Repository Methods
// ============================================

// CreateSession creates a new session in the database
func (r *PostgresRepository) CreateSession(ctx context.Context, session *Session) error {
	query := `
		INSERT INTO sessions (
			id, user_id, token_hash, ip_address, user_agent,
			created_at, expires_at, last_activity, is_active
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`

	_, err := r.db.ExecContext(
		ctx, query,
		session.ID, session.UserID, session.TokenHash, session.IPAddress, session.UserAgent,
		session.CreatedAt, session.ExpiresAt, session.LastActivity, session.IsActive,
	)

	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	return nil
}

// GetSessionByTokenHash retrieves a session by token hash
func (r *PostgresRepository) GetSessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error) {
	query := `
		SELECT id, user_id, token_hash, ip_address, user_agent,
			created_at, expires_at, last_activity, is_active
		FROM sessions
		WHERE token_hash = $1
	`

	session := &Session{}
	err := r.db.QueryRowContext(ctx, query, tokenHash).Scan(
		&session.ID, &session.UserID, &session.TokenHash, &session.IPAddress, &session.UserAgent,
		&session.CreatedAt, &session.ExpiresAt, &session.LastActivity, &session.IsActive,
	)

	if err == sql.ErrNoRows {
		return nil, auth.ErrSessionInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	return session, nil
}

// ValidateSession checks if a session is valid and returns the user
func (r *PostgresRepository) ValidateSession(ctx context.Context, tokenHash string) (*Session, *User, error) {
	session, err := r.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, nil, err
	}

	// Check if session is active
	if !session.IsActive {
		return nil, nil, auth.ErrSessionInvalid
	}

	// Check if session is expired
	if time.Now().UTC().After(session.ExpiresAt) {
		return nil, nil, auth.ErrSessionExpired
	}

	// Get the user
	user, err := r.GetUserByID(ctx, session.UserID)
	if err != nil {
		return nil, nil, err
	}

	// Update last activity
	_ = r.UpdateSessionActivity(ctx, session.ID)

	return session, user, nil
}

// UpdateSessionActivity updates the last activity timestamp for a session
func (r *PostgresRepository) UpdateSessionActivity(ctx context.Context, sessionID string) error {
	query := `UPDATE sessions SET last_activity = $1 WHERE id = $2`
	_, err := r.db.ExecContext(ctx, query, time.Now().UTC(), sessionID)
	if err != nil {
		return fmt.Errorf("failed to update session activity: %w", err)
	}
	return nil
}

// InvalidateSession marks a session as inactive (logout)
func (r *PostgresRepository) InvalidateSession(ctx context.Context, tokenHash string) error {
	query := `UPDATE sessions SET is_active = false WHERE token_hash = $1`
	_, err := r.db.ExecContext(ctx, query, tokenHash)
	if err != nil {
		return fmt.Errorf("failed to invalidate session: %w", err)
	}
	return nil
}

// InvalidateAllUserSessions invalidates all sessions for a user
func (r *PostgresRepository) InvalidateAllUserSessions(ctx context.Context, userID string) error {
	query := `UPDATE sessions SET is_active = false WHERE user_id = $1`
	_, err := r.db.ExecContext(ctx, query, userID)
	if err != nil {
		return fmt.Errorf("failed to invalidate user sessions: %w", err)
	}
	return nil
}

// ============================================
// Email Verification Token Repository Methods
// ============================================

// CreateEmailVerificationToken creates a new email verification token
func (r *PostgresRepository) CreateEmailVerificationToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	query := `
		INSERT INTO email_verification_tokens (id, user_id, token_hash, created_at, expires_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4)
	`
	_, err := r.db.ExecContext(ctx, query, userID, tokenHash, time.Now().UTC(), expiresAt)
	if err != nil {
		return fmt.Errorf("failed to create email verification token: %w", err)
	}
	return nil
}

// GetEmailVerificationToken retrieves a token by its hash and validates it
func (r *PostgresRepository) GetEmailVerificationToken(ctx context.Context, tokenHash string) (*EmailVerificationToken, error) {
	query := `
		SELECT id, user_id, token_hash, created_at, expires_at, used_at
		FROM email_verification_tokens
		WHERE token_hash = $1
	`

	token := &EmailVerificationToken{}
	err := r.db.QueryRowContext(ctx, query, tokenHash).Scan(
		&token.ID, &token.UserID, &token.TokenHash, &token.CreatedAt, &token.ExpiresAt, &token.UsedAt,
	)

	if err == sql.ErrNoRows {
		return nil, auth.ErrInvalidToken
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get email verification token: %w", err)
	}

	return token, nil
}

// UseEmailVerificationToken marks a token as used and verifies the user's email
func (r *PostgresRepository) UseEmailVerificationToken(ctx context.Context, tokenHash string) error {
	// Get the token first
	token, err := r.GetEmailVerificationToken(ctx, tokenHash)
	if err != nil {
		return err
	}

	// Check if already used
	if token.UsedAt != nil {
		return auth.ErrTokenUsed
	}

	// Check if expired
	if time.Now().UTC().After(token.ExpiresAt) {
		return auth.ErrTokenExpired
	}

	// Mark token as used
	now := time.Now().UTC()
	query := `UPDATE email_verification_tokens SET used_at = $1 WHERE id = $2`
	_, err = r.db.ExecContext(ctx, query, now, token.ID)
	if err != nil {
		return fmt.Errorf("failed to mark token as used: %w", err)
	}

	// Verify the user's email
	err = r.VerifyUserEmail(ctx, token.UserID)
	if err != nil {
		return err
	}

	return nil
}

// ============================================
// Password Reset Token Repository Methods
// ============================================

// CreatePasswordResetToken creates a new password reset token
func (r *PostgresRepository) CreatePasswordResetToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	query := `
		INSERT INTO password_reset_tokens (id, user_id, token_hash, created_at, expires_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4)
	`
	_, err := r.db.ExecContext(ctx, query, userID, tokenHash, time.Now().UTC(), expiresAt)
	if err != nil {
		return fmt.Errorf("failed to create password reset token: %w", err)
	}
	return nil
}

// GetPasswordResetToken retrieves a password reset token by its hash
func (r *PostgresRepository) GetPasswordResetToken(ctx context.Context, tokenHash string) (*PasswordResetToken, error) {
	query := `
		SELECT id, user_id, token_hash, created_at, expires_at, used_at
		FROM password_reset_tokens
		WHERE token_hash = $1
	`

	token := &PasswordResetToken{}
	err := r.db.QueryRowContext(ctx, query, tokenHash).Scan(
		&token.ID, &token.UserID, &token.TokenHash, &token.CreatedAt, &token.ExpiresAt, &token.UsedAt,
	)

	if err == sql.ErrNoRows {
		return nil, auth.ErrInvalidToken
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get password reset token: %w", err)
	}

	return token, nil
}

// UsePasswordResetToken validates and marks a password reset token as used
func (r *PostgresRepository) UsePasswordResetToken(ctx context.Context, tokenHash, newPasswordHash string) error {
	// Get the token first
	token, err := r.GetPasswordResetToken(ctx, tokenHash)
	if err != nil {
		return err
	}

	// Check if already used
	if token.UsedAt != nil {
		return auth.ErrTokenUsed
	}

	// Check if expired
	if time.Now().UTC().After(token.ExpiresAt) {
		return auth.ErrTokenExpired
	}

	// Mark token as used
	now := time.Now().UTC()
	query := `UPDATE password_reset_tokens SET used_at = $1 WHERE id = $2`
	_, err = r.db.ExecContext(ctx, query, now, token.ID)
	if err != nil {
		return fmt.Errorf("failed to mark token as used: %w", err)
	}

	// Update the user's password
	err = r.UpdateUserPassword(ctx, token.UserID, newPasswordHash)
	if err != nil {
		return err
	}

	// Invalidate all sessions for security
	err = r.InvalidateAllUserSessions(ctx, token.UserID)
	if err != nil {
		return err
	}

	return nil
}

// ============================================
// Auth Audit Log Repository Methods
// ============================================

// CreateAuthAuditLog creates an auth audit log entry
func (r *PostgresRepository) CreateAuthAuditLog(ctx context.Context, log *AuthAuditLog) error {
	query := `
		INSERT INTO auth_audit_log (id, user_id, email, event_type, status, error_reason, ip_address, user_agent, created_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := r.db.ExecContext(
		ctx, query,
		log.UserID, log.Email, log.EventType, log.Status, log.ErrorReason, log.IPAddress, log.UserAgent, time.Now().UTC(),
	)
	if err != nil {
		// Don't fail operations because of audit log failures - just log it
		return fmt.Errorf("failed to create auth audit log: %w", err)
	}
	return nil
}

// ============================================
// Helper Functions
// ============================================

// isDuplicateKeyError checks if an error is a PostgreSQL unique constraint violation
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	// PostgreSQL error code 23505 is unique_violation
	return strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "unique constraint")
}
