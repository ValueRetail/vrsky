package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	stdhkdf "crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/crypto"
)

// Tests for binding a backup archive to its object key (#549), written
// against the acceptance criteria and the format in
// plans/issue-549-bind-archive.md, not the implementation. The v1 and v2
// formats are re-implemented here from the spec ("spec*" helpers): v1 because
// production code can no longer write it, v2 so the tests pin the exact
// format (header in the HKDF info and in every chunk's AAD) rather than
// whatever the writer and reader happen to agree on.

// Byte offsets of the v2 header fields, from the spec.
const (
	offChunk   = len(backupMagicPrefix) + 1 // 8
	offSalt    = offChunk + 4               // 12
	offCreated = offSalt + backupSaltSize   // 44
	offKeyLen  = offCreated + 8             // 52
	offKey     = offKeyLen + 2              // 54
)

// --- spec implementation -------------------------------------------------------

func specHeaderV1(chunk uint32, salt []byte) []byte {
	h := []byte(backupMagicV1)
	h = binary.BigEndian.AppendUint32(h, chunk)
	return append(h, salt...)
}

// specHeaderV2 writes keyLen as given so a test can make the length field lie.
func specHeaderV2(chunk uint32, salt []byte, created int64, keyLen uint16, key []byte) []byte {
	h := []byte(backupMagicV2)
	h = binary.BigEndian.AppendUint32(h, chunk)
	h = append(h, salt...)
	h = binary.BigEndian.AppendUint64(h, uint64(created))
	h = binary.BigEndian.AppendUint16(h, keyLen)
	return append(h, key...)
}

func specV2Info(header []byte) []byte {
	return append([]byte(backupKDFInfoV2), header...)
}

// specNonce is 0^7 || uint32 BE counter || last flag.
func specNonce(i uint32, last bool) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint32(n[7:11], i)
	if last {
		n[11] = 1
	}
	return n
}

