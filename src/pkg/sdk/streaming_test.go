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

// --- dispatchEnvelope: the routing chokepoint (streaming vs buffered) ---

// dispatchProbe records which delivery path a test envelope took.
type dispatchProbe struct {
	delivered    *envelope.Envelope // set when the buffered path ran
	streamedBody []byte             // set when the streaming path ran
	streamCalls  int
	declineWith  error // when non-nil, DeliverStream returns this instead of reading
}

func (p *dispatchProbe) deliver(_ context.Context, env *envelope.Envelope) error {
	p.delivered = env
	return nil
}

func (p *dispatchProbe) deliverStream(_ context.Context, _ *envelope.Envelope, body io.Reader) error {
	p.streamCalls++
	if p.declineWith != nil {
		return p.declineWith
	}
	b, err := io.ReadAll(body)
	p.streamedBody = b
	return err
}

// offloadedEnv puts a payload in the store and returns the (small) envelope
// referencing it, as it would arrive off NATS.
func offloadedEnv(t *testing.T, store *memStore, payload []byte) *envelope.Envelope {
	t.Helper()
	env := mkEnv(payload)
	if _, err := offloadIfLarge(context.Background(), store, env, 256, testLogger()); err != nil {
		t.Fatalf("offload: %v", err)
	}
	return env
}

func TestDispatchEnvelope_StreamsWithoutRehydrating(t *testing.T) {
	store := newMemStore()
	payload := bytes.Repeat([]byte("Z"), 4096)
	env := offloadedEnv(t, store, payload)
	res := &Resources{Logger: testLogger(), payloadStore: store, rehydrateMaxBytes: defaultRehydrateMaxBytes}
	probe := &dispatchProbe{}

	streamed, err := dispatchEnvelope(context.Background(), res, env, probe.deliver, probe.deliverStream)
	if err != nil {
		t.Fatalf("dispatchEnvelope: %v", err)
	}
	if !streamed {
		t.Error("expected the streaming path to be used")
	}
	if probe.delivered != nil {
		t.Error("buffered Deliver must NOT be called on the streaming path")
	}
	if !bytes.Equal(probe.streamedBody, payload) {
		t.Errorf("streamed %d bytes, want %d", len(probe.streamedBody), len(payload))
	}
	// The decisive proof that rehydrate was skipped: it would have populated
	// Payload and cleared PayloadRef.
	if env.Payload != nil {
		t.Errorf("payload was buffered into the envelope (%d bytes) — rehydrate ran", len(env.Payload))
	}
	if env.PayloadRef == "" {
		t.Error("PayloadRef was cleared — rehydrate ran")
	}
}

func TestDispatchEnvelope_FallsBackWhenConnectorDeclines(t *testing.T) {
	store := newMemStore()
	payload := bytes.Repeat([]byte("Z"), 4096)
	env := offloadedEnv(t, store, payload)
	res := &Resources{Logger: testLogger(), payloadStore: store, rehydrateMaxBytes: defaultRehydrateMaxBytes}
	probe := &dispatchProbe{declineWith: ErrStreamUnsupported}

	streamed, err := dispatchEnvelope(context.Background(), res, env, probe.deliver, probe.deliverStream)
	if err != nil {
		t.Fatalf("a declined stream must fall back cleanly, got: %v", err)
	}
	if streamed {
		t.Error("a declined message did not stream")
	}
	if probe.streamCalls != 1 {
		t.Errorf("DeliverStream should have been offered the message once, got %d", probe.streamCalls)
	}
	if probe.delivered == nil {
		t.Fatal("expected fallback to buffered Deliver")
	}
	// Fallback means a full rehydrate: payload restored, ref cleared.
	if !bytes.Equal(probe.delivered.Payload, payload) {
		t.Errorf("fallback delivered %d bytes, want %d", len(probe.delivered.Payload), len(payload))
	}
	if probe.delivered.PayloadRef != "" {
		t.Error("PayloadRef should be cleared after the fallback rehydrate")
	}
}

func TestDispatchEnvelope_BufferedWhenNotStreamingCapable(t *testing.T) {
	store := newMemStore()
	payload := bytes.Repeat([]byte("Z"), 4096)
	env := offloadedEnv(t, store, payload)
	res := &Resources{Logger: testLogger(), payloadStore: store, rehydrateMaxBytes: defaultRehydrateMaxBytes}
	probe := &dispatchProbe{}

	// streamDeliver nil = a plain Producer.
	streamed, err := dispatchEnvelope(context.Background(), res, env, probe.deliver, nil)
	if err != nil {
		t.Fatalf("dispatchEnvelope: %v", err)
	}
	if streamed || probe.streamCalls != 0 {
		t.Error("a non-streaming producer must never take the streaming path")
	}
	if probe.delivered == nil || !bytes.Equal(probe.delivered.Payload, payload) {
		t.Error("expected the buffered path to rehydrate and deliver")
	}
}

func TestDispatchEnvelope_InlinePayloadGoesStraightToDeliver(t *testing.T) {
	res := &Resources{Logger: testLogger(), payloadStore: newMemStore(), rehydrateMaxBytes: defaultRehydrateMaxBytes}
	probe := &dispatchProbe{}
	env := mkEnv([]byte("small")) // never offloaded — no PayloadRef

	streamed, err := dispatchEnvelope(context.Background(), res, env, probe.deliver, probe.deliverStream)
	if err != nil {
		t.Fatalf("dispatchEnvelope: %v", err)
	}
	if streamed || probe.streamCalls != 0 {
		t.Error("an inline payload must not take the streaming path")
	}
	if probe.delivered == nil || string(probe.delivered.Payload) != "small" {
		t.Error("expected direct buffered delivery")
	}
}

func TestDispatchEnvelope_StreamingBypassesTheRehydrateCap(t *testing.T) {
	// The headline claim of ADR 0001: a streaming connector has no size cap.
	store := newMemStore()
	payload := bytes.Repeat([]byte("Z"), 4096)
	env := offloadedEnv(t, store, payload)
	// A cap far below the payload — fatal on the buffered path.
	res := &Resources{Logger: testLogger(), payloadStore: store, rehydrateMaxBytes: 100}

	streamed, err := dispatchEnvelope(context.Background(), res, env, (&dispatchProbe{}).deliver, (&dispatchProbe{}).deliverStream)
	if err != nil {
		t.Fatalf("streaming must ignore the rehydrate cap, got: %v", err)
	}
	if !streamed {
		t.Error("expected the streaming path")
	}

	// Same envelope, same cap, but a non-streaming connector → rejected.
	env2 := offloadedEnv(t, store, payload)
	if _, err := dispatchEnvelope(context.Background(), res, env2, (&dispatchProbe{}).deliver, nil); err == nil {
		t.Fatal("the buffered path must still enforce the cap")
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
