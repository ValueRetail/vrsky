package claimcheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"log/slog"

	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// Spool is an io.Writer for transform output whose size is unknown until it has
// been produced (ADR 0002 phase B). It keeps the bytes in memory while they fit
// under the inline threshold — so a large input whose filtered result is small
// still travels inline, UI preview included — and switches to streaming into the
// spill store the moment the threshold is crossed. Memory is bounded by the
// threshold regardless of output size.
type Spool struct {
	ctx         context.Context
	store       objectstore.ObjectStore
	key         string
	contentType string
	threshold   int

	buf  bytes.Buffer
	h    hash.Hash
	size int64

	// set once the threshold is crossed
	spilled bool
	pw      *io.PipeWriter
	putErr  chan error

	closed bool
}

// SpoolResult is what a completed Spool produced: either the inline bytes, or a
// reference into the spill store.
type SpoolResult struct {
	Inline []byte // non-nil when the output stayed at or below the threshold

	Ref      string // set when spilled
	Size     int64
	Checksum string
}

// NewSpool starts spooling output destined for key. A threshold <= 0 spills
// from the first byte (offload effectively disabled for inline travel — the
// output cannot be kept in memory either way on this path).
func NewSpool(ctx context.Context, store objectstore.ObjectStore, key, contentType string, threshold int) *Spool {
	return &Spool{
		ctx:         ctx,
		store:       store,
		key:         key,
		contentType: contentType,
		threshold:   threshold,
		h:           sha256.New(),
	}
}

func (s *Spool) Write(p []byte) (int, error) {
	if s.closed {
		return 0, fmt.Errorf("spool: write after close")
	}
	s.h.Write(p)
	s.size += int64(len(p))

	if !s.spilled && (s.threshold <= 0 || s.size > int64(s.threshold)) {
		if err := s.startSpill(); err != nil {
			return 0, err
		}
	}
	if s.spilled {
		return s.pw.Write(p)
	}
	return s.buf.Write(p)
}

// startSpill opens the pipe into the store and replays the buffered prefix.
func (s *Spool) startSpill() error {
	if s.store == nil {
		return fmt.Errorf("spool: output exceeds the %d-byte inline threshold but no offload store is configured", s.threshold)
	}
	pr, pw := io.Pipe()
	s.putErr = make(chan error, 1)
	go func() {
		err := s.store.PutStream(s.ctx, s.key, pr, s.contentType)
		if err != nil {
			// Unblock the writer; Write/Close surface the error.
			_ = pr.CloseWithError(err)
		}
		s.putErr <- err
	}()
	if s.buf.Len() > 0 {
		if _, err := pw.Write(s.buf.Bytes()); err != nil {
			return fmt.Errorf("spool: replay buffered prefix: %w", err)
		}
		s.buf.Reset()
	}
	s.pw = pw
	s.spilled = true
	return nil
}

// Close finalizes the spool. It must be called exactly once before Result.
func (s *Spool) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if !s.spilled {
		return nil
	}
	if err := s.pw.Close(); err != nil {
		return err
	}
	if err := <-s.putErr; err != nil {
		return fmt.Errorf("spool: store write: %w", err)
	}
	return nil
}

// Abort discards the spool: nothing produced should survive (e.g. a filter that
// dropped every record publishes nothing). Best-effort — a spilled partial
// object that cannot be deleted is reaped by the bucket lifecycle TTL.
func (s *Spool) Abort(logger *slog.Logger) {
	if s.closed {
		return
	}
	s.closed = true
	if !s.spilled {
		return
	}
	_ = s.pw.CloseWithError(fmt.Errorf("spool aborted"))
	<-s.putErr // wait the goroutine out; its error is expected and irrelevant
	if err := s.store.Delete(s.ctx, s.key); err != nil {
		logger.Warn("spool abort: could not delete partial object; lifecycle TTL will reap it", "key", s.key, "error", err)
	}
}

// Result reports what the spool produced. Only valid after a successful Close.
func (s *Spool) Result() SpoolResult {
	if !s.spilled {
		// Copy: the caller owns the result; the spool may be reused/GC'd.
		// make() rather than append(nil, ...) so an EMPTY output is a non-nil
		// empty slice: callers distinguish inline from spilled by Inline != nil,
		// and a nil here would make an empty result look like a spill with no
		// reference — a malformed envelope.
		inline := make([]byte, s.buf.Len())
		copy(inline, s.buf.Bytes())
		return SpoolResult{Inline: inline}
	}
	return SpoolResult{
		Ref:      s.key,
		Size:     s.size,
		Checksum: "sha256:" + hex.EncodeToString(s.h.Sum(nil)),
	}
}