func specSubkey(t testing.TB, keyHex string, salt, info []byte) []byte {
	t.Helper()
	master, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := stdhkdf.Key(sha256.New, master, salt, string(info), 32)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func gcmFor(t testing.TB, key []byte) cipher.AEAD {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return gcm
}

// specSealChunks returns header || chunk_0 || ... || chunk_n, every chunk
// sealed with aad. Every chunk but the last is full; the last is empty only
// when plain is.
func specSealChunks(aead cipher.AEAD, header, aad []byte, chunk int, plain []byte) []byte {
	out := bytes.Clone(header)
	for i, off := uint32(0), 0; ; i++ {
		end := min(off+chunk, len(plain))
		last := end == len(plain)
		out = aead.Seal(out, specNonce(i, last), plain[off:end], aad)
		if last {
			return out
		}
		off = end
	}
}

// specSealV1 writes a VRSKYBK1 archive, which production code no longer can.
func specSealV1(t testing.TB, plain []byte, keyHex string, chunk int) []byte {
	t.Helper()
	salt := randomBytes(t, backupSaltSize)
	header := specHeaderV1(uint32(chunk), salt)
	aead := gcmFor(t, specSubkey(t, keyHex, salt, []byte(backupKDFInfoV1)))
	return specSealChunks(aead, header, header, chunk, plain)
}

// specSealV2 writes a VRSKYBK2 archive from the spec, without the writer's
// validation of the key, so tests can make archives the writer refuses.
func specSealV2(t testing.TB, plain []byte, keyHex string, key []byte, created int64, chunk int) []byte {
	t.Helper()
	salt := randomBytes(t, backupSaltSize)
	header := specHeaderV2(uint32(chunk), salt, created, uint16(len(key)), key)
	aead := gcmFor(t, specSubkey(t, keyHex, salt, specV2Info(header)))
	return specSealChunks(aead, header, header, chunk, plain)
}

// specOpenV2 decrypts a VRSKYBK2 archive following the spec alone.
func specOpenV2(t testing.TB, archive []byte, keyHex string) ([]byte, error) {
	t.Helper()
	if len(archive) < offKey || string(archive[:8]) != backupMagicV2 {
		return nil, errors.New("not a v2 archive")
	}
	chunk := int(binary.BigEndian.Uint32(archive[offChunk:]))
	salt := archive[offSalt:offCreated]
	keyLen := int(binary.BigEndian.Uint16(archive[offKeyLen:]))
	if len(archive) < offKey+keyLen {
		return nil, errors.New("short header")
	}
	header := archive[:offKey+keyLen]
	aead := gcmFor(t, specSubkey(t, keyHex, salt, specV2Info(header)))
	rest := archive[len(header):]
	var out []byte
	for i := uint32(0); ; i++ {
		rec := chunk + gcmTagSize
		last := len(rest) <= rec
		n := min(rec, len(rest))
		p, err := aead.Open(nil, specNonce(i, last), rest[:n], header)
		if err != nil {
			return nil, fmt.Errorf("chunk %d: %w", i, err)
		}
		out = append(out, p...)
		if last {
			return out, nil
		}
		rest = rest[n:]
	}
}

// --- helpers --------------------------------------------------------------------

// sealBound seals plain bound to objectKey and created; chunk 0 uses the
// default writer.
func sealBound(t testing.TB, plain []byte, keyHex, objectKey string, created time.Time, chunk int) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.WriteCloser
	var err error
	if chunk == 0 {
		w, err = newSealWriter(&buf, keyHex, objectKey, created)
	} else {
		w, err = newSealWriterSize(&buf, keyHex, objectKey, created, chunk)
	}
	if err != nil {
		t.Fatalf("new seal writer: %v", err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// openArchiveAll reads a backup object stored under objectKey to the end
// through openArchive.
func openArchiveAll(object []byte, keyHex, objectKey string, allowUnbound bool) ([]byte, archiveInfo, error) {
	r, info, err := openArchive(bytes.NewReader(object), keyHex, objectKey, allowUnbound)
	if err != nil {
		return nil, info, err
	}
	b, err := io.ReadAll(r)
	return b, info, err
}

// verifyAt runs backup verify on object stored under objectKey.
func verifyAt(object []byte, objectKey, keyHex string, allowUnbound bool) (int64, archiveInfo, error) {
	return verifyBackup(context.Background(), getStore{objectKey: object}, objectKey, keyHex, allowUnbound)
}

// mustReject checks that both verify and restore refuse object under
// objectKey, with and without --allow-unbound, and that no dump bytes come out.
func mustReject(t *testing.T, what string, object []byte, keyHex, objectKey string) {
	t.Helper()
	reject(t, what, object, keyHex, objectKey, true)
}

// mustFail is mustReject for damage past the first chunk, where the chunks
// before it have already been streamed out by the time it is found.
func mustFail(t *testing.T, what string, object []byte, keyHex, objectKey string) {
	t.Helper()
	reject(t, what, object, keyHex, objectKey, false)
}

func reject(t *testing.T, what string, object []byte, keyHex, objectKey string, noBytes bool) {
	t.Helper()
	for _, allow := range []bool{false, true} {
		got, _, err := openArchiveAll(object, keyHex, objectKey, allow)
		if err == nil {
			t.Errorf("%s: openArchive(allowUnbound=%v) accepted it (%d bytes)", what, allow, len(got))
		} else if noBytes && len(got) > 0 {
			t.Errorf("%s: openArchive(allowUnbound=%v) yielded %d dump bytes before failing", what, allow, len(got))
		}
		if n, _, err := verifyAt(object, objectKey, keyHex, allow); err == nil {
			t.Errorf("%s: verify(allowUnbound=%v) accepted it (%d bytes)", what, allow, n)
		}
	}
}

// nearMissKeys are object keys an attacker could copy testObjectKey's archive
// to, each close to the real one.
func nearMissKeys() map[string]string {
	real := testObjectKey // mgmt-backups/management_db-20261029T110000Z.dump.gz.enc
	return map[string]string{
		"other prefix":      "dr-backups/" + strings.TrimPrefix(real, "mgmt-backups/"),
		"newer stamp":       strings.Replace(real, "20261029T110000Z", "20261030T110000Z", 1),
		"older stamp":       strings.Replace(real, "20261029T110000Z", "20261028T110000Z", 1),
		"one char off":      real[:len(real)-1] + "d",
		"other database":    strings.Replace(real, "management_db", "tenant_db", 1),
		"trailing slash":    real + "/",
		"trailing space":    real + " ",
		"leading space":     " " + real,
		"leading slash":     "/" + real,
		"dot slash":         "./" + real,
		"trailing NUL":      real + "\x00",
		"prefix of real":    strings.TrimSuffix(real, ".enc"),
		"suffix of real":    strings.TrimPrefix(real, "mgmt-backups/"),
		"real twice":        real + real,
		"upper case":        strings.ToUpper(real),
		"one letter case":   "M" + real[1:],
		"single char":       "m",
		"different, longer": real + ".copy",
	}
}

func setUint64(b []byte, off int, v uint64) []byte {
	c := bytes.Clone(b)
	binary.BigEndian.PutUint64(c[off:], v)
	return c
}

func setUint32(b []byte, off int, v uint32) []byte {
	c := bytes.Clone(b)
	binary.BigEndian.PutUint32(c[off:], v)
	return c
}

func setUint16(b []byte, off int, v uint16) []byte {
	c := bytes.Clone(b)
	binary.BigEndian.PutUint16(c[off:], v)
	return c
}

// rewriteKey replaces the header's key (oldLen bytes) with newKey and writes
// lenField into the key length field: what an attacker who can write to the
// bucket can do to any header.
func rewriteKey(archive []byte, oldLen int, newKey string, lenField uint16) []byte {
	out := bytes.Clone(archive[:offKeyLen])
	out = binary.BigEndian.AppendUint16(out, lenField)
	out = append(out, newKey...)
	return append(out, archive[offKey+oldLen:]...)
}

// --- 1. An archive copied to another key ----------------------------------------

func TestBoundArchiveRejectedUnderOtherKey(t *testing.T) {
	keyHex := randomKey(t)
	dump := append([]byte("PGDMP"), randomBytes(t, 3000)...)
	store := &memStore{}
	if _, err := streamBackup(context.Background(), store, testObjectKey, bytes.NewReader(dump), func() error { return nil }, keyHex); err != nil {
		t.Fatal(err)
	}
	obj := store.body
	h := backupHeaderFixedV2 + len(testObjectKey)

	// Control: under its own key it verifies and restores without the flag.
	if got, _, err := openArchiveAll(obj, keyHex, testObjectKey, false); err != nil || !bytes.Equal(got, dump) {
		t.Fatalf("archive does not restore under its own key: %v", err)
	}
	if n, _, err := verifyAt(obj, testObjectKey, keyHex, false); err != nil || n != int64(len(dump)) {
		t.Fatalf("archive does not verify under its own key: n=%d err=%v", n, err)
	}

	// The same header with every chunk byte scrambled, and the header alone.
	// If the key is checked before any chunk is decrypted, these give the
	// same error as the intact archive.
	garbage := bytes.Clone(obj)
	for i := h; i < len(garbage); i++ {
		garbage[i] ^= 0x5a
	}
	headerOnly := obj[:h]

	for name, target := range nearMissKeys() {
		t.Run(name, func(t *testing.T) {
			for _, allow := range []bool{false, true} {
				got, _, err := openArchiveAll(obj, keyHex, target, allow)
				if err == nil {
					t.Fatalf("openArchive(allowUnbound=%v) restored %d bytes stored under %q", allow, len(got), target)
				}
				if len(got) > 0 {
					t.Errorf("openArchive(allowUnbound=%v) yielded %d dump bytes", allow, len(got))
				}
				if want := strconv.Quote(testObjectKey); !strings.Contains(err.Error(), want) {
					t.Errorf("openArchive error %q does not name the archive's own key %s", err, want)
				}
				_, _, errG := openArchiveAll(garbage, keyHex, target, allow)
				_, _, errH := openArchiveAll(headerOnly, keyHex, target, allow)
				if errG == nil || errG.Error() != err.Error() {
					t.Errorf("scrambled chunks give %v, intact archive %q: the key is not checked before decryption", errG, err)
				}
				if errH == nil || errH.Error() != err.Error() {
					t.Errorf("header alone gives %v, intact archive %q: the key is not checked before decryption", errH, err)
				}

				n, _, err := verifyAt(obj, target, keyHex, allow)
				if err == nil {
					t.Fatalf("verify(allowUnbound=%v) passed under %q (%d bytes)", allow, target, n)
				}
				if n != 0 {
					t.Errorf("verify(allowUnbound=%v) read %d dump bytes before refusing", allow, n)
				}
			}
		})
	}
}

const capturePgRestoreScript = `#!/bin/sh
cat > "$BIND_PGR_STDIN"
`

// installCapturingPgRestore puts a pg_restore on PATH that saves its stdin to
// the returned file.
func installCapturingPgRestore(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake pg_restore is a /bin/sh script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pg_restore"), []byte(capturePgRestoreScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out := filepath.Join(t.TempDir(), "stdin")
	t.Setenv("BIND_PGR_STDIN", out)
	return out
}

// Nothing of a refused archive reaches pg_restore: the restore path (open,
// then pgRestore) stops at openArchive.
func TestRefusedArchiveNeverReachesPgRestore(t *testing.T) {
	captured := installCapturingPgRestore(t)
	keyHex := randomKey(t)
	dump := append([]byte("PGDMP"), randomBytes(t, 200<<10)...)
	store := &memStore{}
	if _, err := streamBackup(context.Background(), store, testObjectKey, bytes.NewReader(dump), func() error { return nil }, keyHex); err != nil {
		t.Fatal(err)
	}
	v1 := specSealV1(t, gzipBytes(t, dump), keyHex, backupChunkSize)
	legacy, err := crypto.EncryptBytes(gzipBytes(t, dump), keyHex)
	if err != nil {
		t.Fatal(err)
	}

	restore := func(object []byte, objectKey string, allow bool) error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		r, _, err := openArchive(bytes.NewReader(object), keyHex, objectKey, allow)
		if err != nil {
			return err
		}
		_, err = pgRestore(ctx, testTargetURL, r)
		return err
	}

	// Control: the harness sees a good restore.
	if err := restore(store.body, testObjectKey, false); err != nil {
		t.Fatalf("restore under its own key: %v", err)
	}
	if got, err := os.ReadFile(captured); err != nil || !bytes.Equal(got, dump) {
		t.Fatalf("fake pg_restore got %d bytes (err %v), want the %d-byte dump", len(got), err, len(dump))
	}

	type tc struct {
		name      string
		object    []byte
		objectKey string
		allow     bool
	}
	cases := []tc{
		{"v1 without --allow-unbound", v1, testObjectKey, false},
		{"legacy without --allow-unbound", legacy, testObjectKey, false},
	}
	for name, target := range nearMissKeys() {
		cases = append(cases, tc{name, store.body, target, false}, tc{name + " with --allow-unbound", store.body, target, true})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := os.Remove(captured); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := restore(c.object, c.objectKey, c.allow); err == nil {
				t.Fatal("restore succeeded")
			}
			if b, err := os.ReadFile(captured); err == nil {
				t.Fatalf("pg_restore ran and received %d bytes", len(b))
			}
		})
	}
}

