package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/claimcheck"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// memStore is an in-memory objectstore.ObjectStore standing in for the spill
// bucket — no MinIO, no Docker.
type memStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (m *memStore) List(context.Context, string) ([]objectstore.Object, error) { return nil, nil }
func (m *memStore) Get(_ context.Context, key string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objects[key], "", nil
}
func (m *memStore) Put(_ context.Context, key string, body []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = append([]byte(nil), body...)
	return nil
}
func (m *memStore) GetStream(_ context.Context, key string) (io.ReadCloser, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return nil, "", io.ErrUnexpectedEOF
	}
	return io.NopCloser(bytes.NewReader(b)), "", nil
}
func (m *memStore) PutStream(_ context.Context, key string, body io.Reader, _ string) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = b
	return nil
}
func (m *memStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}
func (m *memStore) Copy(context.Context, string, string) error { return nil }
func (m *memStore) Close() error                               { return nil }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestFilterService builds a FilterService with the pipeline cache pre-seeded
// (no DB) and the given spill store.
func newTestFilterService(spill objectstore.ObjectStore, entries ...*FilterEntry) *FilterService {
	s := &FilterService{
		logger:            quietLogger(),
		pipelineCache:     map[string]*PipelineInfo{"conn-1": {Entries: entries}},
		pipelineCacheTime: map[string]time.Time{"conn-1": time.Now()},
		pipelineCacheTTL:  time.Hour,
		eventSubs:         make(map[string][]chan FilterEvent),
		recentEvents:      make(map[string][]FilterEvent),
		spill:             spill,
		inlineMax:         claimcheck.DefaultInlineMaxBytes,
		rehydrateMax:      claimcheck.DefaultRehydrateMaxBytes,
		stopCh:            make(chan struct{}),
		stoppedCh:         make(chan struct{}),
	}
	return s
}

func refEnvelopeMsg(t *testing.T, store objectstore.ObjectStore, payload []byte) *nats.Msg {
	t.Helper()
	env := envelope.New()
	env.TenantID = "tenant-x"
	env.IntegrationID = "conn-1"
	env.ContentType = "application/json"
	env.Payload = payload
	// inlineMax=1 forces the offload regardless of payload size.
	if _, err := claimcheck.OffloadIfLarge(context.Background(), store, env, 1, quietLogger()); err != nil {
		t.Fatalf("offload: %v", err)
	}
	data, _ := json.Marshal(env)
	return &nats.Msg{Subject: "vrsky.data.tenant-x.pipeline.conn-1", Data: data}
}

// THE regression this phase exists for: before ADR 0002 phase A an offloaded
// envelope failed json.Unmarshal(nil), emitted "Invalid JSON payload", and was
// ACKED — silently lost. It must now surface an error so the subscriber NAKs.
func TestFilter_OffloadedEnvelopeWithoutStoreNAKs(t *testing.T) {
	store := newMemStore()
	msg := refEnvelopeMsg(t, store, []byte(`{"keep":"yes"}`))

	// The service has NO spill store configured — rehydrate cannot succeed.
	s := newTestFilterService(nil)
	err := s.handleMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("an offloaded envelope that cannot be rehydrated must NAK, not ack")
	}
}

func TestFilter_OverCapEnvelopeNAKs(t *testing.T) {
	store := newMemStore()
	msg := refEnvelopeMsg(t, store, bytes.Repeat([]byte("A"), 4096))

	s := newTestFilterService(store)
	s.rehydrateMax = 100 // cap far below the payload
	if err := s.handleMessage(context.Background(), msg); err == nil {
		t.Fatal("an over-cap payload must NAK (explicit DLQ path), not ack")
	}
}

// End-to-end on embedded JetStream: an offloaded input is rehydrated, filtered,
// and republished inline — the full silent-drop scenario, fixed.
func TestFilter_RehydratesOffloadedInputAndRepublishes(t *testing.T) {
	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	defer cleanup()

	store := newMemStore()
	entry := &FilterEntry{
		NodeID:         "f1",
		Config:         &FilterNodeConfig{Rules: []FilterRule{{Field: "keep", Operator: "equals", Value: "yes"}}},
		PredIsConsumer: true,
	}
	s := newTestFilterService(store, entry)
	s.nc = nc
	s.pub = messaging.NewPublisher(js)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()

	// Capture republished envelopes (fresh IDs, _last_processed_by=f1).
	out := make(chan *envelope.Envelope, 4)
	sub, err := nc.Subscribe("vrsky.data.tenant-x.pipeline.conn-1", func(m *nats.Msg) {
		var env envelope.Envelope
		if json.Unmarshal(m.Data, &env) == nil && env.Metadata != nil {
			if v, _ := env.Metadata["_last_processed_by"].(string); v == "f1" {
				out <- &env
			}
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	payload := []byte(`[{"keep":"yes","v":1},{"keep":"no","v":2}]`)
	msg := refEnvelopeMsg(t, store, payload)
	if _, err := js.Publish(msg.Subject, msg.Data); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-out:
		if got.PayloadRef != "" {
			t.Errorf("small filtered output should be inline, got ref %q", got.PayloadRef)
		}
		var rows []map[string]interface{}
		if err := json.Unmarshal(got.Payload, &rows); err != nil {
			t.Fatalf("republished payload not JSON: %v", err)
		}
		if len(rows) != 1 || rows[0]["keep"] != "yes" {
			t.Errorf("filter semantics changed: %v", rows)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no republished envelope — the offloaded input was dropped")
	}
}

// A filter can INFLATE a payload past the inline threshold (or receive one just
// under it); the republished result must then be offloaded, not published raw
// into NATS max_payload.
func TestFilter_OffloadsLargeOutput(t *testing.T) {
	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	defer cleanup()

	store := newMemStore()
	entry := &FilterEntry{NodeID: "f1", Config: &FilterNodeConfig{}, PredIsConsumer: true} // pass-through
	s := newTestFilterService(store, entry)
	s.nc = nc
	s.pub = messaging.NewPublisher(js)
	s.inlineMax = 64 // force the output over the threshold
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()

	out := make(chan *envelope.Envelope, 4)
	sub, err := nc.Subscribe("vrsky.data.tenant-x.pipeline.conn-1", func(m *nats.Msg) {
		var env envelope.Envelope
		if json.Unmarshal(m.Data, &env) == nil && env.Metadata != nil {
			if v, _ := env.Metadata["_last_processed_by"].(string); v == "f1" {
				out <- &env
			}
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// Inline input (small envelope), output identical and > inlineMax.
	env := envelope.New()
	env.TenantID = "tenant-x"
	env.IntegrationID = "conn-1"
	env.ContentType = "application/json"
	env.Payload = []byte(`[{"pad":"` + string(bytes.Repeat([]byte("x"), 200)) + `"}]`)
	data, _ := json.Marshal(env)
	if _, err := js.Publish("vrsky.data.tenant-x.pipeline.conn-1", data); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-out:
		if got.PayloadRef == "" {
			t.Fatalf("output of %d bytes should have been offloaded (inlineMax=64)", got.PayloadSize)
		}
		if got.Payload != nil {
			t.Error("offloaded output must not carry the payload inline")
		}
		if got.Checksum == "" {
			t.Error("offloaded output should carry a checksum")
		}
		if b, _, _ := store.Get(context.Background(), got.PayloadRef); len(b) == 0 {
			t.Error("spill store does not contain the offloaded output")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no republished envelope")
	}
}
