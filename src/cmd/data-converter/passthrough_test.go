package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/claimcheck"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// throughNode runs one envelope through a configured node on embedded
// JetStream and returns what it republished as node n1, or nil.
func throughNode(t *testing.T, store objectstore.ObjectStore, in *envelope.Envelope) *envelope.Envelope {
	t.Helper()
	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	defer cleanup()

	s := newTestConverterService(store, &ConverterEntry{NodeID: "n1", Config: &ConverterNodeConfig{OutputFormat: "csv"}, PredIsConsumer: true})
	s.nc = nc
	s.pub = messaging.NewPublisher(js)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()

	out := make(chan *envelope.Envelope, 4)
	sub, err := nc.Subscribe("vrsky.data.tenant-x.pipeline.conn-1", func(m *nats.Msg) {
		var env envelope.Envelope
		if json.Unmarshal(m.Data, &env) == nil && env.Metadata != nil {
			if v, _ := env.Metadata["_last_processed_by"].(string); v == "n1" {
				out <- &env
			}
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	data, _ := json.Marshal(in)
	if _, err := js.Publish("vrsky.data.tenant-x.pipeline.conn-1", data); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case got := <-out:
		return got
	case <-time.After(3 * time.Second):
		return nil
	}
}

func picture(payload []byte) *envelope.Envelope {
	env := envelope.New()
	env.TenantID = "tenant-x"
	env.IntegrationID = "conn-1"
	env.ContentType = "image/jpeg"
	env.Payload = payload
	env.PayloadSize = int64(len(payload))
	env.Metadata = map[string]interface{}{"filename": "1896-S.jpg", "number": "1896-S"}
	return env
}

// A picture travelling beside a catalogue's records (#281) used to fail
// "Payload parse failed" here and be acked — dropped. It must come out the
// other side byte for byte, still named 1896-S.jpg, routed to the next node.
func TestConverter_PassesAPictureThroughUnchanged(t *testing.T) {
	jpeg := []byte("\xff\xd8\xff\xe0 not records at all")
	in := picture(jpeg)
	got := throughNode(t, newMemStore(), in)
	if got == nil {
		t.Fatal("the picture was dropped")
	}
	if !bytes.Equal(got.Payload, jpeg) || got.ContentType != "image/jpeg" {
		t.Errorf("picture changed: %q (%s)", got.Payload, got.ContentType)
	}
	if got.Metadata["filename"] != "1896-S.jpg" || got.Metadata["number"] != "1896-S" {
		t.Errorf("metadata lost: %v", got.Metadata)
	}
	if _, converted := got.Metadata["_converted"]; converted {
		t.Error("marked _converted — the destinations would re-extension 1896-S.jpg")
	}
	if got.ID == in.ID {
		t.Error("same envelope id — JetStream dedup would drop the republish")
	}
}

// An offloaded picture passes as its claim-check ref: not rehydrated (so it
// works even on a worker without a store) and not re-offloaded.
func TestConverter_PassesAnOffloadedPictureByRef(t *testing.T) {
	store := newMemStore()
	in := picture(bytes.Repeat([]byte("x"), 4096))
	if _, err := claimcheck.OffloadIfLarge(context.Background(), store, in, 1, quietLogger()); err != nil {
		t.Fatalf("offload: %v", err)
	}
	ref := in.PayloadRef

	got := throughNode(t, nil, in) // no store on this worker
	if got == nil {
		t.Fatal("the offloaded picture was dropped (or NAKed trying to rehydrate it)")
	}
	if got.PayloadRef != ref || len(got.Payload) != 0 {
		t.Errorf("ref = %q (payload %d bytes), want the original ref %q and no inline bytes", got.PayloadRef, len(got.Payload), ref)
	}
}