// --- 2. Edited header fields ----------------------------------------------------

func TestHeaderEditsFailEvenWithKeyRewritten(t *testing.T) {
	keyHex := randomKey(t)
	dump := randomBytes(t, 500)
	const chunk = 16
	obj := sealBound(t, gzipBytes(t, dump), keyHex, testObjectKey, testCreated, chunk)
	kl := len(testObjectKey)
	if got, _, err := openArchiveAll(obj, keyHex, testObjectKey, false); err != nil || !bytes.Equal(got, dump) {
		t.Fatalf("baseline does not restore: %v", err)
	}

	sameLen := strings.Replace(testObjectKey, "20261029T110000Z", "20261030T110000Z", 1)
	shorter := "mgmt-backups/x.dump.gz.enc"
	longer := testObjectKey + ".copy"
	created := testCreated.Unix()
	if len(sameLen) != kl {
		t.Fatal("test key lengths differ")
	}

	type tc struct {
		name   string
		object []byte
		readAs string // the key the attacker stores it under
	}
	cases := []tc{
		{"key rewritten, same length", rewriteKey(obj, kl, sameLen, uint16(kl)), sameLen},
		{"key rewritten, shorter, length fixed", rewriteKey(obj, kl, shorter, uint16(len(shorter))), shorter},
		{"key rewritten, longer, length fixed", rewriteKey(obj, kl, longer, uint16(len(longer))), longer},
		{"key rewritten, shorter, length left", rewriteKey(obj, kl, shorter, uint16(kl)), shorter},
		{"key rewritten to one byte", rewriteKey(obj, kl, "m", 1), "m"},
		{"created +1s", setUint64(obj, offCreated, uint64(created+1)), testObjectKey},
		{"created -1s", setUint64(obj, offCreated, uint64(created-1)), testObjectKey},
		{"created +1 day", setUint64(obj, offCreated, uint64(created+86400)), testObjectKey},
		{"created zero", setUint64(obj, offCreated, 0), testObjectKey},
		{"created negative", setUint64(obj, offCreated, 1<<63), testObjectKey},
		{"created max", setUint64(obj, offCreated, 1<<63-1), testObjectKey},
		{"chunk size 15", setUint32(obj, offChunk, chunk-1), testObjectKey},
		{"chunk size 17", setUint32(obj, offChunk, chunk+1), testObjectKey},
		{"chunk size doubled", setUint32(obj, offChunk, 2*chunk), testObjectKey},
		{"chunk size 0", setUint32(obj, offChunk, 0), testObjectKey},
		{"chunk size over max", setUint32(obj, offChunk, maxBackupChunk+1), testObjectKey},
		{"chunk size 2^32-1", setUint32(obj, offChunk, 1<<32-1), testObjectKey},
		{"key length +1", setUint16(obj, offKeyLen, uint16(kl+1)), testObjectKey},
		{"key length -1", setUint16(obj, offKeyLen, uint16(kl-1)), testObjectKey},
		{"key length 0", setUint16(obj, offKeyLen, 0), testObjectKey},
		{"key length 1025", setUint16(obj, offKeyLen, maxObjectKeyLen+1), testObjectKey},
		{"key length 0xffff", setUint16(obj, offKeyLen, 0xffff), testObjectKey},
		{"key length -1, read as shorter key", setUint16(obj, offKeyLen, uint16(kl-1)), testObjectKey[:kl-1]},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if bytes.Equal(c.object, obj) {
				t.Fatal("edit changed nothing")
			}
			for _, as := range []string{c.readAs, testObjectKey} {
				mustReject(t, "stored under "+strconv.Quote(as), c.object, keyHex, as)
				for _, allow := range []bool{false, true} {
					if n, _, err := verifyAt(c.object, as, keyHex, allow); err != nil && n != 0 {
						t.Errorf("verify read %d dump bytes before failing", n)
					}
				}
			}
			if got, err := openAll(c.object, keyHex); err == nil || len(got) > 0 {
				t.Errorf("newOpenReader: %d bytes, err %v; want an error and no plaintext", len(got), err)
			}
		})
	}
}

// --- 3. What the header reports -------------------------------------------------

