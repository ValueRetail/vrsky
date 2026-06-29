package managementapi

import (
	"time"

	"github.com/google/uuid"
)

// UserStatus represents the status of a user account
type UserStatus string

const (
	UserStatusPending   UserStatus = "pending"
	UserStatusActive    UserStatus = "active"
	UserStatusSuspended UserStatus = "suspended"
	UserStatusDeleted   UserStatus = "deleted"
)

// User represents a user account in the system
type User struct {
	ID              string     `json:"id" db:"id"`
	Email           string     `json:"email" db:"email"`
	PasswordHash    string     `json:"-" db:"password_hash"` // Never expose in JSON
	FullName        string     `json:"full_name" db:"full_name"`
	Status          UserStatus `json:"status" db:"status"`
	EmailVerified   bool       `json:"email_verified" db:"email_verified"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty" db:"email_verified_at"`
	CreatedAt       time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at" db:"updated_at"`
	LastLoginAt     *time.Time `json:"last_login_at,omitempty" db:"last_login_at"`
	DeletedAt       *time.Time `json:"-" db:"deleted_at"`
	// OIDC identity link (Phase 1C / #68). Empty until the user signs in
	// via SSO at least once.
	OIDCProvider string `json:"oidc_provider,omitempty" db:"oidc_provider"`
	OIDCSubject  string `json:"-" db:"oidc_subject"`
}

// Session represents a user login session
type Session struct {
	ID           string    `json:"id" db:"id"`
	UserID       string    `json:"user_id" db:"user_id"`
	TokenHash    string    `json:"-" db:"token_hash"` // Never expose
	IPAddress    *string   `json:"ip_address,omitempty" db:"ip_address"`
	UserAgent    *string   `json:"user_agent,omitempty" db:"user_agent"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	ExpiresAt    time.Time `json:"expires_at" db:"expires_at"`
	LastActivity time.Time `json:"last_activity" db:"last_activity"`
	IsActive     bool      `json:"is_active" db:"is_active"`
}

// EmailVerificationToken represents an email verification token
type EmailVerificationToken struct {
	ID        string     `json:"id" db:"id"`
	UserID    string     `json:"user_id" db:"user_id"`
	TokenHash string     `json:"-" db:"token_hash"`
	CreatedAt time.Time  `json:"created_at" db:"created_at"`
	ExpiresAt time.Time  `json:"expires_at" db:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty" db:"used_at"`
}

// PasswordResetToken represents a password reset token
type PasswordResetToken struct {
	ID        string     `json:"id" db:"id"`
	UserID    string     `json:"user_id" db:"user_id"`
	TokenHash string     `json:"-" db:"token_hash"`
	CreatedAt time.Time  `json:"created_at" db:"created_at"`
	ExpiresAt time.Time  `json:"expires_at" db:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty" db:"used_at"`
}

// AuthAuditLog represents an authentication audit log entry
type AuthAuditLog struct {
	ID          string    `json:"id" db:"id"`
	UserID      *string   `json:"user_id,omitempty" db:"user_id"`
	Email       string    `json:"email" db:"email"`
	EventType   string    `json:"event_type" db:"event_type"`
	Status      string    `json:"status" db:"status"`
	ErrorReason *string   `json:"error_reason,omitempty" db:"error_reason"`
	IPAddress   *string   `json:"ip_address,omitempty" db:"ip_address"`
	UserAgent   *string   `json:"user_agent,omitempty" db:"user_agent"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

// NewUser creates a new User with default values
func NewUser(email, passwordHash, fullName string) *User {
	now := time.Now().UTC()
	return &User{
		ID:            uuid.New().String(),
		Email:         email,
		PasswordHash:  passwordHash,
		FullName:      fullName,
		Status:        UserStatusPending,
		EmailVerified: false,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

// CanLogin checks if a user is allowed to login
func (u *User) CanLogin() error {
	if u.Status == UserStatusSuspended {
		return &UnauthorizedError{Message: "account is suspended"}
	}
	if u.Status == UserStatusDeleted {
		return &UnauthorizedError{Message: "account not found"}
	}
	if u.Status == UserStatusPending || !u.EmailVerified {
		return &UnauthorizedError{Message: "email not verified"}
	}
	return nil
}

// UserResponse is the safe user data to return in API responses
type UserResponse struct {
	ID            string     `json:"id"`
	Email         string     `json:"email"`
	FullName      string     `json:"full_name"`
	EmailVerified bool       `json:"email_verified"`
	CreatedAt     time.Time  `json:"created_at"`
	LastLoginAt   *time.Time `json:"last_login_at,omitempty"`
}

// ToResponse converts a User to a safe UserResponse
func (u *User) ToResponse() *UserResponse {
	return &UserResponse{
		ID:            u.ID,
		Email:         u.Email,
		FullName:      u.FullName,
		EmailVerified: u.EmailVerified,
		CreatedAt:     u.CreatedAt,
		LastLoginAt:   u.LastLoginAt,
	}
}

// ============================================
// Auth Request/Response Types
// ============================================

// RegisterRequest is the request body for user registration
type RegisterRequest struct {
	Email         string `json:"email"`
	Password      string `json:"password"`
	FullName      string `json:"full_name"`
	WorkspaceName string `json:"workspace_name"`
}

// LoginRequest is the request body for user login
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse is the response body for successful login
type LoginResponse struct {
	Success      bool          `json:"success"`
	SessionToken string        `json:"session_token"`
	User         *UserResponse `json:"user"`
	ExpiresAt    time.Time     `json:"expires_at"`
}

// RegisterResponse is the response body for successful registration
type RegisterResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	UserID  string `json:"user_id"`
}

// ForgotPasswordRequest is the request body for password reset request
type ForgotPasswordRequest struct {
	Email string `json:"email"`
}

// ResetPasswordRequest is the request body for password reset
type ResetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// ChangePasswordRequest is the request body for changing password while logged in
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// MeResponse is the response body for GET /auth/me
type MeResponse struct {
	User             *UserResponse     `json:"user"`
	SessionExpiresAt time.Time         `json:"session_expires_at"`
	Tenants          []*TenantResponse `json:"tenants"`
	CurrentTenant    *TenantResponse   `json:"current_tenant"`
}

// MessageResponse is a simple success/error message response
type MessageResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}
