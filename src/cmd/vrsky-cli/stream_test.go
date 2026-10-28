package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/ValueRetail/vrsky/pkg/crypto"
)

// Tests for the streaming backup (#430), written against the acceptance
// criteria in plans/issue-430-backup-stream.md and the API contract, not the
// implementation.

func randomKey(t testing.TB) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(k)
}

func randomBytes(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func sum(b []byte) [32]byte { return sha256.Sum256(b) }

// seal encrypts plain with the given chunk size (0 means the default writer).
func seal(t testing.TB, plain []byte, keyHex string, chunk int) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.WriteCloser
	var err error
	if chunk == 0 {
		w, err = newSealWriter(&buf, keyHex)
	} else {
		w, err = newSealWriterSize(&buf, keyHex, chunk)
	}
	if err != nil {
		t.Fatalf("new seal writer: %v", err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatalf("seal write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("seal close: %v", err)
	}
	return buf.Bytes()
}

// openAll reads a chunked archive to EOF through newOpenReader. Construction
// errors and read errors are both returned.
func openAll(archive []byte, keyHex string) ([]byte, error) {
	r, err := newOpenReader(bytes.NewReader(archive), keyHex)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// restoreAll reads a whole backup object (either format) through openArchive.
func restoreAll(object []byte, keyHex string) ([]byte, error) {
	r, err := openArchive(bytes.NewReader(object), keyHex)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func gzipBytes(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// memStore is a fake streamPutter that keeps the body in memory.
type memStore struct {
	mu          sync.Mutex
	calls       int
	key         string
	contentType string
	body        []byte
	readErr     error // error that ended the body read (nil on clean EOF)
}

func (m *memStore) PutStream(_ context.Context, key string, body io.Reader, contentType string) error {
	b, err := io.ReadAll(body)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.key, m.contentType, m.body, m.readErr = key, contentType, b, err
	return err
}

// --- 2. Round trip -----------------------------------------------------------

func TestSealOpenRoundTrip(t *testing.T) {
	key := randomKey(t)
	type tc struct {
		chunk, size int
	}
	var cases []tc
	for _, c := range []int{1, 2, 7, 16} {
		for _, n := range []int{0, 1, c - 1, c, c + 1, 3 * c, 3*c + 1} {
			cases = append(cases, tc{c, n})
		}
	}
	const def = 64 << 10
	for _, n := range []int{0, 1, def - 1, def, def + 1, 3 * def, 3*def + 5} {
		cases = append(cases, tc{0, n})
	}
	for _, c := range cases {
		name := fmt.Sprintf("chunk=%d/size=%d", c.chunk, c.size)
		if c.chunk == 0 {
			name = fmt.Sprintf("default/size=%d", c.size)
		}
		t.Run(name, func(t *testing.T) {
			plain := randomBytes(t, c.size)
			archive := seal(t, plain, key, c.chunk)
			if !bytes.HasPrefix(archive, []byte(backupMagic)) {
				t.Fatalf("archive does not start with %q", backupMagic)
			}
			got, err := openAll(archive, key)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if sum(got) != sum(plain) {
				t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(plain))
			}
		})
	}
}

// Writes of odd sizes and one-byte reads on both sides must not change the
// result, and the open reader must behave like a well-formed io.Reader.
func TestSealOpenOddIO(t *testing.T) {
	key := randomKey(t)
	plain := randomBytes(t, 1000)
	for _, chunk := range []int{1, 13, 64, 4096} {
		t.Run(fmt.Sprintf("chunk=%d", chunk), func(t *testing.T) {
			var buf bytes.Buffer
			w, err := newSealWriterSize(&buf, key, chunk)
			if err != nil {
				t.Fatal(err)
			}
			for off, step := 0, 1; off < len(plain); step = step%37 + 3 {
				end := min(off+step, len(plain))
				n, err := w.Write(plain[off:end])
				if err != nil || n != end-off {
					t.Fatalf("write [%d:%d]: n=%d err=%v", off, end, n, err)
				}
				off = end
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			r, err := newOpenReader(iotest.OneByteReader(bytes.NewReader(buf.Bytes())), key)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(iotest.OneByteReader(r))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatal("round trip mismatch with odd-sized I/O")
			}
			r2, err := newOpenReader(bytes.NewReader(buf.Bytes()), key)
			if err != nil {
				t.Fatal(err)
			}
			if err := iotest.TestReader(r2, plain); err != nil {
				t.Fatalf("iotest.TestReader: %v", err)
			}
		})
	}
}

// Close must not close the underlying writer.
func TestSealWriterCloseKeepsUnderlyingOpen(t *testing.T) {
	cw := &closeTracker{}
	w, err := newSealWriter(cw, randomKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if cw.closed {
		t.Fatal("seal writer Close closed the underlying writer")
	}
}

type closeTracker struct {
	bytes.Buffer
	closed bool
}

func (c *closeTracker) Close() error { c.closed = true; return nil }

// Same key, same data: the random salt must make archives differ, or GCM
// nonces would repeat under one key across backups.
func TestSealArchivesDiffer(t *testing.T) {
	key := randomKey(t)
	plain := bytes.Repeat([]byte("same data "), 100)
	a, b := seal(t, plain, key, 64), seal(t, plain, key, 64)
	if bytes.Equal(a, b) {
		t.Fatal("two archives of the same data under the same key are identical")
	}
}

func TestBadKeys(t *testing.T) {
	for _, k := range []string{"", "zz", strings.Repeat("a", 63), strings.Repeat("a", 62), strings.Repeat("g", 64), strings.Repeat("a", 32)} {
		t.Run(fmt.Sprintf("len=%d", len(k)), func(t *testing.T) {
			if w, err := newSealWriter(io.Discard, k); err == nil {
				_ = w.Close()
				t.Errorf("newSealWriter accepted key %q", k)
			}
			archive := seal(t, []byte("x"), randomKey(t), 0)
			if _, err := openAll(archive, k); err == nil {
				t.Errorf("newOpenReader accepted key %q", k)
			}
		})
	}
}

func TestStreamBackupRoundTrip(t *testing.T) {
	const kib = 1 << 10
	sizes := []int{1, 2, 64*kib - 1, 64 * kib, 64*kib + 1, 3*64*kib + 7, 1 << 20}
	for _, n := range sizes {
		for _, kind := range []string{"random", "text"} {
			t.Run(fmt.Sprintf("%s/%d", kind, n), func(t *testing.T) {
				var dump []byte
				if kind == "random" {
					dump = randomBytes(t, n)
				} else {
					line := "COPY public.t (id, name) FROM stdin;\n"
					dump = []byte(strings.Repeat(line, n/len(line)+1)[:n])
				}
				key := randomKey(t)
				store := &memStore{}
				waits := 0
				res, err := streamBackup(context.Background(), store, "backups/x.dump.enc", bytes.NewReader(dump),
					func() error { waits++; return nil }, key)
				if err != nil {
					t.Fatalf("streamBackup: %v", err)
				}
				if waits != 1 {
					t.Errorf("wait called %d times, want 1", waits)
				}
				if store.calls != 1 || store.readErr != nil {
					t.Fatalf("store calls=%d readErr=%v", store.calls, store.readErr)
				}
				if store.key != "backups/x.dump.enc" {
					t.Errorf("key = %q", store.key)
				}
				if store.contentType != "application/octet-stream" {
					t.Errorf("content type = %q", store.contentType)
				}
				// 6. raw and sealed byte counts.
				if res.raw != int64(n) {
					t.Errorf("raw = %d, want %d", res.raw, n)
				}
				if res.sealed != int64(len(store.body)) {
					t.Errorf("sealed = %d, store read %d", res.sealed, len(store.body))
				}
				if !bytes.HasPrefix(store.body, []byte(backupMagic)) {
					t.Fatal("stored object is not a chunked archive")
				}
				got, err := restoreAll(store.body, key)
				if err != nil {
					t.Fatalf("openArchive: %v", err)
				}
				if sum(got) != sum(dump) {
					t.Fatalf("restored %d bytes, want %d identical bytes", len(got), n)
				}
				if _, err := restoreAll(store.body, randomKey(t)); err == nil {
					t.Fatal("openArchive with the wrong key succeeded")
				}
			})
		}
	}
}

// --- 3. Legacy archives ------------------------------------------------------

func TestOpenArchiveLegacy(t *testing.T) {
	cases := map[string][]byte{
		"one byte":      {0x42},
		"random 100KiB": randomBytes(t, 100<<10),
		"text 1MiB":     []byte(strings.Repeat("INSERT INTO t VALUES (1);\n", (1<<20)/26)),
	}
	for name, dump := range cases {
		t.Run(name, func(t *testing.T) {
			key := randomKey(t)
			legacy, err := crypto.EncryptBytes(gzipBytes(t, dump), key)
			if err != nil {
				t.Fatal(err)
			}
			got, err := restoreAll(legacy, key)
			if err != nil {
				t.Fatalf("openArchive(legacy): %v", err)
			}
			if sum(got) != sum(dump) {
				t.Fatalf("legacy restore mismatch: got %d bytes, want %d", len(got), len(dump))
			}
			if _, err := restoreAll(legacy, randomKey(t)); err == nil {
				t.Error("legacy archive opened with the wrong key")
			}
			bad := bytes.Clone(legacy)
			bad[len(bad)/2] ^= 1
			if _, err := restoreAll(bad, key); err == nil {
				t.Error("tampered legacy archive opened")
			}
			if _, err := restoreAll(legacy[:len(legacy)-1], key); err == nil {
				t.Error("truncated legacy archive opened")
			}
		})
	}
}

// --- 4. Tamper and truncation ------------------------------------------------

// layout derives the byte layout of an archive empirically: header size and
// the size of a full (non-final) chunk record.
func layout(t *testing.T, key string, chunk int) (header, record int) {
	t.Helper()
	l2 := len(seal(t, make([]byte, 2*chunk+5), key, chunk))
	l3 := len(seal(t, make([]byte, 3*chunk+5), key, chunk))
	l4 := len(seal(t, make([]byte, 4*chunk+5), key, chunk))
	record = l4 - l3
	if l3-l2 != record || record <= chunk {
		t.Fatalf("chunk records are not fixed-size: %d, %d", l3-l2, l4-l3)
	}
	header = backupHeaderSize
	if final := l3 - header - 3*record; final <= 0 || final >= record {
		t.Fatalf("unexpected layout: header %d, record %d, total %d", header, record, l3)
	}
	return header, record
}

func expectOpenError(t *testing.T, name string, archive []byte, key string, plain []byte) {
	t.Helper()
	got, err := openAll(archive, key)
	if err == nil {
		t.Errorf("%s: opened without error (%d of %d bytes, equal=%v)", name, len(got), len(plain), bytes.Equal(got, plain))
	}
}

func TestOpenReaderRejectsTampering(t *testing.T) {
	key := randomKey(t)
	const chunk = 16
	h, rec := layout(t, key, chunk)
	plain := randomBytes(t, 3*chunk+5) // three full chunks and a final one
	archive := seal(t, plain, key, chunk)
	chunkAt := func(i int) []byte { return archive[h+i*rec : h+(i+1)*rec] }

	if got, err := openAll(archive, key); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("baseline archive does not open: %v", err)
	}

	type tc struct {
		name    string
		archive []byte
		key     string
	}
	var cases []tc
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

	// Truncated at every offset: inside the header, at every chunk boundary,
	// inside every chunk, and one byte short of the end.
	for n := 0; n < len(archive); n++ {
		cases = append(cases, tc{fmt.Sprintf("truncated to %d of %d", n, len(archive)), archive[:n], key})
	}
	// A byte flipped at every offset, header included.
	for i := range archive {
		b := bytes.Clone(archive)
		b[i] ^= 0x01
		cases = append(cases, tc{fmt.Sprintf("bit flipped at %d", i), b, key})
	}
	final := archive[h+3*rec:]
	cases = append(cases,
		tc{"chunk 0 dropped", cat(archive[:h], chunkAt(1), chunkAt(2), final), key},
		tc{"chunk 1 dropped", cat(archive[:h], chunkAt(0), chunkAt(2), final), key},
		tc{"chunk 2 dropped", cat(archive[:h], chunkAt(0), chunkAt(1), final), key},
		tc{"final chunk dropped", cat(archive[:h], chunkAt(0), chunkAt(1), chunkAt(2)), key},
		tc{"chunks 0 and 1 swapped", cat(archive[:h], chunkAt(1), chunkAt(0), chunkAt(2), final), key},
		tc{"chunks 1 and 2 swapped", cat(archive[:h], chunkAt(0), chunkAt(2), chunkAt(1), final), key},
		tc{"chunk 1 duplicated", cat(archive[:h], chunkAt(0), chunkAt(1), chunkAt(1), chunkAt(2), final), key},
		tc{"one trailing byte", cat(archive, []byte{0}), key},
		tc{"trailing garbage", cat(archive, randomBytes(t, 100)), key},
		tc{"archive appended twice", cat(archive, archive), key},
		tc{"trailing copy of final chunk", cat(archive, final), key},
		tc{"wrong key", archive, randomKey(t)},
		tc{"empty input", nil, key},
		tc{"header only", archive[:h], key},
		tc{"magic only", []byte(backupMagic), key},
	)
	// A chunk spliced in from another archive under the same key.
	other := seal(t, randomBytes(t, len(plain)), key, chunk)
	cases = append(cases, tc{"chunk spliced from another archive",
		cat(archive[:h], chunkAt(0), other[h+rec:h+2*rec], chunkAt(2), final), key})
	// Header of another archive (different salt) on this body.
	cases = append(cases, tc{"header from another archive", cat(other[:h], archive[h:]), key})

	for _, c := range cases {
		expectOpenError(t, c.name, c.archive, c.key, plain)
	}
}

// The default 64 KiB chunk size gets the same truncation treatment at its
// chunk boundaries.
func TestOpenReaderRejectsTruncationDefaultChunk(t *testing.T) {
	key := randomKey(t)
	const chunk = 64 << 10
	plain := randomBytes(t, 2*chunk+100)
	archive := seal(t, plain, key, 0)
	h := backupHeaderSize
	rec := len(seal(t, make([]byte, 3*chunk+100), key, 0)) - len(archive)
	for _, n := range []int{0, 1, h - 1, h, h + 1, h + rec - 1, h + rec, h + rec + 1, h + 2*rec, len(archive) - 1} {
		expectOpenError(t, fmt.Sprintf("truncated to %d", n), archive[:n], key, plain)
	}
}

// An archive whose writer was never closed lacks its final chunk.
func TestOpenReaderRejectsUnclosedWriter(t *testing.T) {
	key := randomKey(t)
	for _, n := range []int{0, 1, 15, 16, 17, 64, 100} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var buf bytes.Buffer
			w, err := newSealWriterSize(&buf, key, 16)
			if err != nil {
				t.Fatal(err)
			}
			plain := randomBytes(t, n)
			if _, err := w.Write(plain); err != nil {
				t.Fatal(err)
			}
			expectOpenError(t, "unclosed", bytes.Clone(buf.Bytes()), key, plain)
		})
	}
}

// openArchive (the restore path) must reject the same damage on a real
// backup object: gzip inside chunked encryption.
func TestOpenArchiveRejectsTampering(t *testing.T) {
	key := randomKey(t)
	dump := randomBytes(t, 200<<10) // incompressible: several 64 KiB chunks
	store := &memStore{}
	if _, err := streamBackup(context.Background(), store, "k", bytes.NewReader(dump), func() error { return nil }, key); err != nil {
		t.Fatal(err)
	}
	obj := store.body
	h := backupHeaderSize
	rec := 64<<10 + (len(seal(t, make([]byte, 64<<10+1), key, 0)) - len(seal(t, make([]byte, 1), key, 0)) - 64<<10)
	if len(obj) < h+3*rec {
		t.Fatalf("object too small for the test: %d bytes", len(obj))
	}
	chunkAt := func(i int) []byte { return obj[h+i*rec : h+(i+1)*rec] }
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	flip := func(i int) []byte { b := bytes.Clone(obj); b[i] ^= 0x80; return b }

	cases := map[string][]byte{
		"empty object":           nil,
		"header truncated":       obj[:h-1],
		"header only":            obj[:h],
		"truncated at chunk 1":   obj[:h+rec],
		"truncated at chunk 2":   obj[:h+2*rec],
		"truncated at chunk 3":   obj[:h+3*rec],
		"truncated inside chunk": obj[:h+rec+rec/2],
		"last byte cut":          obj[:len(obj)-1],
		"chunk 1 dropped":        cat(obj[:h], chunkAt(0), obj[h+2*rec:]),
		"chunks 0 and 1 swapped": cat(obj[:h], chunkAt(1), chunkAt(0), obj[h+2*rec:]),
		"byte flipped in magic":  flip(0),
		"byte flipped in header": flip(h - 1),
		"byte flipped in chunk":  flip(h + rec + 10),
		"byte flipped at end":    flip(len(obj) - 1),
		"trailing bytes":         cat(obj, []byte("junk")),
	}
	for name, b := range cases {
		got, err := restoreAll(b, key)
		if err == nil {
			t.Errorf("%s: restored without error (%d of %d bytes)", name, len(got), len(dump))
		}
	}
	if _, err := restoreAll(obj, randomKey(t)); err == nil {
		t.Error("wrong key: restored without error")
	}
}

// --- 5. streamBackup failure paths ------------------------------------------

// errAfterReader yields data then fails with err instead of EOF.
type errAfterReader struct {
	r   io.Reader
	err error
}

func (e *errAfterReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		return n, e.err
	}
	return n, err
}