func TestArchiveInfoReportsBinding(t *testing.T) {
	keyHex := randomKey(t)
	dump := randomBytes(t, 1000)
	cet := time.FixedZone("CET", 3600)
	for _, created := range []time.Time{
		testCreated,
		time.Date(2026, 10, 29, 12, 34, 56, 999_999_999, cet),
		time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
	} {
		t.Run(created.String(), func(t *testing.T) {
			obj := sealBound(t, gzipBytes(t, dump), keyHex, testObjectKey, created, 0)
			want := time.Unix(created.Unix(), 0).UTC()
			check := func(where string, info archiveInfo) {
				t.Helper()
				if info.format != backupMagicV2 || !info.bound() {
					t.Errorf("%s: format %q bound %v, want %q bound", where, info.format, info.bound(), backupMagicV2)
				}
				if info.key != testObjectKey {
					t.Errorf("%s: key %q, want %q", where, info.key, testObjectKey)
				}
				if !info.created.Equal(want) || info.created.Location() != time.UTC {
					t.Errorf("%s: created %v (%v), want %v in UTC", where, info.created, info.created.Location(), want)
				}
			}
			got, info, err := openArchiveAll(obj, keyHex, testObjectKey, false)
			if err != nil || !bytes.Equal(got, dump) {
				t.Fatalf("openArchive: %v", err)
			}
			check("openArchive", info)
			n, info, err := verifyAt(obj, testObjectKey, keyHex, false)
			if err != nil || n != int64(len(dump)) {
				t.Fatalf("verify: n=%d err=%v", n, err)
			}
			check("verify", info)
			_, info, err = newOpenReader(bytes.NewReader(obj), keyHex)
			if err != nil {
				t.Fatal(err)
			}
			check("newOpenReader", info)
			if d := describeArchive(info); !strings.Contains(d, backupMagicV2) || !strings.Contains(d, want.Format(time.RFC3339)) {
				t.Errorf("describeArchive = %q, want the format and the creation time %s", d, want.Format(time.RFC3339))
			}
		})
	}

	// streamBackup binds exactly the key it stored under, and the time it ran.
	for _, key := range []string{"k", testObjectKey, "backups/æøå 2026-10-29.dump.gz.enc"} {
		t.Run("streamBackup/"+key, func(t *testing.T) {
			store := &memStore{}
			before := time.Now()
			if _, err := streamBackup(context.Background(), store, key, bytes.NewReader(dump), func() error { return nil }, keyHex); err != nil {
				t.Fatal(err)
			}
			after := time.Now()
			if store.key != key {
				t.Fatalf("stored under %q, want %q", store.key, key)
			}
			_, info, err := openArchiveAll(store.body, keyHex, store.key, false)
			if err != nil {
				t.Fatalf("openArchive: %v", err)
			}
			if info.key != store.key || !info.bound() {
				t.Errorf("archive bound to %q (bound %v), stored under %q", info.key, info.bound(), store.key)
			}
			if c := info.created.Unix(); c < before.Unix() || c > after.Unix() {
				t.Errorf("created %v not within the call [%v, %v]", info.created, before, after)
			}
		})
	}
}

// --- 4. Unbound archives (v1, legacy) -------------------------------------------

func TestUnboundArchivesNeedFlag(t *testing.T) {
	keyHex := randomKey(t)
	type tc struct {
		name   string
		dump   []byte
		object func(gz []byte) []byte
		format string
	}
	v1 := func(chunk int) func([]byte) []byte {
		return func(gz []byte) []byte { return specSealV1(t, gz, keyHex, chunk) }
	}
	legacy := func(gz []byte) []byte {
		b, err := crypto.EncryptBytes(gz, keyHex)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := []tc{
		{"v1 chunk 16, small", randomBytes(t, 300), v1(16), backupMagicV1},
		{"v1 chunk 16, one byte", []byte{7}, v1(16), backupMagicV1},
		{"v1 default chunk, several chunks", randomBytes(t, 200<<10), v1(backupChunkSize), backupMagicV1},
		{"v1 default chunk, text", []byte(strings.Repeat("COPY t FROM stdin;\n", 5000)), v1(backupChunkSize), backupMagicV1},
		{"legacy small", randomBytes(t, 300), legacy, formatLegacy},
		{"legacy 200 KiB", randomBytes(t, 200<<10), legacy, formatLegacy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gz := gzipBytes(t, c.dump)
			obj := c.object(gz)

			got, info, err := openArchiveAll(obj, keyHex, testObjectKey, true)
			if err != nil {
				t.Fatalf("openArchive with --allow-unbound: %v", err)
			}
			if !bytes.Equal(got, c.dump) {
				t.Fatalf("restored %d bytes, want the identical %d-byte dump", len(got), len(c.dump))
			}
			if info.format != c.format || info.bound() {
				t.Errorf("info format %q bound %v, want %q unbound", info.format, info.bound(), c.format)
			}
			n, vinfo, err := verifyAt(obj, testObjectKey, keyHex, true)
			if err != nil || n != int64(len(c.dump)) {
				t.Fatalf("verify with --allow-unbound: n=%d err=%v", n, err)
			}
			if vinfo.format != c.format || vinfo.bound() {
				t.Errorf("verify info format %q bound %v", vinfo.format, vinfo.bound())
			}

			got, _, err = openArchiveAll(obj, keyHex, testObjectKey, false)
			if err == nil || len(got) > 0 {
				t.Fatalf("openArchive without --allow-unbound: %d bytes, err %v", len(got), err)
			}
			if !strings.Contains(err.Error(), "--allow-unbound") {
				t.Errorf("openArchive error %q does not name --allow-unbound", err)
			}
			n, _, err = verifyAt(obj, testObjectKey, keyHex, false)
			if err == nil {
				t.Fatalf("verify without --allow-unbound passed (%d bytes)", n)
			}
			if !strings.Contains(err.Error(), "--allow-unbound") {
				t.Errorf("verify error %q does not name --allow-unbound", err)
			}

			w := unboundWarning(testObjectKey, info)
			if !strings.Contains(w, testObjectKey) {
				t.Errorf("unboundWarning %q does not name the key", w)
			}
			if lw := strings.ToLower(w); !strings.Contains(lw, "not bound") && !strings.Contains(lw, "unbound") {
				t.Errorf("unboundWarning %q does not say the archive is not bound", w)
			}
			if describeArchive(info) == "" {
				t.Error("describeArchive is empty for an unbound archive")
			}

			if c.format == backupMagicV1 {
				// newOpenReader reads v1 itself, without a binding decision.
				plain, err := openAll(obj, keyHex)
				if err != nil || !bytes.Equal(plain, gz) {
					t.Fatalf("newOpenReader on v1: %v", err)
				}
			}
		})
	}
}

