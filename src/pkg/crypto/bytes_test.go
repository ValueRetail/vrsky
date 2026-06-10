package crypto

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestEncryptBytes_RoundTrip(t *testing.T) {
	cases := map[string][]byte{
		"small":  []byte("a tiny pg_dump"),
		"binary": {0x00, 0x01, 0xff, 0xfe, 0x7f, 0x80},
	}
	for name, plain := range cases {
		t.Run(name, func(t *testing.T) {
			sealed, err := EncryptBytes(plain, testKey)
			if err != nil {
				t.Fatalf("EncryptBytes: %v", err)
			}
			if bytes.Equal(sealed, plain) {
				t.Fatal("ciphertext equals plaintext")
			}
			out, err := DecryptBytes(sealed, testKey)
			if err != nil {
				t.Fatalf("DecryptBytes: %v", err)
			}
			if !bytes.Equal(out, plain) {
				t.Errorf("round-trip mismatch: got %v want %v", out, plain)
			}
		})
	}
}

func TestEncryptBytes_LargeBlob(t *testing.T) {
	// A multi-MB blob like a real dump.
	plain := make([]byte, 4*1024*1024)
	if _, err := rand.Read(plain); err != nil {
		t.Fatalf("rand: %v", err)
	}
	sealed, err := EncryptBytes(plain, testKey)
	if err != nil {
		t.Fatalf("EncryptBytes: %v", err)
	}
	out, err := DecryptBytes(sealed, testKey)
	if err != nil {
		t.Fatalf("DecryptBytes: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Error("large-blob round-trip mismatch")
	}
}

func TestEncryptBytes_Empty(t *testing.T) {
	sealed, err := EncryptBytes(nil, testKey)
	if err != nil || sealed != nil {
		t.Errorf("empty EncryptBytes = %v, %v; want nil, nil", sealed, err)
	}
	out, err := DecryptBytes(nil, testKey)
	if err != nil || out != nil {
		t.Errorf("empty DecryptBytes = %v, %v; want nil, nil", out, err)
	}
}

func TestDecryptBytes_WrongKeyFails(t *testing.T) {
	sealed, err := EncryptBytes([]byte("secret dump"), testKey)
	if err != nil {
		t.Fatalf("EncryptBytes: %v", err)
	}
	otherKey := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, err := DecryptBytes(sealed, otherKey); err == nil {
		t.Fatal("decrypt with wrong key should fail (GCM auth)")
	}
	// Tampering with a byte must fail authentication too.
	sealed[len(sealed)-1] ^= 0xff
	if _, err := DecryptBytes(sealed, testKey); err == nil {
		t.Fatal("decrypt of tampered ciphertext should fail")
	}
}