// eofTracker records whether the dump has been read to EOF.
type eofTracker struct {
	r   io.Reader
	eof atomic.Bool
}

func (e *eofTracker) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		e.eof.Store(true)
	}
	return n, err
}

func TestStreamBackupProducerFailures(t *testing.T) {
	waitErr := errors.New("pg_dump: exit status 1")
	readErr := errors.New("read |0: broken pipe")
	cases := []struct {
		name string
		dump func() io.Reader
		wait error
	}{
		{"wait fails after small dump", func() io.Reader { return strings.NewReader("partial dump") }, waitErr},
		{"wait fails after multi-chunk dump", func() io.Reader { return bytes.NewReader(randomBytes(t, 300<<10)) }, waitErr},
		{"wait fails on empty dump", func() io.Reader { return strings.NewReader("") }, waitErr},
		{"empty dump, wait ok", func() io.Reader { return strings.NewReader("") }, nil},
		{"read error at start", func() io.Reader { return &errAfterReader{strings.NewReader(""), readErr} }, nil},
		{"read error mid-way", func() io.Reader {
			return &errAfterReader{bytes.NewReader(randomBytes(t, 300<<10)), readErr}
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := runtime.NumGoroutine()
			dump := &eofTracker{r: c.dump()}
			store := &memStore{}
			waits := 0
			var eofAtWait bool
			_, err := streamBackup(context.Background(), store, "k", dump, func() error {
				waits++
				eofAtWait = dump.eof.Load()
				return c.wait
			}, randomKey(t))
			if err == nil {
				t.Fatal("streamBackup succeeded")
			}
			if store.calls != 1 {
				t.Fatalf("PutStream called %d times", store.calls)
			}
			if store.readErr == nil {
				t.Fatalf("store saw a clean end of stream after %d bytes; want a read error", len(store.body))
			}
			if c.wait != nil {
				if waits != 1 {
					t.Errorf("wait called %d times, want 1", waits)
				}
				if !eofAtWait {
					t.Error("wait called before the dump reached EOF")
				}
			}
			// Whatever the store got must not restore.
			if got, err := restoreAll(store.body, "00"+strings.Repeat("0", 62)); err == nil {
				t.Errorf("partial object restored (%d bytes)", len(got))
			}
			waitGoroutines(t, before)
		})
	}
}