// The flag only waives the binding: a damaged v1 archive still fails.
func TestDamagedV1FailsWithFlag(t *testing.T) {
	keyHex := randomKey(t)
	const chunk = 16
	dump := randomBytes(t, 150)
	obj := specSealV1(t, gzipBytes(t, dump), keyHex, chunk)
	if got, _, err := openArchiveAll(obj, keyHex, testObjectKey, true); err != nil || !bytes.Equal(got, dump) {
		t.Fatalf("baseline v1 does not restore: %v", err)
	}
	h, rec := backupHeaderSizeV1, chunk+gcmTagSize
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

	type tc struct {
		name   string
		object []byte
		key    string
	}
	var cases []tc
	for n := 0; n < len(obj); n++ {
		cases = append(cases, tc{fmt.Sprintf("truncated to %d", n), obj[:n], keyHex})
	}
	for i := range obj {
		b := bytes.Clone(obj)
		b[i] ^= 0x01
		cases = append(cases, tc{fmt.Sprintf("bit flipped at %d", i), b, keyHex})
	}
	other := specSealV1(t, gzipBytes(t, randomBytes(t, 150)), keyHex, chunk)
	cases = append(cases,
		tc{"wrong key", obj, randomKey(t)},
		tc{"trailing byte", cat(obj, []byte{0}), keyHex},
		tc{"chunk 1 dropped", cat(obj[:h+rec], obj[h+2*rec:]), keyHex},
		tc{"chunks 0 and 1 swapped", cat(obj[:h], obj[h+rec:h+2*rec], obj[h:h+rec], obj[h+2*rec:]), keyHex},
		tc{"header from another v1 archive", cat(other[:h], obj[h:]), keyHex},
		tc{"chunk spliced from another v1 archive", cat(obj[:h+rec], other[h+rec:h+2*rec], obj[h+2*rec:]), keyHex},
	)
	for _, c := range cases {
		got, _, err := openArchiveAll(c.object, c.key, testObjectKey, true)
		if err == nil {
			t.Errorf("%s: restored with --allow-unbound (%d bytes)", c.name, len(got))
		}
	}
	for _, c := range cases[len(cases)-6:] {
		if n, _, err := verifyAt(c.object, testObjectKey, c.key, true); err == nil {
			t.Errorf("%s: verified with --allow-unbound (%d bytes)", c.name, n)
		}
	}
}

// --- 5. Downgrade ---------------------------------------------------------------

func TestDowngradeRefused(t *testing.T) {
	keyHex := randomKey(t)
	dump := randomBytes(t, 100<<10)
	gz := gzipBytes(t, dump)
	obj := sealBound(t, gz, keyHex, testObjectKey, testCreated, 0)
	h := backupHeaderFixedV2 + len(testObjectKey)
	v1 := specSealV1(t, gz, keyHex, backupChunkSize)
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	withMagic := func(m string) []byte { return cat([]byte(m), obj[8:]) }

	cases := map[string][]byte{
		"magic rewritten to VRSKYBK1":            withMagic(backupMagicV1),
		"v1 header (own salt) on v2 chunks":      cat(specHeaderV1(backupChunkSize, obj[offSalt:offCreated]), obj[h:]),
		"v1 header (own salt) and v2 rest":       cat([]byte(backupMagicV1), obj[8:offCreated], obj[h:]),
		"genuine v1 header on v2 chunks":         cat(v1[:backupHeaderSizeV1], obj[h:]),
		"magic rewritten to random bytes":        withMagic("\x13\x37\xc0\xde\x00\xffzz"),
		"magic rewritten to zeros":               withMagic("\x00\x00\x00\x00\x00\x00\x00\x00"),
		"magic rewritten to lower case":          withMagic("vrskybk2"),
		"header stripped (chunks only)":          obj[h:],
		"header stripped, nonce-sized prefix":    cat(randomBytes(t, gcmNonceSize), obj[h:]),
		"first 12 bytes of header kept, rest v2": cat(obj[:gcmNonceSize], obj[h:]),
	}
	for name, object := range cases {
		t.Run(name, func(t *testing.T) {
			mustReject(t, name, object, keyHex, testObjectKey)
			if bytes.HasPrefix(object, []byte(backupMagicV2)) {
				return
			}
			// Without the flag anything that isn't v2 is refused by name.
			_, _, err := openArchiveAll(object, keyHex, testObjectKey, false)
			if err == nil || !strings.Contains(err.Error(), "--allow-unbound") {
				t.Errorf("openArchive without the flag: %v, want an error naming --allow-unbound", err)
			}
		})
	}
}

// legacyWithNonce is a valid pre-#430 archive whose GCM nonce the test picks.
func legacyWithNonce(t *testing.T, gz []byte, keyHex string, nonce []byte) []byte {
	t.Helper()
	master, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatal(err)
	}
	gcm := gcmFor(t, master)
	return gcm.Seal(bytes.Clone(nonce), nonce, gz, nil)
}

func TestUnknownVersionRefused(t *testing.T) {
	keyHex := randomKey(t)
	dump := randomBytes(t, 2000)
	gz := gzipBytes(t, dump)
	obj := sealBound(t, gz, keyHex, testObjectKey, testCreated, 0)

	// The crafted legacy archive is genuine: with a nonce outside the
	// VRSKYBK space it restores.
	ok := legacyWithNonce(t, gz, keyHex, []byte("NOTMAGIC0123"))
	if got, _, err := openArchiveAll(ok, keyHex, testObjectKey, true); err != nil || !bytes.Equal(got, dump) {
		t.Fatalf("crafted legacy archive does not restore: %v", err)
	}

	for _, v := range []byte{'0', '3', '9', 'A', 0x00, 0xff} {
		t.Run(fmt.Sprintf("version %q", v), func(t *testing.T) {
			magic := append([]byte(backupMagicPrefix), v)
			v2Body := bytes.Clone(obj)
			v2Body[7] = v
			// A legacy archive whose random nonce happens to start with this
			// magic: refusing it is right, trying it as legacy would open it.
			asLegacy := legacyWithNonce(t, gz, keyHex, append(bytes.Clone(magic), 1, 2, 3, 4))
			for name, object := range map[string][]byte{"v2 body": v2Body, "legacy body": asLegacy} {
				for _, allow := range []bool{false, true} {
					got, _, err := openArchiveAll(object, keyHex, testObjectKey, allow)
					if err == nil {
						t.Errorf("%s, allowUnbound=%v: restored %d bytes", name, allow, len(got))
						continue
					}
					if !strings.Contains(strings.ToLower(err.Error()), "unsupported") {
						t.Errorf("%s, allowUnbound=%v: error %q does not say unsupported", name, allow, err)
					}
					if n, _, err := verifyAt(object, testObjectKey, keyHex, allow); err == nil {
						t.Errorf("%s, allowUnbound=%v: verify passed (%d bytes)", name, allow, n)
					}
				}
			}
		})
	}
}

// --- 6. Round trips and key limits ----------------------------------------------

