package crypto

import (
	"strings"
	"testing"
)

const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestRoundTrip(t *testing.T) {
	cases := []string{
		"hunter2",
		"",
		"a string with spaces and 🦀 unicode",
		strings.Repeat("x", 4096),
	}
	for _, plain := range cases {
		t.Run(plain, func(t *testing.T) {
			ct, err := Encrypt(plain, testKey)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			if plain == "" {
				if ct != "" {
					t.Fatalf("empty plaintext should encrypt to empty string, got %q", ct)
				}
				return
			}
			if !IsCiphertext(ct) {
				t.Fatalf("Encrypt did not emit a ciphertext prefix: %q", ct)
			}
			got, err := Decrypt(ct, testKey)
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}
			if got != plain {
				t.Fatalf("roundtrip mismatch: got %q want %q", got, plain)
			}
		})
	}
}

func TestNonceIsRandom(t *testing.T) {
	a, _ := Encrypt("same plaintext", testKey)
	b, _ := Encrypt("same plaintext", testKey)
	if a == b {
		t.Fatalf("two encryptions of the same plaintext produced identical ciphertexts; nonce is not random")
	}
}

func TestDecryptPlaintextPassthrough(t *testing.T) {
	got, err := Decrypt("legacy-plaintext-value", testKey)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != "legacy-plaintext-value" {
		t.Fatalf("plaintext passthrough failed: %q", got)
	}
}

func TestBadKeyLength(t *testing.T) {
	_, err := Encrypt("x", "deadbeef")
	if err == nil {
		t.Fatalf("expected error on short key")
	}
}

func TestNotHexKey(t *testing.T) {
	_, err := Encrypt("x", strings.Repeat("Z", 64))
	if err == nil {
		t.Fatalf("expected error on non-hex key")
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	ct, _ := Encrypt("hunter2", testKey)
	tampered := ct[:len(ct)-2] + "AA"
	if _, err := Decrypt(tampered, testKey); err == nil {
		t.Fatalf("expected decryption failure on tampered ciphertext")
	}
}

func TestEmptyKeyErrors(t *testing.T) {
	if _, err := Encrypt("x", ""); err == nil {
		t.Fatalf("expected ErrKeyMissing on empty key")
	}
}

func TestIsCiphertext(t *testing.T) {
	if !IsCiphertext("aes256:abc") {
		t.Fatalf("expected aes256: prefix to be recognised")
	}
	if IsCiphertext("plain") {
		t.Fatalf("plain string must not be flagged as ciphertext")
	}
}

func TestMustKey(t *testing.T) {
	t.Setenv(KeyEnvVar, testKey)
	got := MustKey()
	if got != testKey {
		t.Fatalf("MustKey returned %q, want %q", got, testKey)
	}
}

func TestMustKeyPanicsOnMissing(t *testing.T) {
	t.Setenv(KeyEnvVar, "")
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("MustKey should have panicked with missing key")
		}
	}()
	MustKey()
}

func TestKey(t *testing.T) {
	t.Setenv(KeyEnvVar, testKey)
	got, err := Key()
	if err != nil || got != testKey {
		t.Fatalf("Key returned (%q, %v)", got, err)
	}
	t.Setenv(KeyEnvVar, "")
	if _, err := Key(); err == nil {
		t.Fatalf("Key should return ErrKeyMissing when env is empty")
	}
}