// The partial object must not open even with the right key.
func TestStreamBackupFailedObjectDoesNotOpen(t *testing.T) {
	key := randomKey(t)
	dump := randomBytes(t, 300<<10)
	store := &memStore{}
	_, err := streamBackup(context.Background(), store, "k", bytes.NewReader(dump),
		func() error { return errors.New("exit status 1") }, key)
	if err == nil {
		t.Fatal("streamBackup succeeded")
	}
	if got, err := restoreAll(store.body, key); err == nil {
		t.Fatalf("object from a failed pg_dump restored (%d bytes)", len(got))
	}
}

// endlessDump is an io.ReadCloser that never reaches EOF. In blocking mode it
// returns some data, then blocks until closed.
type endlessDump struct {
	blockAfter int64 // 0: never block
	read       atomic.Int64
	inRead     atomic.Int32
	closeOnce  sync.Once
	closed     chan struct{}
	x          uint64 // PRNG state, only touched by the reading goroutine
}

func newEndlessDump(blockAfter int64) *endlessDump {
	return &endlessDump{blockAfter: blockAfter, closed: make(chan struct{}), x: 0x9e3779b97f4a7c15}
}

func (d *endlessDump) Read(p []byte) (int, error) {
	d.inRead.Add(1)
	defer d.inRead.Add(-1)
	select {
	case <-d.closed:
		return 0, errors.New("read from closed dump")
	default:
	}
	if d.blockAfter > 0 && d.read.Load() >= d.blockAfter {
		<-d.closed
		return 0, errors.New("read from closed dump")
	}
	// Incompressible bytes (xorshift), so gzip passes data on to the store
	// before the dump blocks.
	for i := range p {
		d.x ^= d.x << 13
		d.x ^= d.x >> 7
		d.x ^= d.x << 17
		p[i] = byte(d.x)
	}
	d.read.Add(int64(len(p)))
	return len(p), nil
}

