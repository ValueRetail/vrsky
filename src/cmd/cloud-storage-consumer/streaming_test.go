package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// ingestProbe records which publish path an object took.
type ingestProbe struct {
	inline     *envelope.Envelope
	streamed   *envelope.Envelope
	streamBody []byte
}

func (p *ingestProbe) publish(_ context.Context, env *envelope.Envelope) error {
	p.inline = env
	return nil
}

func (p *ingestProbe) publishStream(_ context.Context, env *envelope.Envelope, body io.Reader) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	p.streamed = env
	p.streamBody = b
	// The SDK stamps the size after streaming; mimic that so callers see it.
	env.PayloadSize = int64(len(b))
	return nil
}

// newTestConsumer builds a consumer wired to the probe. streaming=false models a
// worker with no payload store configured.
func newTestConsumer(probe *ingestProbe, inlineMax int, streaming bool) *cloudConsumer {
	s := &cloudConsumer{
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		publish:   probe.publish,
		inlineMax: inlineMax,
	}
	if streaming {
		s.publishStream = sdk.PublishStreamFunc(probe.publishStream)
	}
	return s
}

func TestFetchAndPublish_LargeObjectStreams(t *testing.T) {
	const inlineMax = 64
	payload := bytes.Repeat([]byte("L"), inlineMax*4)
	store := &fakeStore{objects: map[string][]byte{"in/big.bin": payload}}
	probe := &ingestProbe{}
	s := newTestConsumer(probe, inlineMax, true)

	size, streamed, err := s.fetchAndPublish(context.Background(), "conn-1", "tenant-x",
		store, &cloudConfig{}, "in/big.bin")
	if err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if !streamed {
		t.Error("an object over the inline threshold should stream")
	}
	if probe.inline != nil {
		t.Error("the inline publish path must not be used for a streamed object")
	}
	if !bytes.Equal(probe.streamBody, payload) {
		t.Errorf("streamed %d bytes, want %d", len(probe.streamBody), len(payload))
	}
	if size != int64(len(payload)) {
		t.Errorf("size = %d, want %d", size, len(payload))
	}
	// Metadata must survive the streaming path.
	if probe.streamed.ID == "" {
		t.Error("streamed envelope needs an ID (it keys the spill object)")
	}
	if probe.streamed.Metadata["object_key"] != "in/big.bin" {
		t.Errorf("metadata lost: %+v", probe.streamed.Metadata)
	}
}

func TestFetchAndPublish_SmallObjectStaysInline(t *testing.T) {
	const inlineMax = 64
	payload := []byte(`{"small":true}`)
	store := &fakeStore{objects: map[string][]byte{"in/small.json": payload}}
	probe := &ingestProbe{}
	s := newTestConsumer(probe, inlineMax, true)

	size, streamed, err := s.fetchAndPublish(context.Background(), "conn-1", "tenant-x",
		store, &cloudConfig{}, "in/small.json")
	if err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if streamed || probe.streamed != nil {
		t.Error("an object under the inline threshold must not stream")
	}
	if probe.inline == nil || !bytes.Equal(probe.inline.Payload, payload) {
		t.Error("expected the object to be published inline, intact")
	}
	if size != int64(len(payload)) {
		t.Errorf("size = %d, want %d", size, len(payload))
	}
}

// An object exactly at the threshold is small — the boundary must not stream.
func TestFetchAndPublish_ExactlyAtThresholdStaysInline(t *testing.T) {
	const inlineMax = 64
	payload := bytes.Repeat([]byte("E"), inlineMax)
	store := &fakeStore{objects: map[string][]byte{"in/edge.bin": payload}}
	probe := &ingestProbe{}
	s := newTestConsumer(probe, inlineMax, true)

	_, streamed, err := s.fetchAndPublish(context.Background(), "conn-1", "tenant-x",
		store, &cloudConfig{}, "in/edge.bin")
	if err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if streamed {
		t.Error("a payload exactly at the inline threshold should stay inline")
	}
	if probe.inline == nil || len(probe.inline.Payload) != inlineMax {
		t.Error("expected the whole object inline")
	}
}

// Without a payload store the SDK never supplies publishStream, so a large
// object still takes the buffered path — behaviour unchanged from before.
func TestFetchAndPublish_NoStreamingFallsBackToBufferedGet(t *testing.T) {
	payload := bytes.Repeat([]byte("B"), 512)
	store := &fakeStore{objects: map[string][]byte{"in/big.bin": payload}}
	probe := &ingestProbe{}
	s := newTestConsumer(probe, 64, false) // no publishStream

	_, streamed, err := s.fetchAndPublish(context.Background(), "conn-1", "tenant-x",
		store, &cloudConfig{}, "in/big.bin")
	if err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if streamed {
		t.Error("a worker without a payload store cannot stream")
	}
	if probe.inline == nil || !bytes.Equal(probe.inline.Payload, payload) {
		t.Error("expected the buffered path to publish the whole object")
	}
}

// Content type is sniffed from the peeked head, so a streamed object is still
// typed even though it was never fully read into memory.
func TestFetchAndPublish_StreamedObjectSniffsContentTypeFromHead(t *testing.T) {
	const inlineMax = 16
	payload := append([]byte("{"), bytes.Repeat([]byte("x"), inlineMax*4)...)
	store := &fakeStore{objects: map[string][]byte{"in/big": payload}}
	probe := &ingestProbe{}
	s := newTestConsumer(probe, inlineMax, true)

	if _, streamed, err := s.fetchAndPublish(context.Background(), "conn-1", "tenant-x",
		store, &cloudConfig{}, "in/big"); err != nil || !streamed {
		t.Fatalf("expected a streamed ingest, got streamed=%v err=%v", streamed, err)
	}
	if probe.streamed.ContentType != "application/json" {
		t.Errorf("ContentType = %q, want application/json (sniffed from the head)", probe.streamed.ContentType)
	}
}
