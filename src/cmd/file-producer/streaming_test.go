package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// newStreamingFileProducer stubs the config lookup so the streaming decisions
// can be exercised without a management DB.
func newStreamingFileProducer(t *testing.T, outDir string, configs []*ConnectionConfig) *fileProducer {
	t.Helper()
	return &fileProducer{
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		defaultOutputDir: outDir,
		allowedRoots:     []string{outDir},
		configCache:      map[string][]*ConnectionConfig{"conn-1": configs},
		configCacheTime:  map[string]time.Time{"conn-1": time.Now()},
		configCacheTTL:   time.Hour,
	}
}

func streamedEnv(size int64) *envelope.Envelope {
	return &envelope.Envelope{
		ID:            "env-1",
		TenantID:      "tenant-x",
		IntegrationID: "conn-1",
		ContentType:   "text/csv",
		PayloadSize:   size,
		Metadata:      map[string]interface{}{"filename": "big.csv"},
	}
}

func TestDeliverStream_WritesFileFromStream(t *testing.T) {
	out := t.TempDir()
	payload := bytes.Repeat([]byte("S"), 8192)
	p := newStreamingFileProducer(t, out, []*ConnectionConfig{{TenantID: "tenant-x", OutputPath: out}})

	if err := p.DeliverStream(context.Background(), streamedEnv(int64(len(payload))), bytes.NewReader(payload)); err != nil {
		t.Fatalf("DeliverStream: %v", err)
	}

	// Output lands in the tenant's own subtree of the root (pkg/tenantpath),
	// not directly in it.
	tenantDir := filepath.Join(out, "tenant-x")
	entries, err := os.ReadDir(tenantDir)
	if err != nil {
		t.Fatalf("read out dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one written file, got %d", len(entries))
	}
	got, err := os.ReadFile(filepath.Join(tenantDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("written %d bytes, want %d", len(got), len(payload))
	}
}

// A stream reads once, so writing to several output configs is declined and the
// SDK falls back to buffered delivery.
func TestDeliverStream_DeclinesMultipleOutputs(t *testing.T) {
	out := t.TempDir()
	p := newStreamingFileProducer(t, out, []*ConnectionConfig{
		{OutputPath: out}, {OutputPath: out, FolderName: "second"},
	})
	err := p.DeliverStream(context.Background(), streamedEnv(1), bytes.NewReader([]byte("x")))
	if !errors.Is(err, sdk.ErrStreamUnsupported) {
		t.Fatalf("expected ErrStreamUnsupported, got %v", err)
	}
}

// An output path outside the mounted roots is a config error: dropped, not
// retried, and nothing written.
func TestDeliverStream_DisallowedPathIsDropped(t *testing.T) {
	out := t.TempDir()
	elsewhere := t.TempDir() // not in allowedRoots
	p := newStreamingFileProducer(t, out, []*ConnectionConfig{{TenantID: "tenant-x", OutputPath: elsewhere}})

	if err := p.DeliverStream(context.Background(), streamedEnv(1), bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("a disallowed path should be dropped, not returned as an error: %v", err)
	}
	entries, _ := os.ReadDir(elsewhere)
	if len(entries) != 0 {
		t.Errorf("nothing should have been written outside the allowed roots, found %d entries", len(entries))
	}
}

// failingReader yields some bytes then fails, standing in for a truncated
// object-store read partway through a large transfer.
type failingReader struct {
	data []byte
	n    int
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n >= len(f.data) {
		return 0, errors.New("simulated mid-stream failure")
	}
	n := copy(p, f.data[f.n:])
	f.n += n
	return n, nil
}

// A failed copy must not leave a truncated file behind: whatever watches the
// output directory would treat it as a complete delivery.
func TestDeliverStream_PartialWriteLeavesNoFile(t *testing.T) {
	out := t.TempDir()
	p := newStreamingFileProducer(t, out, []*ConnectionConfig{{TenantID: "tenant-x", OutputPath: out}})

	err := p.DeliverStream(context.Background(), streamedEnv(9999),
		&failingReader{data: bytes.Repeat([]byte("P"), 512)})
	if err == nil {
		t.Fatal("expected a mid-stream failure to be reported")
	}
	if sdk.IsPermanent(err) {
		t.Error("a mid-stream read failure should stay retriable")
	}
	entries, _ := os.ReadDir(filepath.Join(out, "tenant-x"))
	if len(entries) != 0 {
		t.Errorf("a truncated file was left behind: %d entries", len(entries))
	}
}

func TestDeliverStream_NoEligibleConfigIsNoOp(t *testing.T) {
	out := t.TempDir()
	p := newStreamingFileProducer(t, out, []*ConnectionConfig{
		{OutputPath: out, PredecessorID: "other-node"},
	})
	if err := p.DeliverStream(context.Background(), streamedEnv(1), bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("expected a no-op, got %v", err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("nothing should have been written, found %d entries", len(entries))
	}
}

// isPathAllowed resolved symlinks on the candidate path but not on the allowed
// roots, so any root containing a symlinked component (/var -> /private/var, or
// a mount alias like /data -> /mnt/data) rejected every legitimate write — the
// file was logged and dropped, with no retry and no DLQ.
func TestIsPathAllowed_SymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// The root is configured via the symlink; the write target is under it.
	target := filepath.Join(link, "out.csv")
	if !isPathAllowed(target, []string{link}) {
		t.Error("a path under a symlinked root must be allowed")
	}
	// Configured by symlink, resolved differently — the case that was broken.
	if !isPathAllowed(target, []string{real}) {
		t.Error("a path under a root that resolves to the same directory must be allowed")
	}

	// The escape check must still hold.
	outside := t.TempDir()
	if isPathAllowed(filepath.Join(outside, "x.csv"), []string{link}) {
		t.Error("a path outside every root must be rejected")
	}
	if isPathAllowed(filepath.Join(link, "..", "..", "etc", "passwd"), []string{link}) {
		t.Error("a traversal out of the root must be rejected")
	}
	// A sibling whose name merely shares the root's prefix must not slip through.
	if isPathAllowed(real+"-evil/x.csv", []string{real}) {
		t.Error("a sibling directory with a shared name prefix must be rejected")
	}
}

// TestResolveOutputPath_KeepsTenantsApart exercises the wiring, not the helper:
// two connections that configure the SAME output path must not resolve to the
// same directory.
//
// This is the defect it closes. generateFilename preserves metadata.filename
// when the envelope carries one — which a file→file pipeline always does — so
// before this, two tenants both processing "orders.csv" wrote the same path on
// the shared volume and the last writer silently won.
func TestResolveOutputPath_KeepsTenantsApart(t *testing.T) {
	p := &fileProducer{defaultOutputDir: "/data/output"}

	for _, configured := range []string{"", "/data/output", "orders"} {
		a, err := p.resolveOutputPath(&ConnectionConfig{TenantID: "tenant-a", OutputPath: configured, FolderName: "Orders"})
		if err != nil {
			t.Fatalf("tenant-a %q: %v", configured, err)
		}
		b, err := p.resolveOutputPath(&ConnectionConfig{TenantID: "tenant-b", OutputPath: configured, FolderName: "Orders"})
		if err != nil {
			t.Fatalf("tenant-b %q: %v", configured, err)
		}
		if a == b {
			t.Errorf("both tenants write %q to %s — one would overwrite the other", configured, a)
		}
	}
}

// A path outside the mounted root is a permanent misconfiguration, and must
// stay classified as one: the call sites drop the message rather than retrying
// forever against a path that cannot become valid.
func TestResolveOutputPath_RefusesEscapeAsPermanent(t *testing.T) {
	p := &fileProducer{defaultOutputDir: "/data/output"}

	for _, cfg := range []*ConnectionConfig{
		{TenantID: "tenant-a", OutputPath: "/etc"},
		{TenantID: "tenant-a", OutputPath: "../../etc"},
		// FolderName is user-settable and was never checked before.
		{TenantID: "tenant-a", OutputPath: "", FolderName: "../../../etc"},
		{TenantID: "", OutputPath: "orders"}, // no tenant: cannot be placed safely
	} {
		got, err := p.resolveOutputPath(cfg)
		if err == nil {
			t.Errorf("resolveOutputPath(%+v) = %q, want an error", cfg, got)
			continue
		}
		if !errors.Is(err, errPathNotAllowed) {
			t.Errorf("error = %v, want errPathNotAllowed so the call sites treat it as permanent", err)
		}
	}
}
