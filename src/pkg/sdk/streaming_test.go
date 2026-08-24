package sdk

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// testLogger discards output so tests stay quiet.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func drain(t *testing.T, r io.Reader) ([]byte, error) {
	t.Helper()
	return io.ReadAll(r)
}

func TestPublishStream_OffloadsAndPublishesSmallEnvelope(t *testing.T) {
	store := newMemStore()
	res := &Resources{Logger: testLogger(), payloadStore: store}

	var published *envelope.Envelope
	publish := func(_ context.Context, env *envelope.Envelope) error {
		published = env
		return nil
	}
	ps := newPublishStream(publish, res)

	payload := bytes.Repeat([]byte("S"), 5000)
	env := mkEnv(nil)
	env.ContentType = "text/csv"

	if err := ps(context.Background(), env, bytes.NewReader(payload)); err != nil {
		t.Fatalf("publishStream: %v", err)
	}
	if published == nil {
		t.Fatal("envelope was not published")
	}
	if published.Payload != nil {
		t.Errorf("published envelope must not carry the payload (%d bytes)", len(published.Payload))
	}
	if published.PayloadRef == "" {
		t.Fatal("PayloadRef should be set")
	}
	if published.PayloadSize != int64(len(payload)) {
		t.Errorf("PayloadSize = %d, want %d", published.PayloadSize, len(payload))
	}
	if published.Checksum != payloadChecksum(payload) {
		t.Errorf("Checksum = %q, want %q", published.Checksum, payloadChecksum(payload))
	}
	// The bytes really landed in the store.
	if got := store.objects[published.PayloadRef]; !bytes.Equal(got, payload) {
		t.Errorf("stored object mismatch: %d bytes", len(got))
	}
}

func TestPublishStream_RequiresStoreAndID(t *testing.T) {
	publish := func(context.Context, *envelope.Envelope) error { return nil }

	// No store configured.
	noStore := newPublishStream(publish, &Resources{Logger: testLogger()})
	err := noStore(context.Background(), mkEnv(nil), strings.NewReader("x"))
	if err == nil || !IsPermanent(err) {
		t.Fatalf("expected a permanent error without a store, got %v", err)
	}

	// Store present but the envelope has no ID (would collide on the object key).
	withStore := newPublishStream(publish, &Resources{Logger: testLogger(), payloadStore: newMemStore()})
	env := mkEnv(nil)
	env.ID = ""
	if err := withStore(context.Background(), env, strings.NewReader("x")); err == nil || !IsPermanent(err) {
		t.Fatalf("expected a permanent error for an empty envelope ID, got %v", err)
	}
}

func TestDeliverStreamed_HandsOverStreamAndVerifies(t *testing.T) {
	store := newMemStore()
	payload := bytes.Repeat([]byte("D"), 4096)
	env := mkEnv(payload)
	if _, err := offloadIfLarge(context.Background(), store, env, 256, testLogger()); err != nil {
		t.Fatalf("offload: %v", err)
	}

	var got []byte
	err := deliverStreamed(context.Background(), store, env, func(_ context.Context, _ *envelope.Envelope, body io.Reader) error {
		var rerr error
		got, rerr = drain(t, body)
		return rerr
	})
	if err != nil {
		t.Fatalf("deliverStreamed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("streamed payload mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestDeliverStreamed_CorruptObjectFailsTheRead(t *testing.T) {
	store := newMemStore()
	env := mkEnv(bytes.Repeat([]byte("D"), 4096))
	if _, err := offloadIfLarge(context.Background(), store, env, 256, testLogger()); err != nil {
		t.Fatalf("offload: %v", err)
	}
	// Corrupt the object after the checksum was stamped.
	store.objects[env.PayloadRef] = bytes.Repeat([]byte("X"), 4096)

	err := deliverStreamed(context.Background(), store, env, func(_ context.Context, _ *envelope.Envelope, body io.Reader) error {
		_, rerr := io.ReadAll(body)
		return rerr
	})
	if err == nil {
		t.Fatal("expected the connector's read to fail on a corrupt object")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifyingReader_SkipsWhenNoChecksum(t *testing.T) {
	// Envelopes published before checksums existed must still stream.
	r := newVerifyingReader(io.NopCloser(strings.NewReader("legacy")), "")
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "legacy" {
		t.Errorf("got %q", got)
	}
}

func TestErrStreamUnsupported_IsIdentifiable(t *testing.T) {
	// Connectors wrap it; the SDK must still recognise it to fall back.
	wrapped := errors.New("cloud: " + ErrStreamUnsupported.Error())
	if errors.Is(wrapped, ErrStreamUnsupported) {
		t.Fatal("a same-text error must NOT match — callers should return the sentinel")
	}
	if !errors.Is(ErrStreamUnsupported, ErrStreamUnsupported) {
		t.Fatal("sentinel must match itself")
	}
}
