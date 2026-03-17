package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

const encryptionPrefix = "aes256:"

// EncryptToken encrypts a plaintext token using AES-256-GCM
// keyHex: 64 hex characters (32 bytes)
// Returns: "aes256:base64encodedciphertext"
func EncryptToken(plaintext, keyHex string) (string, error) {
	if plaintext == "" {
		return "", nil
	}

	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", fmt.Errorf("invalid encryption key: %w", err)
	}

	if len(key) != 32 {
		return "", fmt.Errorf("encryption key must be 32 bytes (64 hex chars)")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encrypt and prepend nonce
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)

	// Encode as base64 and add prefix
	encoded := base64.StdEncoding.EncodeToString(ciphertext)
	return encryptionPrefix + encoded, nil
}

// DecryptToken decrypts an encrypted token
// encrypted: "aes256:base64encodedciphertext" or plaintext (for backward compatibility)
// keyHex: 64 hex characters (32 bytes)
func DecryptToken(encrypted, keyHex string) (string, error) {
	if encrypted == "" {
		return "", nil
	}

	// If not encrypted (no prefix), return as-is for backward compatibility
	if !strings.HasPrefix(encrypted, encryptionPrefix) {
		return encrypted, nil
	}

	// Remove prefix
	encoded := strings.TrimPrefix(encrypted, encryptionPrefix)

	// Decode base64
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %w", err)
	}

	// Decode key
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", fmt.Errorf("invalid encryption key: %w", err)
	}

	if len(key) != 32 {
		return "", fmt.Errorf("encryption key must be 32 bytes (64 hex chars)")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Extract nonce and ciphertext
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

	// Decrypt
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}