func (d *endlessDump) Close() error {
	d.closeOnce.Do(func() { close(d.closed) })
	return nil
}

func (d *endlessDump) isClosed() bool {
	select {
	case <-d.closed:
		return true
	default:
		return false
	}
}

// failingStore reads up to n bytes of the body, then fails.
type failingStore struct {
	n   int64
	err error
}

func (f *failingStore) PutStream(_ context.Context, _ string, body io.Reader, _ string) error {
	if f.n > 0 {
		if _, err := io.CopyN(io.Discard, body, f.n); err != nil {
			return err
		}
	}
	return f.err
}

func TestStreamBackupStoreFailureStopsDump(t *testing.T) {
	storeErr := errors.New("upload: 503 service unavailable")
	cases := []struct {
		name       string
		readFirst  int64
		blockAfter int64
	}{
		{"store fails at once, endless dump", 0, 0},
		{"store fails after 1 KiB, endless dump", 1 << 10, 0},
		{"store fails after 1 MiB, endless dump", 1 << 20, 0},
		{"store fails at once, dump blocks", 0, 1},
		{"store fails after 1 KiB, dump blocks after 256 KiB", 1 << 10, 256 << 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := runtime.NumGoroutine()
			dump := newEndlessDump(c.blockAfter)
			defer dump.Close() // do not leave a blocked reader behind if the test fails
			store := &failingStore{n: c.readFirst, err: storeErr}
			type out struct {
				err      error
				inRead   int32
				returned bool
			}
			done := make(chan out, 1)
			go func() {
				_, err := streamBackup(context.Background(), store, "k", dump, func() error { return nil }, randomKey(t))
				done <- out{err: err, inRead: dump.inRead.Load(), returned: true}
			}()
			select {
			case o := <-done:
				if o.err == nil {
					t.Fatal("streamBackup succeeded although the store failed")
				}
				if !dump.isClosed() {
					t.Error("dump was not closed after the store failed")
				}
				if o.inRead != 0 {
					t.Error("streamBackup returned while a dump Read was still running")
				}
			case <-time.After(10 * time.Second):
				buf := make([]byte, 1<<16)
				buf = buf[:runtime.Stack(buf, true)]
				t.Fatalf("streamBackup did not return within 10s after the store failed (dump closed: %v)\n%s", dump.isClosed(), buf)
			}
			waitGoroutines(t, before)
		})
	}
}