func TestBoundRoundTripKeyLengths(t *testing.T) {
	keyHex := randomKey(t)
	keys := map[string]string{
		"len 1":    "k",
		"len 1024": strings.Repeat("a/", maxObjectKeyLen/2),
		"utf-8":    "backups/æøå 2026-10-29.dump.gz.enc",
	}
	for kname, key := range keys {
		for _, c := range []int{16, 0} {
			cs := c
			if c == 0 {
				cs = backupChunkSize
			}
			for _, n := range []int{0, 1, cs - 1, cs, cs + 1, 2 * cs, 5*cs + 3} {
				t.Run(fmt.Sprintf("%s/chunk=%d/size=%d", kname, cs, n), func(t *testing.T) {
					plain := randomBytes(t, n)
					archive := sealBound(t, plain, keyHex, key, testCreated, c)
					if want := backupHeaderFixedV2 + len(key); len(archive) < want || !bytes.Equal(archive[offKey:want], []byte(key)) {
						t.Fatalf("header does not hold the key at offset %d", offKey)
					}
					r, info, err := newOpenReader(bytes.NewReader(archive), keyHex)
					if err != nil {
						t.Fatal(err)
					}
					got, err := io.ReadAll(r)
					if err != nil || !bytes.Equal(got, plain) {
						t.Fatalf("round trip: %d of %d bytes, err %v", len(got), n, err)
					}
					if info.key != key {
						t.Errorf("info key %q", info.key)
					}
					// The spec reader agrees with the writer.
					if got, err := specOpenV2(t, archive, keyHex); err != nil || !bytes.Equal(got, plain) {
						t.Fatalf("spec reader: %v", err)
					}
				})
			}
		}
		t.Run(kname+"/openArchive", func(t *testing.T) {
			dump := randomBytes(t, 3*backupChunkSize+11)
			obj := sealBound(t, gzipBytes(t, dump), keyHex, key, testCreated, 0)
			got, _, err := openArchiveAll(obj, keyHex, key, false)
			if err != nil || !bytes.Equal(got, dump) {
				t.Fatalf("openArchive: %v", err)
			}
		})
	}
}

func TestStreamBackupBoundRoundTrip(t *testing.T) {
	keyHex := randomKey(t)
	const c = backupChunkSize
	for _, key := range []string{"k", testObjectKey, strings.Repeat("b", maxObjectKeyLen)} {
		for _, n := range []int{1, c - 1, c, c + 1, 2 * c, 5*c + 3} {
			t.Run(fmt.Sprintf("keylen=%d/size=%d", len(key), n), func(t *testing.T) {
				dump := randomBytes(t, n)
				store := &memStore{}
				if _, err := streamBackup(context.Background(), store, key, bytes.NewReader(dump), func() error { return nil }, keyHex); err != nil {
					t.Fatal(err)
				}
				got, info, err := openArchiveAll(store.body, keyHex, key, false)
				if err != nil || !bytes.Equal(got, dump) {
					t.Fatalf("openArchive: %d of %d bytes, err %v", len(got), n, err)
				}
				if info.key != key {
					t.Errorf("bound to %q", info.key)
				}
				if v, _, err := verifyAt(store.body, key, keyHex, false); err != nil || v != int64(n) {
					t.Fatalf("verify: n=%d err=%v", v, err)
				}
				if _, _, err := openArchiveAll(store.body, keyHex, key+"x", true); err == nil {
					t.Fatal("restored under another key")
				}
			})
		}
	}
}

func TestSealWriterKeyLimits(t *testing.T) {
	keyHex := randomKey(t)
	for _, c := range []struct {
		name string
		key  string
		ok   bool
	}{
		{"empty", "", false},
		{"1025 bytes", strings.Repeat("k", maxObjectKeyLen+1), false},
		{"64 KiB", strings.Repeat("k", 64<<10), false},
		{"1 byte", "k", true},
		{"1024 bytes", strings.Repeat("k", maxObjectKeyLen), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, mk := range []func() (io.WriteCloser, error){
				func() (io.WriteCloser, error) { return newSealWriter(io.Discard, keyHex, c.key, testCreated) },
				func() (io.WriteCloser, error) {
					return newSealWriterSize(io.Discard, keyHex, c.key, testCreated, 16)
				},
			} {
				w, err := mk()
				if c.ok && err != nil {
					t.Fatalf("rejected a %d-byte key: %v", len(c.key), err)
				}
				if !c.ok && err == nil {
					_ = w.Close()
					t.Fatalf("accepted a %d-byte key", len(c.key))
				}
				if w != nil && c.ok {
					if err := w.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
	// streamBackup can't upload under an empty key either.
	if _, err := streamBackup(context.Background(), &memStore{}, "", strings.NewReader("PGDMP"), func() error { return nil }, keyHex); err == nil {
		t.Error("streamBackup accepted an empty object key")
	}
	// openArchive needs a key to compare with.
	obj := sealBound(t, gzipBytes(t, []byte("PGDMP")), keyHex, testObjectKey, testCreated, 0)
	v1 := specSealV1(t, gzipBytes(t, []byte("PGDMP")), keyHex, 16)
	for _, object := range [][]byte{obj, v1} {
		for _, allow := range []bool{false, true} {
			if _, _, err := openArchiveAll(object, keyHex, "", allow); err == nil {
				t.Errorf("openArchive accepted an empty object key (allowUnbound=%v)", allow)
			}
		}
	}
}

// Properly encrypted archives whose header holds a key the writer would refuse
// are refused by the reader.
func TestReaderRejectsKeyLengthOutOfRange(t *testing.T) {
	keyHex := randomKey(t)
	gz := gzipBytes(t, randomBytes(t, 200))
	created := testCreated.Unix()
	for _, n := range []int{1, maxObjectKeyLen} {
		key := bytes.Repeat([]byte("k"), n)
		archive := specSealV2(t, gz, keyHex, key, created, 16)
		if got, err := openAll(archive, keyHex); err != nil || !bytes.Equal(got, gz) {
			t.Fatalf("control: spec archive with a %d-byte key does not open: %v", n, err)
		}
		if _, _, err := openArchiveAll(archive, keyHex, string(key), false); err != nil {
			t.Fatalf("control: spec archive with a %d-byte key does not restore: %v", n, err)
		}
	}
	for _, n := range []int{0, maxObjectKeyLen + 1, 2000} {
		t.Run(fmt.Sprintf("key length %d", n), func(t *testing.T) {
			key := bytes.Repeat([]byte("k"), n)
			archive := specSealV2(t, gz, keyHex, key, created, 16)
			if got, err := openAll(archive, keyHex); err == nil {
				t.Errorf("newOpenReader accepted it (%d bytes)", len(got))
			}
			readAs := string(key)
			if n == 0 {
				readAs = "k"
			}
			mustReject(t, "openArchive", archive, keyHex, readAs)
		})
	}
	// A length field far beyond the data that follows.
	salt := randomBytes(t, backupSaltSize)
	short := specHeaderV2(16, salt, created, 0xffff, []byte("abc"))
	if _, err := openAll(short, keyHex); err == nil {
		t.Error("header claiming 65535 key bytes with 3 present accepted")
	}
}

// The production writer and reader follow the documented format exactly: the
// spec reader opens what the writer makes, the reader opens what the spec
// writer makes, and a spec archive that leaves the header out of the HKDF
// info or the AAD (or uses v1's info) is refused.
func TestFormatMatchesSpec(t *testing.T) {
	if backupHeaderSizeV1 != 44 || backupHeaderFixedV2 != offKey || offKey != 54 || maxObjectKeyLen != 1024 {
		t.Fatalf("header constants: v1 %d, v2 fixed %d, max key %d", backupHeaderSizeV1, backupHeaderFixedV2, maxObjectKeyLen)
	}
	if backupMagic != backupMagicV2 {
		t.Fatalf("new backups use %q", backupMagic)
	}
	keyHex := randomKey(t)
	created := time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC)
	for _, chunk := range []int{16, backupChunkSize} {
		plain := randomBytes(t, 3*chunk+7)
		t.Run(fmt.Sprintf("chunk=%d", chunk), func(t *testing.T) {
			archive := sealBound(t, plain, keyHex, testObjectKey, created, chunk)
			if got, err := specOpenV2(t, archive, keyHex); err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("spec reader on the writer's archive: %v", err)
			}
			if c := int64(binary.BigEndian.Uint64(archive[offCreated:])); c != created.Unix() {
				t.Errorf("created field %d, want %d", c, created.Unix())
			}
			if l := binary.BigEndian.Uint16(archive[offKeyLen:]); int(l) != len(testObjectKey) {
				t.Errorf("key length field %d", l)
			}

			spec := specSealV2(t, plain, keyHex, []byte(testObjectKey), created.Unix(), chunk)
			r, info, err := newOpenReader(bytes.NewReader(spec), keyHex)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("reader on a spec archive: %v", err)
			}
			if info.format != backupMagicV2 || info.key != testObjectKey || !info.created.Equal(created) {
				t.Errorf("info %+v", info)
			}

			v1 := specSealV1(t, plain, keyHex, chunk)
			r, info, err = newOpenReader(bytes.NewReader(v1), keyHex)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("reader on a spec v1 archive: %v", err)
			}
			if info.format != backupMagicV1 || info.bound() {
				t.Errorf("v1 info %+v", info)
			}

			salt := randomBytes(t, backupSaltSize)
			header := specHeaderV2(uint32(chunk), salt, created.Unix(), uint16(len(testObjectKey)), []byte(testObjectKey))
			wrong := map[string]struct{ info, aad []byte }{
				"info without header": {[]byte(backupKDFInfoV2), header},
				"v1 info":             {[]byte(backupKDFInfoV1), header},
				"aad without key":     {specV2Info(header), header[:offKey]},
				"aad of v1 length":    {specV2Info(header), header[:backupHeaderSizeV1]},
				"no aad":              {specV2Info(header), nil},
			}
			for name, w := range wrong {
				aead := gcmFor(t, specSubkey(t, keyHex, salt, w.info))
				bad := specSealChunks(aead, header, w.aad, chunk, plain)
				if got, err := openAll(bad, keyHex); err == nil || len(got) > 0 {
					t.Errorf("%s: reader accepted it (%d bytes, err %v)", name, len(got), err)
				}
			}
		})
	}
}

