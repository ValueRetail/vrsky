package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"golang.org/x/crypto/hkdf"
)

// Backup archives are encrypted in chunks so neither backup nor restore has to
// hold a whole dump in memory (#430). The layout follows the STREAM
// construction, with a per-archive key as in age. Version 2 (#549) adds the
// archive's identity to the header:
//
//	header  = "VRSKYBK2" (8) || plain chunk size, uint32 BE (4) || salt (32, random)
//	          || created, Unix seconds, int64 BE (8) || key length, uint16 BE (2)
//	          || object key (1..1024 bytes)
//	key     = HKDF-SHA256(ENCRYPTION_KEY, salt, info = "vrsky-backup-v2" || header), 32 bytes
//	chunk_i = AES-256-GCM(key, nonce = 0^7 || uint32 BE i || last, plaintext_i, aad = header)
//	archive = header || chunk_0 || ... || chunk_n
//
// The object key is the one the archive was uploaded under. The header is the
// additional data of every chunk and part of the key derivation, so an archive
// copied to another key, or with any header byte edited, can't pass for the
// backup made under that key: verify and restore compare the header's key with
// the one they downloaded (openArchive), and the first chunk authenticates the
// header.
//
// Version 1 (#430) has a 44-byte header, "VRSKYBK1" || chunk size || salt,
// with info = "vrsky-backup-v1" and that header as the additional data. It
// says nothing about which backup it is, so it is read only when the operator
// allows unbound archives. Nothing writes it any more.
//
// The chunks are never sealed under ENCRYPTION_KEY itself, which pkg/crypto
// uses for the secrets table; the info string keeps the two uses apart and the
// salt gives every archive its own key. Because no key encrypts more than one
// archive, the counter and flag alone keep nonces unique, so there is no random
// nonce prefix.
//
// Every chunk but the last carries exactly chunk-size plaintext bytes; the last
// one has the last byte of its nonce set to 1 and may be shorter (empty only
// when the whole input is). The counter stops chunks being reordered or
// dropped, the last flag stops truncation at a chunk boundary, and the header
// as additional data stops it being edited.
//
// Archives made before #430 are a single GCM seal, nonce || ciphertext, under
// ENCRYPTION_KEY over the whole gzipped dump (crypto.EncryptBytes). Restore
// tells them apart by the magic; a random 12-byte nonce starts with "VRSKYBK"
// with probability 2^-56.
const (
	backupMagicPrefix = "VRSKYBK"
	backupMagicV1     = backupMagicPrefix + "1"
	backupMagicV2     = backupMagicPrefix + "2"
	backupMagic       = backupMagicV2 // what new backups are written as
	backupKDFInfoV1   = "vrsky-backup-v1"
	backupKDFInfoV2   = "vrsky-backup-v2"
	backupChunkSize   = 64 << 10
	maxBackupChunk    = 16 << 20 // refuse larger chunk sizes before allocating
	backupSaltSize    = 32
	// v1: magic || chunk size || salt. v2 starts with the same three fields.
	backupHeaderSizeV1 = len(backupMagicV1) + 4 + backupSaltSize
	// v2 up to the object key: v1's fields || created || key length.
	backupHeaderFixedV2 = backupHeaderSizeV1 + 8 + 2
	maxObjectKeyLen     = 1024 // S3's and Azure's limit on a key or blob name
	gcmNonceSize        = 12
	gcmTagSize          = 16
	lastChunkFlagByte   = 1

	// formatLegacy names a pre-#430 single-seal archive in archiveInfo.
	formatLegacy = "legacy single-seal"
)

var errBackupTruncated = errors.New("backup archive is truncated: final chunk missing")

// archiveInfo is what an archive's header says about it. Only a VRSKYBK2
// header names a key and a creation time, and they are only proven once the
// first chunk has authenticated.
type archiveInfo struct {
	format  string // backupMagicV2, backupMagicV1 or formatLegacy
	key     string
	created time.Time
}

// bound reports whether the archive names the object key it was made for.
func (a archiveInfo) bound() bool { return a.format == backupMagicV2 }

// deriveBackupKey returns an archive key: HKDF-SHA256 over the 64-hex-char
// ENCRYPTION_KEY (decoded as pkg/crypto does) with the archive's salt and info.
func deriveBackupKey(keyHex string, salt, info []byte) ([]byte, error) {
	master, err := hex.DecodeString(keyHex)
	if err != nil || len(master) != 32 {
		return nil, errors.New("ENCRYPTION_KEY must be 64 hex characters (32 bytes)")
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, master, salt, info), key); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}
	return key, nil
}

// backupKDFInfo returns the HKDF info for an archive with this header: the
// version's label, and for v2 the whole header after it.
func backupKDFInfo(header []byte) []byte {
	if string(header[:len(backupMagicV1)]) == backupMagicV1 {
		return []byte(backupKDFInfoV1)
	}
	info := make([]byte, 0, len(backupKDFInfoV2)+len(header))
	info = append(info, backupKDFInfoV2...)
	return append(info, header...)
}