// waitGoroutines fails if the goroutine count does not settle back to before.
func waitGoroutines(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		now := runtime.NumGoroutine()
		if now <= before {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("goroutine leak: %d before, %d after\n%s", before, now, buf)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- 1. Memory does not scale with the dump size -----------------------------

// patternDump yields size bytes without allocating them: 64 KiB blocks
// alternating between incompressible bytes and repetitive text, cut from two
// fixed 1 MiB buffers. gzip's 32 KiB window cannot find repeats in the random
// buffer, so the gzipped stream stays about half the dump size.
type patternDump struct {
	random, text []byte
	off, size    int64
}

func newPatternDump(t testing.TB, size int64) *patternDump {
	line := []byte("COPY public.messages (id, tenant_id, body) FROM stdin;\n")
	text := bytes.Repeat(line, (1<<20)/len(line)+1)[:1<<20]
	return &patternDump{random: randomBytes(t, 1<<20), text: text, size: size}
}

func (d *patternDump) Read(p []byte) (int, error) {
	if d.off >= d.size {
		return 0, io.EOF
	}
	const block = 64 << 10
	src := d.text
	if (d.off/block)%2 == 0 {
		src = d.random
	}
	start := d.off % int64(len(src))
	n := min(int64(len(p)), block-d.off%block, d.size-d.off, int64(len(src))-start)
	copy(p, src[start:start+n])
	d.off += n
	return int(n), nil
}

// discardStore reads the body in small pieces, hashes and counts it.
type discardStore struct {
	n   int64
	h   [32]byte
	err error
}

func (d *discardStore) PutStream(_ context.Context, _ string, body io.Reader, _ string) error {
	hh := sha256.New()
	buf := make([]byte, 4<<10)
	n, err := io.CopyBuffer(hh, struct{ io.Reader }{body}, buf)
	d.n, d.err = n, err
	copy(d.h[:], hh.Sum(nil))
	return err
}

func TestStreamBackupMemoryIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("streams a large dump; skipped with -short")
	}
	size := int64(256 << 20)
	if raceEnabled() {
		size = 96 << 20 // the race detector slows gzip down a lot
	}
	const limit = 32 << 20 // peak heap growth allowed, far below the dump size
	dump := newPatternDump(t, size)
	key := randomKey(t)
	store := &discardStore{}

	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapInuse

	var peak atomic.Uint64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapInuse > peak.Load() {
				peak.Store(m.HeapInuse)
			}
			select {
			case <-stop:
				return
			case <-tick.C:
			}
		}
	}()
	start := time.Now()
	res, err := streamBackup(context.Background(), store, "k", dump, func() error { return nil }, key)
	close(stop)
	<-sampled
	if err != nil {
		t.Fatalf("streamBackup: %v", err)
	}
	growth := int64(peak.Load()) - int64(base)
	t.Logf("dump %d MiB, sealed %d MiB, peak heap growth %.1f MiB, %s",
		size>>20, res.sealed>>20, float64(growth)/(1<<20), time.Since(start).Round(time.Millisecond))
	if res.raw != size {
		t.Errorf("raw = %d, want %d", res.raw, size)
	}
	if res.sealed != store.n {
		t.Errorf("sealed = %d, store read %d", res.sealed, store.n)
	}
	if store.n < size/3 {
		t.Fatalf("sealed output only %d bytes; the test dump compresses too well to catch buffering", store.n)
	}
	if growth > limit {
		t.Fatalf("peak heap grew by %d MiB for a %d MiB dump; limit %d MiB", growth>>20, size>>20, limit>>20)
	}
}

// raceEnabled reports whether the test binary was built with -race.
func raceEnabled() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}
