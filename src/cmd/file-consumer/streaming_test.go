package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

type fileProbe struct {
	inline     *envelope.Envelope
	streamed   *envelope.Envelope
	streamBody []byte
}

func (p *fileProbe) publish(_ context.Context, env *envelope.Envelope) error {
	p.inline = env
	return nil
}

func (p *fileProbe) publishStream(_ context.Context, env *envelope.Envelope, body io.Reader) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	p.streamed, p.streamBody = env, b
	return nil
}

// newStreamingFileConsumer wires a consumer against the probe. db is left nil:
// the buffered path's last_payload write is opportunistic and skipped without a
// DB, and the streaming path never touches the DB at all.
func newStreamingFileConsumer(probe *fileProbe, inlineMax int, streaming bool) *fileConsumer {
	c := &fileConsumer{
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		publish:   probe.publish,
		inlineMax: inlineMax,
	}
	if streaming {
		c.publishStream = sdk.PublishStreamFunc(probe.publishStream)
	}
	return c
}

func writeTempFile(t *testing.T, name string, content []byte) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return dir, path
}

// The headline win: a file larger than maxWatchFileBytes used to be rejected
// outright ("file exceeds size limit"). With streaming it is ingested.
func TestIngestWatchedFile_OversizedFileStreamsInsteadOfBeingRejected(t *testing.T) {
	const inlineMax = 64
	content := bytes.Repeat([]byte("X"), maxWatchFileBytes+1024)
	_, path := writeTempFile(t, "huge.csv", content)
	probe := &fileProbe{}
	c := newStreamingFileConsumer(probe, inlineMax, true)
	ac := &ActiveConnection{ConnectionID: "conn-1", TenantID: "tenant-x"}

	env, size, streamed, err := c.ingestWatchedFile(context.Background(), ac, "huge.csv", path)
	if err != nil {
		t.Fatalf("a file over maxWatchFileBytes should now stream, got: %v", err)
	}
	if !streamed {
		t.Error("expected the streaming path")
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	if !bytes.Equal(probe.streamBody, content) {
		t.Errorf("streamed %d bytes, want %d", len(probe.streamBody), len(content))
	}
	if env.ContentType != "text/csv" {
		t.Errorf("ContentType = %q, want text/csv", env.ContentType)
	}
	if env.ID == "" {
		t.Error("streamed envelope needs an ID (it keys the spill object)")
	}
}

// Without streaming, an oversized file is still refused rather than read into
// memory — the ceiling is only lifted when there is somewhere to stream to.
func TestIngestWatchedFile_OversizedStillRefusedWithoutStreaming(t *testing.T) {
	content := bytes.Repeat([]byte("X"), maxWatchFileBytes+1024)
	_, path := writeTempFile(t, "huge.bin", content)
	probe := &fileProbe{}
	c := newStreamingFileConsumer(probe, 64, false) // no payload store
	ac := &ActiveConnection{ConnectionID: "conn-1", TenantID: "tenant-x"}

	_, size, streamed, err := c.ingestWatchedFile(context.Background(), ac, "huge.bin", path)
	if err == nil {
		t.Fatal("expected an oversized file to be refused without streaming")
	}
	if streamed {
		t.Error("nothing should have been streamed")
	}
	if size != int64(len(content)) {
		t.Errorf("size should still be reported for the error event, got %d", size)
	}
	if probe.inline != nil {
		t.Error("the file must not have been read into memory")
	}
}

func TestIngestWatchedFile_SmallFileStaysInline(t *testing.T) {
	content := []byte(`{"id":1}`)
	_, path := writeTempFile(t, "small.json", content)
	probe := &fileProbe{}
	c := newStreamingFileConsumer(probe, 1024, true)
	ac := &ActiveConnection{ConnectionID: "conn-1", TenantID: "tenant-x"}

	env, size, streamed, err := c.ingestWatchedFile(context.Background(), ac, "small.json", path)
	if err != nil {
		t.Fatalf("ingestWatchedFile: %v", err)
	}
	if streamed || probe.streamed != nil {
		t.Error("a file under the inline threshold must not stream")
	}
	if probe.inline == nil || !bytes.Equal(probe.inline.Payload, content) {
		t.Error("expected the file published inline, intact")
	}
	if size != int64(len(content)) || env == nil {
		t.Errorf("size = %d, env = %v", size, env)
	}
}

func TestIngestWatchedFile_MissingFileErrors(t *testing.T) {
	probe := &fileProbe{}
	c := newStreamingFileConsumer(probe, 1024, true)
	ac := &ActiveConnection{ConnectionID: "conn-1", TenantID: "tenant-x"}

	if _, _, _, err := c.ingestWatchedFile(context.Background(), ac,
		"gone.json", filepath.Join(t.TempDir(), "gone.json")); err == nil {
		t.Fatal("expected a stat error for a missing file")
	}
}
