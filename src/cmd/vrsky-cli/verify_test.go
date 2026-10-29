package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	stdhkdf "crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/crypto"
)

// Key separation (review of #471): chunks are sealed under a per-archive
// HKDF subkey, never under ENCRYPTION_KEY itself.

func archiveSalt(t *testing.T, archive []byte) []byte {
	t.Helper()
	if len(archive) < backupHeaderSizeV1 {
		t.Fatalf("archive shorter than its header: %d bytes", len(archive))
	}
	return archive[len(backupMagic)+4 : backupHeaderSizeV1]
}

// v2Info is the HKDF info of a VRSKYBK2 archive with this header:
// "vrsky-backup-v2" || header (#549).
func v2Info(header []byte) []byte {
	return append([]byte("vrsky-backup-v2"), header...)
}

// The derivation is HKDF-SHA256(ENCRYPTION_KEY, salt, info), checked against
// the standard library's HKDF so the parameters are pinned, for v1's info and
// for a v2 one. TestArchivesGetSeparateKeys pins which info v2 uses.
func TestDeriveBackupKeyMatchesHKDF(t *testing.T) {
	master := randomBytes(t, 32)
	salt := randomBytes(t, backupSaltSize)
	for _, info := range []string{"vrsky-backup-v1", string(v2Info(randomBytes(t, testHeaderSize)))} {
		want, err := stdhkdf.Key(sha256.New, master, salt, info, 32)
		if err != nil {
			t.Fatal(err)
		}
		got, err := deriveBackupKey(hex.EncodeToString(master), salt, []byte(info))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("derived key %x, want %x", got, want)
		}
	}
}

func TestArchivesGetSeparateKeys(t *testing.T) {
	keyHex := randomKey(t)
	master, _ := hex.DecodeString(keyHex)
	plain := bytes.Repeat([]byte("same dump "), 1000)
	a, b := seal(t, plain, keyHex, 64), seal(t, plain, keyHex, 64)

	saltA, saltB := archiveSalt(t, a), archiveSalt(t, b)
	if bytes.Equal(saltA, saltB) {
		t.Fatal("two archives share a salt")
	}
	keyA, err := deriveBackupKey(keyHex, saltA, v2Info(a[:testHeaderSize]))
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := deriveBackupKey(keyHex, saltB, v2Info(b[:testHeaderSize]))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(keyA, keyB) {
		t.Fatal("two archives share a subkey")
	}
	if bytes.Equal(keyA, master) || bytes.Equal(keyB, master) {
		t.Fatal("subkey equals ENCRYPTION_KEY")
	}
	if bytes.Equal(a[testHeaderSize:], b[testHeaderSize:]) {
		t.Fatal("two archives of the same input have the same ciphertext")
	}

	// The first chunk opens under the subkey and not under the master key.
	chunk0 := a[testHeaderSize : testHeaderSize+64+gcmTagSize]
	header := a[:testHeaderSize]
	nonce := chunkNonce(nil, 0, false)
	open := func(key []byte) error {
		block, err := aes.NewCipher(key)
		if err != nil {
			t.Fatal(err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			t.Fatal(err)
		}
		_, err = gcm.Open(nil, nonce, chunk0, header)
		return err
	}
	if err := open(keyA); err != nil {
		t.Fatalf("chunk 0 does not open under the derived key: %v", err)
	}
	if err := open(master); err == nil {
		t.Fatal("chunk 0 opens under ENCRYPTION_KEY directly; the subkey is not used")
	}
}

func TestHeaderSaltAndKeyEditsFail(t *testing.T) {
	keyHex := randomKey(t)
	plain := randomBytes(t, 300)
	archive := seal(t, plain, keyHex, 64)
	saltStart := len(backupMagic) + 4

	type tc struct {
		name    string
		archive []byte
		key     string
	}
	var cases []tc
	for _, i := range []int{saltStart, saltStart + 15, backupHeaderSizeV1 - 1} {
		c := bytes.Clone(archive)
		c[i] ^= 0x01
		cases = append(cases, tc{fmt.Sprintf("salt byte %d flipped", i-saltStart), c, keyHex})
	}
	zeroSalt := bytes.Clone(archive)
	copy(zeroSalt[saltStart:backupHeaderSizeV1], make([]byte, backupSaltSize))
	cases = append(cases,
		tc{"salt zeroed", zeroSalt, keyHex},
		tc{"wrong key", archive, randomKey(t)},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := openAll(c.archive, c.key)
			if err == nil {
				t.Fatalf("opened without error (%d bytes, want an error)", len(got))
			}
		})
	}
	if got, err := openAll(archive, keyHex); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("untouched archive: err %v, equal %v", err, bytes.Equal(got, plain))
	}
}

// backup verify

type getStore map[string][]byte

func (g getStore) GetStream(_ context.Context, key string) (io.ReadCloser, string, error) {
	b, ok := g[key]
	if !ok {
		return nil, "", errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(b)), "application/octet-stream", nil
}

func TestVerifyBackup(t *testing.T) {
	keyHex := randomKey(t)
	dump := append([]byte("PGDMP"), randomBytes(t, 200<<10)...)

	put := &memStore{}
	if _, err := streamBackup(context.Background(), put, "mgmt-backups/x.dump.gz.enc", bytes.NewReader(dump), func() error { return nil }, keyHex); err != nil {
		t.Fatalf("backup: %v", err)
	}
	good := put.body
	legacy, err := crypto.EncryptBytes(gzipBytes(t, dump), keyHex)
	if err != nil {
		t.Fatal(err)
	}
	flip := func(b []byte, i int) []byte {
		c := bytes.Clone(b)
		c[i] ^= 0x80
		return c
	}

	cases := []struct {
		name   string
		object []byte
		key    string
		ok     bool
	}{
		{"good archive", good, keyHex, true},
		{"good legacy archive", legacy, keyHex, true},
		{"truncated by one byte", good[:len(good)-1], keyHex, false},
		{"truncated at half", good[:len(good)/2], keyHex, false},
		{"truncated after the header", good[:backupHeaderFixedV2+len("mgmt-backups/x.dump.gz.enc")], keyHex, false},
		{"bit flipped in the middle", flip(good, len(good)/2), keyHex, false},
		{"bit flipped in the last byte", flip(good, len(good)-1), keyHex, false},
		{"bit flipped in the salt", flip(good, len(backupMagic)+4), keyHex, false},
		{"legacy truncated", legacy[:len(legacy)-1], keyHex, false},
		{"legacy bit flipped", flip(legacy, len(legacy)/2), keyHex, false},
		{"wrong key", good, randomKey(t), false},
		{"empty object", nil, keyHex, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := getStore{"mgmt-backups/x.dump.gz.enc": c.object}
			n, _, err := verifyBackup(context.Background(), store, "mgmt-backups/x.dump.gz.enc", c.key, true)
			if c.ok {
				if err != nil {
					t.Fatalf("verify failed on a good archive: %v", err)
				}
				if n != int64(len(dump)) {
					t.Fatalf("verify read %d dump bytes, want %d", n, len(dump))
				}
				return
			}
			if err == nil {
				t.Fatalf("verify passed a damaged archive (%d bytes read)", n)
			}
		})
	}

	if _, _, err := verifyBackup(context.Background(), getStore{}, "missing", keyHex, true); err == nil {
		t.Fatal("verify passed a key that does not exist")
	}
}
