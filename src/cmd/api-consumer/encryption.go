package main

import "github.com/ValueRetail/vrsky/pkg/crypto"

// EncryptToken delegates to the shared crypto package. Kept as a thin wrapper
// during the migration period; remove once no in-tree caller refers to it.
func EncryptToken(plaintext, keyHex string) (string, error) {
	return crypto.Encrypt(plaintext, keyHex)
}

// DecryptToken delegates to the shared crypto package. See EncryptToken.
func DecryptToken(encrypted, keyHex string) (string, error) {
	return crypto.Decrypt(encrypted, keyHex)
}
