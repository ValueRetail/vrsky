package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	// SessionTokenPrefix is added to session tokens for identification
	SessionTokenPrefix = "session_"

	// DefaultSessionDuration is the default session expiry time (24 hours)
	DefaultSessionDuration = 24 * time.Hour

	// DefaultVerificationTokenDuration is the default email verification token expiry (1 hour)
	DefaultVerificationTokenDuration = 1 * time.Hour

	// DefaultPasswordResetDuration is the default password reset token expiry (1 hour)
	DefaultPasswordResetDuration = 1 * time.Hour

	// TokenRandomBytes is the number of random bytes in tokens
	TokenRandomBytes = 32
)

// GenerateSessionToken generates a new session token.
// Format: session_{uuid}_{32_random_bytes_hex}
// Returns both the raw token (to send to client) and the hash (to store in DB).
func GenerateSessionToken() (rawToken, hashedToken string, err error) {
	// Generate UUID
	id := uuid.New().String()

	// Generate random bytes
	randomBytes := make([]byte, TokenRandomBytes)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Combine into raw token
	rawToken = fmt.Sprintf("%s%s_%s", SessionTokenPrefix, id, hex.EncodeToString(randomBytes))

	// Hash the token for storage
	hashedToken = HashToken(rawToken)

	return rawToken, hashedToken, nil
}

// GenerateVerificationToken generates a token for email verification.
// Returns both the raw token (to send in email) and the hash (to store in DB).
func GenerateVerificationToken() (rawToken, hashedToken string, err error) {
	// Generate random bytes
	randomBytes := make([]byte, TokenRandomBytes)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Raw token is hex-encoded random bytes
	rawToken = hex.EncodeToString(randomBytes)

	// Hash the token for storage
	hashedToken = HashToken(rawToken)

	return rawToken, hashedToken, nil
}

// GeneratePasswordResetToken generates a token for password reset.
// Returns both the raw token (to send in email) and the hash (to store in DB).
func GeneratePasswordResetToken() (rawToken, hashedToken string, err error) {
	// Uses same generation as verification tokens
	return GenerateVerificationToken()
}

// HashToken hashes a token using SHA-256 for secure storage.
// This is a one-way hash - tokens cannot be recovered from the hash.
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// ValidateTokenExpiry checks if a token has expired.
// Returns true if the token is still valid, false if expired.
func ValidateTokenExpiry(expiresAt time.Time) bool {
	return time.Now().UTC().Before(expiresAt)
}

// CalculateSessionExpiry returns the expiry time for a new session.
func CalculateSessionExpiry() time.Time {
	return time.Now().UTC().Add(DefaultSessionDuration)
}

// CalculateVerificationTokenExpiry returns the expiry time for a verification token.
func CalculateVerificationTokenExpiry() time.Time {
	return time.Now().UTC().Add(DefaultVerificationTokenDuration)
}

// CalculatePasswordResetTokenExpiry returns the expiry time for a password reset token.
func CalculatePasswordResetTokenExpiry() time.Time {
	return time.Now().UTC().Add(DefaultPasswordResetDuration)
}