// newBackupAEAD builds AES-256-GCM under the archive key for header, a full
// v1 or v2 header.
func newBackupAEAD(keyHex string, header []byte) (cipher.AEAD, error) {
	salt := header[len(backupMagicV1)+4 : backupHeaderSizeV1]
	key, err := deriveBackupKey(keyHex, salt, backupKDFInfo(header))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// chunkNonce is 7 zero bytes, the chunk counter and the last flag.
func chunkNonce(dst []byte, counter uint32, last bool) []byte {
	dst = append(dst[:0], 0, 0, 0, 0, 0, 0, 0)
	dst = binary.BigEndian.AppendUint32(dst, counter)
	if last {
		return append(dst, lastChunkFlagByte)
	}
	return append(dst, 0)
}

// sealWriter encrypts everything written to it into the chunked format. It
// holds at most one chunk of plaintext and one of ciphertext.
type sealWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	header  []byte
	nonce   []byte
	buf     []byte // pending plaintext, len <= cap == chunk size
	out     []byte
	counter uint32
	err     error
	closed  bool
}

// newSealWriter returns a WriteCloser that encrypts into w as a VRSKYBK2
// archive with 64 KiB chunks, bound to objectKey (the key it will be stored
// under) and created. Close writes the final chunk; it does not close w.
// Without Close the archive has no final chunk and will not restore.
func newSealWriter(w io.Writer, keyHex, objectKey string, created time.Time) (io.WriteCloser, error) {
	return newSealWriterSize(w, keyHex, objectKey, created, backupChunkSize)
}

// newSealWriterSize is newSealWriter with an explicit plain chunk size, for
// tests that want many chunk boundaries from little data.
func newSealWriterSize(w io.Writer, keyHex, objectKey string, created time.Time, chunkSize int) (io.WriteCloser, error) {
	if chunkSize <= 0 || chunkSize > maxBackupChunk {
		return nil, fmt.Errorf("chunk size %d out of range (1..%d)", chunkSize, maxBackupChunk)
	}
	if objectKey == "" || len(objectKey) > maxObjectKeyLen {
		return nil, fmt.Errorf("object key length %d out of range (1..%d)", len(objectKey), maxObjectKeyLen)
	}
	salt := make([]byte, backupSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("rand.Read: %w", err)
	}
	header := make([]byte, 0, backupHeaderFixedV2+len(objectKey))
	header = append(header, backupMagicV2...)
	header = binary.BigEndian.AppendUint32(header, uint32(chunkSize))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint64(header, uint64(created.Unix()))
	header = binary.BigEndian.AppendUint16(header, uint16(len(objectKey)))
	header = append(header, objectKey...)
	aead, err := newBackupAEAD(keyHex, header)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(header); err != nil {
		return nil, err
	}
	return &sealWriter{
		w:      w,
		aead:   aead,
		header: header,
		nonce:  make([]byte, 0, gcmNonceSize),
		buf:    make([]byte, 0, chunkSize),
		out:    make([]byte, 0, chunkSize+gcmTagSize),
	}, nil
}

func (s *sealWriter) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	if s.closed {
		return 0, errors.New("write to closed backup archive")
	}
	n := 0
	for len(p) > 0 {
		// A full buffer is only flushed once more data arrives, so the chunk
		// written on Close is always the true last one.
		if len(s.buf) == cap(s.buf) {
			if err := s.flush(false); err != nil {
				return n, err
			}
		}
		k := copy(s.buf[len(s.buf):cap(s.buf)], p)
		s.buf = s.buf[:len(s.buf)+k]
		p = p[k:]
		n += k
	}
	return n, nil
}

func (s *sealWriter) flush(last bool) error {
	if !last && s.counter == math.MaxUint32 {
		s.err = errors.New("backup archive too large: chunk counter exhausted")
		return s.err
	}
	s.nonce = chunkNonce(s.nonce, s.counter, last)
	s.out = s.aead.Seal(s.out[:0], s.nonce, s.buf, s.header)
	if _, err := s.w.Write(s.out); err != nil {
		s.err = err
		return err
	}
	s.buf = s.buf[:0]
	s.counter++
	return nil
}

// Close seals the buffered plaintext as the final chunk.
func (s *sealWriter) Close() error {
	if s.err != nil {
		return s.err
	}
	if s.closed {
		return nil
	}
	s.closed = true
	return s.flush(true)
}

// openReader decrypts a chunked archive. It releases a chunk's plaintext only
// after that chunk authenticated, and reports io.EOF only after a valid final
// chunk followed by the end of the input.
type openReader struct {
	r       *bufio.Reader
	aead    cipher.AEAD
	header  []byte
	nonce   []byte
	in      []byte // one encrypted chunk
	plain   []byte // unread plaintext of the current chunk
	pbuf    []byte
	counter uint32
	done    bool
	err     error
}

