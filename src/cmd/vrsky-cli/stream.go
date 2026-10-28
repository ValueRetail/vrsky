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

	"golang.org/x/crypto/hkdf"
)

// Backup archives are encrypted in chunks so neither backup nor restore has to
// hold a whole dump in memory (#430). The layout follows the STREAM
// construction, with a per-archive key as in age:
//
//	header  = magic (8) || plain chunk size, uint32 big-endian (4) || salt (32, random)
//	key     = HKDF-SHA256(ENCRYPTION_KEY, salt, info = "vrsky-backup-v1"), 32 bytes
//	chunk_i = AES-256-GCM(key, nonce = 0^7 || uint32 BE i || last, plaintext_i, aad = header)
//	archive = header || chunk_0 || ... || chunk_n
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
// as additional data stops it, the salt included, being edited.
//
// Archives made before #430 are a single GCM seal, nonce || ciphertext, under
// ENCRYPTION_KEY over the whole gzipped dump (crypto.EncryptBytes). Restore
// tells them apart by the magic; a random 12-byte nonce starts with it with
// probability 2^-64.
const (
	backupMagic       = "VRSKYBK1"
	backupKDFInfo     = "vrsky-backup-v1"
	backupChunkSize   = 64 << 10
	maxBackupChunk    = 16 << 20 // refuse larger chunk sizes before allocating
	backupSaltSize    = 32
	backupHeaderSize  = len(backupMagic) + 4 + backupSaltSize
	gcmNonceSize      = 12
	gcmTagSize        = 16
	lastChunkFlagByte = 1
)

var errBackupTruncated = errors.New("backup archive is truncated: final chunk missing")

// deriveBackupKey returns the archive key for salt: HKDF-SHA256 over the
// 64-hex-char ENCRYPTION_KEY (decoded as pkg/crypto does).
func deriveBackupKey(keyHex string, salt []byte) ([]byte, error) {
	master, err := hex.DecodeString(keyHex)
	if err != nil || len(master) != 32 {
		return nil, errors.New("ENCRYPTION_KEY must be 64 hex characters (32 bytes)")
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, master, salt, []byte(backupKDFInfo)), key); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}
	return key, nil
}

// newBackupAEAD builds AES-256-GCM under the archive key for salt.
func newBackupAEAD(keyHex string, salt []byte) (cipher.AEAD, error) {
	key, err := deriveBackupKey(keyHex, salt)
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

// newSealWriter returns a WriteCloser that encrypts into w with 64 KiB chunks.
// Close writes the final chunk; it does not close w. Without Close the archive
// has no final chunk and will not restore.
func newSealWriter(w io.Writer, keyHex string) (io.WriteCloser, error) {
	return newSealWriterSize(w, keyHex, backupChunkSize)
}

// newSealWriterSize is newSealWriter with an explicit plain chunk size, for
// tests that want many chunk boundaries from little data.
func newSealWriterSize(w io.Writer, keyHex string, chunkSize int) (io.WriteCloser, error) {
	if chunkSize <= 0 || chunkSize > maxBackupChunk {
		return nil, fmt.Errorf("chunk size %d out of range (1..%d)", chunkSize, maxBackupChunk)
	}
	salt := make([]byte, backupSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("rand.Read: %w", err)
	}
	aead, err := newBackupAEAD(keyHex, salt)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 0, backupHeaderSize)
	header = append(header, backupMagic...)
	header = binary.BigEndian.AppendUint32(header, uint32(chunkSize))
	header = append(header, salt...)
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

// newOpenReader reads the header from r and returns a Reader of the plaintext.
// r must be positioned at the start of a chunked archive (the magic included).
func newOpenReader(r io.Reader, keyHex string) (io.Reader, error) {
	// Reject a bad key before reading anything, like the writer does.
	if _, err := deriveBackupKey(keyHex, nil); err != nil {
		return nil, err
	}
	header := make([]byte, backupHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("read backup header: %w", err)
	}
	if string(header[:len(backupMagic)]) != backupMagic {
		return nil, errors.New("not a chunked backup archive")
	}
	chunkSize := binary.BigEndian.Uint32(header[len(backupMagic):])
	if chunkSize == 0 || chunkSize > maxBackupChunk {
		return nil, fmt.Errorf("backup header: chunk size %d out of range", chunkSize)
	}
	aead, err := newBackupAEAD(keyHex, header[len(backupMagic)+4:])
	if err != nil {
		return nil, err
	}
	enc := int(chunkSize) + gcmTagSize
	return &openReader{
		r:      bufio.NewReaderSize(r, 4096),
		aead:   aead,
		header: header,
		nonce:  make([]byte, 0, gcmNonceSize),
		in:     make([]byte, enc),
		pbuf:   make([]byte, 0, chunkSize),
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