// --- 7. v2-specific tamper cases ------------------------------------------------

func TestV2SpliceAcrossKeyNames(t *testing.T) {
	keyHex := randomKey(t)
	const chunk = 16
	k1 := testObjectKey
	k2 := strings.Replace(k1, "20261029T110000Z", "20261030T110000Z", 1) // same length
	k3 := "mgmt-backups/other.dump.gz.enc"                               // different length
	dump := randomBytes(t, 400)
	mk := func(key string) []byte {
		return sealBound(t, gzipBytes(t, dump), keyHex, key, testCreated, chunk)
	}
	a, b, c := mk(k1), mk(k2), mk(k3)
	ha, hb, hc := offKey+len(k1), offKey+len(k2), offKey+len(k3)
	rec := chunk + gcmTagSize
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	full := func(x []byte, h int) int { return (len(x) - h - 1) / rec } // full chunks before the final one

	type tc struct {
		name   string
		object []byte
		readAs string
	}
	cases := []tc{
		{"k2 header on k1 body", cat(b[:hb], a[ha:]), k2},
		{"k1 header on k2 body", cat(a[:ha], b[hb:]), k1},
		{"k3 header on k1 body", cat(c[:hc], a[ha:]), k3},
		{"k1 header on k3 body", cat(a[:ha], c[hc:]), k1},
	}
	for i := 0; i < min(full(a, ha), full(b, hb), full(c, hc)); i++ {
		at := func(x []byte, h int) []byte { return x[h+i*rec : h+(i+1)*rec] }
		cases = append(cases,
			tc{fmt.Sprintf("chunk %d from the k2 archive", i), cat(a[:ha+i*rec], at(b, hb), a[ha+(i+1)*rec:]), k1},
			tc{fmt.Sprintf("chunk %d from the k3 archive", i), cat(a[:ha+i*rec], at(c, hc), a[ha+(i+1)*rec:]), k1},
		)
	}
	// Final chunks swapped.
	cases = append(cases, tc{"final chunk from the k2 archive",
		cat(a[:ha+full(a, ha)*rec], b[hb+full(b, hb)*rec:]), k1})

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, err := openAll(c.object, keyHex); err == nil {
				t.Errorf("newOpenReader accepted it (%d bytes)", len(got))
			}
			mustFail(t, c.name, c.object, keyHex, c.readAs)
		})
	}
}

// Every header offset, through the restore and verify paths with and without
// the flag: truncation inside the header and a flipped byte.
func TestV2HeaderTruncationAndFlips(t *testing.T) {
	keyHex := randomKey(t)
	dump := randomBytes(t, 300)
	obj := sealBound(t, gzipBytes(t, dump), keyHex, testObjectKey, testCreated, 16)
	h := offKey + len(testObjectKey)
	for n := 0; n <= h; n++ {
		mustReject(t, fmt.Sprintf("truncated to %d", n), obj[:n], keyHex, testObjectKey)
	}
	for i := 0; i < h; i++ {
		for _, bit := range []byte{0x01, 0x80} {
			b := bytes.Clone(obj)
			b[i] ^= bit
			mustReject(t, fmt.Sprintf("byte %d ^ %#x", i, bit), b, keyHex, testObjectKey)
			if i >= offKey {
				// Read as the key the edited header now names.
				mustReject(t, fmt.Sprintf("byte %d ^ %#x as its own key", i, bit), b, keyHex, string(b[offKey:h]))
			}
		}
	}
}

