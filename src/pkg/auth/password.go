// Package auth provides authentication utilities for VRSky
// including password hashing, token generation, and session management.
package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const (
	// DefaultBcryptCost is the bcrypt cost factor for password hashing
	// Cost 12 provides a good balance between security and performance
	// Takes approximately 250-300ms on modern hardware
	DefaultBcryptCost = 12

	// MinPasswordLength is the minimum required password length
	MinPasswordLength = 8
)

// HashPassword hashes a password using bcrypt with the default cost factor.
// Returns the hashed password as a string suitable for storage.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength {
		return "", fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), DefaultBcryptCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}

	return string(hash), nil
}

// VerifyPassword compares a plaintext password with a bcrypt hash.
// Returns nil if the password matches, or an error if it doesn't.
func VerifyPassword(hashedPassword, password string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	if err != nil {
		if err == bcrypt.ErrMismatchedHashAndPassword {
			return ErrInvalidCredentials
		}
		return fmt.Errorf("failed to verify password: %w", err)
	}
	return nil
}

// ValidatePassword checks if a password meets the minimum requirements.
// Returns nil if valid, or an error describing the issue.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	return nil
}
