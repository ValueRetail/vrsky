package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// NewLogger writes to the configured log file, rotating by size, and also to
// stderr when running in a console. extra, if non-nil, receives every record
// too — the Windows Event Log when running as a service.
func NewLogger(cfg *Config, console bool, extra slog.Handler) (*slog.Logger, io.Closer, error) {
	rf, err := openRotating(cfg.Log.File, int64(cfg.Log.MaxSizeMB)<<20, cfg.Log.MaxFiles)
	if err != nil {
		return nil, nil, fmt.Errorf("open log %s: %w", cfg.Log.File, err)
	}
	var w io.Writer = rf
	if console {
		w = io.MultiWriter(rf, os.Stderr)
	}
	var h slog.Handler = slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	if extra != nil {
		h = teeHandler{h, extra}
	}
	return slog.New(h), rf, nil
}

// rotatingFile is an append-only log file that rolls over at a size limit,
// keeping a fixed number of old copies (agent.log.1 … agent.log.N).
type rotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func openRotating(path string, max int64, keep int) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	r := &rotatingFile{path: path, max: max, keep: keep}
	return r, r.open()
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, info.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.max > 0 && r.size+int64(len(p)) > r.max && r.size > 0 {
		if err := r.rotate(); err != nil {
			// Keep logging into the full file rather than lose the line.
			fmt.Fprintf(os.Stderr, "log rotation failed: %v\n", err)
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil {
		_ = r.open()
		return err
	}
	return r.open()
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// teeHandler sends each record to two handlers.
type teeHandler struct{ a, b slog.Handler }

func (t teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return t.a.Enabled(ctx, l) || t.b.Enabled(ctx, l)
}

func (t teeHandler) Handle(ctx context.Context, rec slog.Record) error {
	var err error
	if t.a.Enabled(ctx, rec.Level) {
		err = t.a.Handle(ctx, rec.Clone())
	}
	if t.b.Enabled(ctx, rec.Level) {
		if e := t.b.Handle(ctx, rec.Clone()); err == nil {
			err = e
		}
	}
	return err
}

func (t teeHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return teeHandler{t.a.WithAttrs(as), t.b.WithAttrs(as)}
}

func (t teeHandler) WithGroup(name string) slog.Handler {
	return teeHandler{t.a.WithGroup(name), t.b.WithGroup(name)}
}