// An object shorter than a magic is a damaged backup, not an unbound one: the
// error must not send the operator to --allow-unbound (review of #549).
func TestShortObjectNotCalledUnbound(t *testing.T) {
	keyHex := randomKey(t)
	obj := sealBound(t, gzipBytes(t, randomBytes(t, 100)), keyHex, testObjectKey, testCreated, 16)
	for n := 0; n < len(backupMagic); n++ {
		for _, allow := range []bool{false, true} {
			_, _, err := openArchiveAll(obj[:n], keyHex, testObjectKey, allow)
			if err == nil {
				t.Fatalf("%d bytes, allowUnbound=%v: opened", n, allow)
			}
			if strings.Contains(err.Error(), "--allow-unbound") {
				t.Errorf("%d bytes, allowUnbound=%v: error points at --allow-unbound: %v", n, allow, err)
			}
			if _, _, err := verifyAt(obj[:n], testObjectKey, keyHex, allow); err == nil || strings.Contains(err.Error(), "--allow-unbound") {
				t.Errorf("%d bytes, allowUnbound=%v: verify error %v", n, allow, err)
			}
		}
	}
}

// --- 8. Subkey separation -------------------------------------------------------

func TestSubkeySeparationV2(t *testing.T) {
	keyHex := randomKey(t)
	salt := randomBytes(t, backupSaltSize)
	created := testCreated.Unix()
	hdr := func(key string, created int64) []byte {
		return specHeaderV2(backupChunkSize, salt, created, uint16(len(key)), []byte(key))
	}
	derive := func(info []byte) string {
		k, err := deriveBackupKey(keyHex, salt, info)
		if err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(k)
	}
	same := map[string]string{
		"key A":              derive(specV2Info(hdr(testObjectKey, created))),
		"key B, same length": derive(specV2Info(hdr(strings.Replace(testObjectKey, "1029", "1030", 1), created))),
		"key C, other len":   derive(specV2Info(hdr("k", created))),
		"key A, created +1":  derive(specV2Info(hdr(testObjectKey, created+1))),
		"v1 info":            derive([]byte(backupKDFInfoV1)),
		"bare v2 info":       derive([]byte(backupKDFInfoV2)),
	}
	seen := map[string]string{}
	for name, k := range same {
		if prev, dup := seen[k]; dup {
			t.Errorf("%s and %s share a subkey under the same salt", name, prev)
		}
		seen[k] = name
	}

	// Two archives with the same key name, time and plaintext.
	plain := bytes.Repeat([]byte("same dump "), 500)
	a := sealBound(t, plain, keyHex, testObjectKey, testCreated, 0)
	b := sealBound(t, plain, keyHex, testObjectKey, testCreated, 0)
	h := offKey + len(testObjectKey)
	if bytes.Equal(a[offSalt:offCreated], b[offSalt:offCreated]) {
		t.Fatal("same salt in two archives")
	}
	ka, err := deriveBackupKey(keyHex, a[offSalt:offCreated], specV2Info(a[:h]))
	if err != nil {
		t.Fatal(err)
	}
	kb, err := deriveBackupKey(keyHex, b[offSalt:offCreated], specV2Info(b[:h]))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ka, kb) {
		t.Fatal("same subkey in two archives")
	}
	if bytes.Equal(a[h:], b[h:]) {
		t.Fatal("same ciphertext in two archives")
	}
}

// --- 9. No secrets in errors ----------------------------------------------------

func TestBindingErrorsCarryNoSecrets(t *testing.T) {
	keyHex := randomKey(t)
	wrongHex := randomKey(t)
	dump := randomBytes(t, 3000)
	gz := gzipBytes(t, dump)
	obj := sealBound(t, gz, keyHex, testObjectKey, testCreated, 16)
	h := offKey + len(testObjectKey)
	v1 := specSealV1(t, gz, keyHex, 16)
	legacy, err := crypto.EncryptBytes(gz, keyHex)
	if err != nil {
		t.Fatal(err)
	}
	flip := func(b []byte, i int) []byte { c := bytes.Clone(b); c[i] ^= 1; return c }

	sub2 := specSubkey(t, keyHex, obj[offSalt:offCreated], specV2Info(obj[:h]))
	sub1 := specSubkey(t, keyHex, v1[offSalt:backupHeaderSizeV1], []byte(backupKDFInfoV1))
	var secrets []string
	for _, s := range []string{keyHex, wrongHex, hex.EncodeToString(sub2), hex.EncodeToString(sub1)} {
		secrets = append(secrets, strings.ToLower(s), strings.ToUpper(s))
	}

	var msgs []string
	collect := func(object []byte, key, objectKey string, allow bool) {
		if _, _, err := openArchiveAll(object, key, objectKey, allow); err != nil {
			msgs = append(msgs, err.Error())
		}
		if _, _, err := verifyAt(object, objectKey, key, allow); err != nil {
			msgs = append(msgs, err.Error())
		}
		if _, err := openAll(object, key); err != nil {
			msgs = append(msgs, err.Error())
		}
	}
	for _, allow := range []bool{false, true} {
		for _, target := range nearMissKeys() {
			collect(obj, keyHex, target, allow)
		}
		collect(obj, wrongHex, testObjectKey, allow)
		collect(flip(obj, h+3), keyHex, testObjectKey, allow)
		collect(flip(obj, offSalt), keyHex, testObjectKey, allow)
		collect(flip(obj, offCreated+7), keyHex, testObjectKey, allow)
		collect(flip(obj, len(obj)-1), keyHex, testObjectKey, allow)
		collect(rewriteKey(obj, len(testObjectKey), "x", 1), keyHex, "x", allow)
		collect(obj[:h+5], keyHex, testObjectKey, allow)
		collect(obj, keyHex, "", allow)
		collect(append([]byte(backupMagicPrefix+"3"), obj[8:]...), keyHex, testObjectKey, allow)
		collect(append([]byte(backupMagicV1), obj[8:]...), keyHex, testObjectKey, allow)
		collect(v1, keyHex, testObjectKey, allow)
		collect(v1, wrongHex, testObjectKey, allow)
		collect(flip(v1, len(v1)/2), keyHex, testObjectKey, allow)
		collect(legacy, keyHex, testObjectKey, allow)
		collect(legacy, wrongHex, testObjectKey, allow)
		collect(flip(legacy, len(legacy)/2), keyHex, testObjectKey, allow)
	}
	_, info, err := openArchiveAll(v1, keyHex, testObjectKey, true)
	if err != nil {
		t.Fatal(err)
	}
	msgs = append(msgs, unboundWarning(testObjectKey, info), describeArchive(info))
	_, info, err = openArchiveAll(obj, keyHex, testObjectKey, false)
	if err != nil {
		t.Fatal(err)
	}
	msgs = append(msgs, describeArchive(info))
	if len(msgs) < 50 {
		t.Fatalf("only %d messages collected", len(msgs))
	}

	for _, m := range msgs {
		for _, s := range secrets {
			// Any 16 hex characters of a secret count as a leak.
			for i := 0; i+16 <= len(s); i += 8 {
				if strings.Contains(m, s[i:i+16]) {
					t.Fatalf("message %q contains key material", m)
				}
			}
		}
	}
}
