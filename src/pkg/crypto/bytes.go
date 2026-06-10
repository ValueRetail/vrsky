package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// EncryptBytes encrypts arbitrary binary data (e.g. a pg_dump) with AES-256-GCM
// under the same ENCRYPTION_KEY as the string Encrypt. Unlike Encrypt it returns
// raw bytes with no "aes256:"/base64 envelope — the wire format is simply
// nonce || ciphertext, suitable for writing straight to a file or object store.
// Empty input returns empty output.
func EncryptBytes(plaintext []byte, keyHex string) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	gcm, err := newGCM(keyHex)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("rand.Read: %w", err)
	}
	// Seal appends the ciphertext to nonce, giving nonce || ciphertext.
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// DecryptBytes reverses EncryptBytes.
func DecryptBytes(sealed []byte, keyHex string) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, nil
	}
	gcm, err := newGCM(keyHex)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(sealed) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := sealed[:nonceSize], sealed[nonceSize:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("gcm.Open: %w", err)
	}
	return plain, nil
}

// newGCM builds an AES-256-GCM AEAD from the hex key (shared by the byte funcs).
func newGCM(keyHex string) (cipher.AEAD, error) {
	key, err := decodeKey(keyHex)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}
	return gcm, nil
}
