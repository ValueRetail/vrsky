// Package crypto provides AES-256-GCM symmetric encryption used to protect
// connector credentials at rest. The on-disk format is:
//
//	"aes256:" + base64( nonce || ciphertext )
//
// The 32-byte key is read from the ENCRYPTION_KEY environment variable as
// 64 hex characters. All services that touch credentials (management-api and
// each *-consumer / *-producer worker) share this package.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	// Prefix marks a value as encrypted with the v1 AES-256-GCM scheme.
	Prefix = "aes256:"

	// KeyEnvVar is the env var holding the master key (64 hex chars).
	KeyEnvVar = "ENCRYPTION_KEY"
)

// ErrKeyMissing is returned when ENCRYPTION_KEY is unset or malformed.
var ErrKeyMissing = errors.New("ENCRYPTION_KEY must be set to 64 hex characters (32 bytes)")

// Encrypt produces "aes256:base64(nonce||ciphertext)" from plaintext.
// Empty plaintext returns empty string (callers can use this to opt out).
func Encrypt(plaintext, keyHex string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	key, err := decodeKey(keyHex)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("cipher.NewGCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return Prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. Values without the prefix are returned as-is
// to support a migration window during which some configs still hold
// plaintext.
func Decrypt(encrypted, keyHex string) (string, error) {
	if encrypted == "" {
		return "", nil
	}
	if !IsCiphertext(encrypted) {
		return encrypted, nil
	}
	encoded := strings.TrimPrefix(encrypted, Prefix)
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	key, err := decodeKey(keyHex)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("cipher.NewGCM: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(sealed) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := sealed[:nonceSize], sealed[nonceSize:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("gcm.Open: %w", err)
	}
	return string(plain), nil
}

// IsCiphertext reports whether the string carries our encryption prefix.
func IsCiphertext(s string) bool {
	return strings.HasPrefix(s, Prefix)
}

// MustKey reads ENCRYPTION_KEY and panics if it is missing or malformed.
// Call this once at service startup so misconfiguration surfaces immediately
// instead of on the first decrypt attempt.
func MustKey() string {
	keyHex := os.Getenv(KeyEnvVar)
	if _, err := decodeKey(keyHex); err != nil {
		panic(err)
	}
	return keyHex
}

// Key returns the configured ENCRYPTION_KEY or ErrKeyMissing without panicking.
func Key() (string, error) {
	keyHex := os.Getenv(KeyEnvVar)
	if _, err := decodeKey(keyHex); err != nil {
		return "", err
	}
	return keyHex, nil
}

func decodeKey(keyHex string) ([]byte, error) {
	if keyHex == "" {
		return nil, ErrKeyMissing
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, ErrKeyMissing
	}
	if len(key) != 32 {
		return nil, ErrKeyMissing
	}
	return key, nil
}
