package auth

import "errors"

// Auth error types
var (
	// ErrInvalidCredentials is returned when login credentials are incorrect
	ErrInvalidCredentials = errors.New("invalid email or password")

	// ErrUserNotFound is returned when a user is not found
	ErrUserNotFound = errors.New("user not found")

	// ErrEmailNotVerified is returned when a user tries to login without verifying email
	ErrEmailNotVerified = errors.New("email not verified")

	// ErrUserSuspended is returned when a suspended user tries to login
	ErrUserSuspended = errors.New("user account is suspended")

	// ErrEmailExists is returned when trying to register with an existing email
	ErrEmailExists = errors.New("email already registered")

	// ErrInvalidToken is returned when a token is invalid or malformed
	ErrInvalidToken = errors.New("invalid token")

	// ErrTokenExpired is returned when a token has expired
	ErrTokenExpired = errors.New("token has expired")

	// ErrTokenUsed is returned when a one-time token has already been used
	ErrTokenUsed = errors.New("token has already been used")

	// ErrSessionExpired is returned when a session has expired
	ErrSessionExpired = errors.New("session has expired")

	// ErrSessionInvalid is returned when a session token is invalid
	ErrSessionInvalid = errors.New("invalid session")

	// ErrWeakPassword is returned when password doesn't meet requirements
	ErrWeakPassword = errors.New("password does not meet requirements")
)