// newOpenReader reads a VRSKYBK1 or VRSKYBK2 header from r and returns a
// Reader of the plaintext and what the header says. r must be positioned at
// the start of a chunked archive (the magic included). It does not decide
// whether the archive may be used for a given key; openArchive does that.
// The header is only proven once the first chunk has been read.
func newOpenReader(r io.Reader, keyHex string) (io.Reader, archiveInfo, error) {
	// Reject a bad key before reading anything, like the writer does.
	if _, err := deriveBackupKey(keyHex, nil, nil); err != nil {
		return nil, archiveInfo{}, err
	}
	header, info, err := readBackupHeader(r)
	if err != nil {
		return nil, archiveInfo{}, err
	}
	chunkSize := binary.BigEndian.Uint32(header[len(backupMagicV1):])
	aead, err := newBackupAEAD(keyHex, header)
	if err != nil {
		return nil, archiveInfo{}, err
	}
	enc := int(chunkSize) + gcmTagSize
	return &openReader{
		r:      bufio.NewReaderSize(r, 4096),
		aead:   aead,
		header: header,
		nonce:  make([]byte, 0, gcmNonceSize),
		in:     make([]byte, enc),
		pbuf:   make([]byte, 0, chunkSize),
	}, info, nil
}

// readBackupHeader reads a whole v1 or v2 header and checks its sizes before
// anything is allocated from them.
func readBackupHeader(r io.Reader) ([]byte, archiveInfo, error) {
	header := make([]byte, backupHeaderSizeV1, backupHeaderFixedV2+maxObjectKeyLen)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, archiveInfo{}, fmt.Errorf("read backup header: %w", err)
	}
	magic := string(header[:len(backupMagicV1)])
	if magic != backupMagicV1 && magic != backupMagicV2 {
		return nil, archiveInfo{}, fmt.Errorf("not a chunked backup archive (magic %q)", magic)
	}
	chunkSize := binary.BigEndian.Uint32(header[len(backupMagicV1):])
	if chunkSize == 0 || chunkSize > maxBackupChunk {
		return nil, archiveInfo{}, fmt.Errorf("backup header: chunk size %d out of range", chunkSize)
	}
	if magic == backupMagicV1 {
		return header, archiveInfo{format: backupMagicV1}, nil
	}

	header = header[:backupHeaderFixedV2]
	if _, err := io.ReadFull(r, header[backupHeaderSizeV1:]); err != nil {
		return nil, archiveInfo{}, fmt.Errorf("read backup header: %w", err)
	}
	created := int64(binary.BigEndian.Uint64(header[backupHeaderSizeV1:]))
	keyLen := int(binary.BigEndian.Uint16(header[backupHeaderSizeV1+8:]))
	if keyLen == 0 || keyLen > maxObjectKeyLen {
		return nil, archiveInfo{}, fmt.Errorf("backup header: object key length %d out of range", keyLen)
	}
	header = header[:backupHeaderFixedV2+keyLen]
	if _, err := io.ReadFull(r, header[backupHeaderFixedV2:]); err != nil {
		return nil, archiveInfo{}, fmt.Errorf("read backup header: %w", err)
	}
	return header, archiveInfo{
		format:  backupMagicV2,
		key:     string(header[backupHeaderFixedV2:]),
		created: time.Unix(created, 0).UTC(),
	}, nil
}

func (o *openReader) Read(p []byte) (int, error) {
	for len(o.plain) == 0 {
		if o.err != nil {
			return 0, o.err
		}
		if o.done {
			return 0, io.EOF
		}
		o.err = o.next()
	}
	n := copy(p, o.plain)
	o.plain = o.plain[n:]
	return n, nil
}

// next reads and authenticates one chunk into o.plain.
func (o *openReader) next() error {
	n, err := io.ReadFull(o.r, o.in)
	last := false
	switch {
	case err == io.EOF:
		return errBackupTruncated
	case err == io.ErrUnexpectedEOF:
		last = true
	case err != nil:
		return fmt.Errorf("read backup chunk %d: %w", o.counter, err)
	default:
		// A full-size chunk is the last one exactly when nothing follows it.
		if _, perr := o.r.Peek(1); perr == io.EOF {
			last = true
		} else if perr != nil {
			return fmt.Errorf("read backup chunk %d: %w", o.counter, perr)
		}
	}
	if !last && o.counter == math.MaxUint32 {
		return errors.New("backup archive has too many chunks")
	}
	o.nonce = chunkNonce(o.nonce, o.counter, last)
	plain, err := o.aead.Open(o.pbuf[:0], o.nonce, o.in[:n], o.header)
	if err != nil {
		if last {
			// Either corrupt, or cut off: a non-final chunk opened as final
			// fails authentication too.
			return fmt.Errorf("backup chunk %d failed authentication (wrong ENCRYPTION_KEY, corrupt or truncated archive): %w", o.counter, err)
		}
		return fmt.Errorf("backup chunk %d failed authentication (wrong ENCRYPTION_KEY or corrupt archive): %w", o.counter, err)
	}
	o.plain = plain
	o.counter++
	o.done = last
	return nil
}
