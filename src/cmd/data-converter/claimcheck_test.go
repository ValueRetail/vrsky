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

func newTestConverterService(spill objectstore.ObjectStore, entries ...*ConverterEntry) *ConverterService {
	return &ConverterService{
		logger:            quietLogger(),
		pipelineCache:     map[string]*ConverterPipelineInfo{"conn-1": {Entries: entries}},
		pipelineCacheTime: map[string]time.Time{"conn-1": time.Now()},
		pipelineCacheTTL:  time.Hour,
		eventSubs:         make(map[string][]chan ConvertEvent),
		recentEvents:      make(map[string][]ConvertEvent),
		spill:             spill,
		inlineMax:         claimcheck.DefaultInlineMaxBytes,
		rehydrateMax:      claimcheck.DefaultRehydrateMaxBytes,
		stopCh:            make(chan struct{}),
		stoppedCh:         make(chan struct{}),
	}
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

// Before ADR 0002 phase A an offloaded envelope failed json.Unmarshal(nil),
// emitted an error event, and was ACKED — silently lost. It must NAK.
func TestConverter_OffloadedEnvelopeWithoutStoreNAKs(t *testing.T) {
	store := newMemStore()
	msg := refEnvelopeMsg(t, store, []byte(`{"name":"a"}`))

	s := newTestConverterService(nil) // no spill store configured
	if err := s.handleMessage(context.Background(), msg); err == nil {
		t.Fatal("an offloaded envelope that cannot be rehydrated must NAK, not ack")
	}
}

// End-to-end on embedded JetStream, both claim-check directions in one flow: an
// offloaded input is rehydrated, mapped — and the result, over a small inline
// threshold, is offloaded again on republish.
func TestConverter_RehydratesInputAndOffloadsOutput(t *testing.T) {
	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	defer cleanup()

	store := newMemStore()
	entry := &ConverterEntry{
		NodeID:         "cv1",
		Config:         &ConverterNodeConfig{Mappings: []FieldMapping{{Source: "name", Target: "full_name", Type: "rename"}}},
		PredIsConsumer: true,
	}
	s := newTestConverterService(store, entry)
	s.nc = nc
	s.pub = messaging.NewPublisher(js)
	s.inlineMax = 32 // force the output over the threshold
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()

	out := make(chan *envelope.Envelope, 4)
	sub, err := nc.Subscribe("vrsky.data.tenant-x.pipeline.conn-1", func(m *nats.Msg) {
		var env envelope.Envelope
		if json.Unmarshal(m.Data, &env) == nil && env.Metadata != nil {
			if v, _ := env.Metadata["_last_processed_by"].(string); v == "cv1" {
				out <- &env
			}
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	payload := []byte(`{"name":"acme","padding":"` + string(bytes.Repeat([]byte("x"), 100)) + `"}`)
	msg := refEnvelopeMsg(t, store, payload)
	if _, err := js.Publish(msg.Subject, msg.Data); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-out:
		if got.PayloadRef == "" {
			t.Fatalf("converted output of %d bytes should have been offloaded (inlineMax=32)", got.PayloadSize)
		}
		body, _, _ := store.Get(context.Background(), got.PayloadRef)
		var obj map[string]interface{}
		if err := json.Unmarshal(body, &obj); err != nil {
			t.Fatalf("offloaded output not JSON: %v", err)
		}
		if obj["full_name"] != "acme" {
			t.Errorf("mapping semantics changed: %v", obj)
		}
		if _, stillThere := obj["name"]; stillThere {
			t.Errorf("rename should have removed the source field: %v", obj)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no republished envelope — the offloaded input was dropped")
	}
}

// --- ADR 0002 phase B: record streaming ---

// streamThrough boots a converter on embedded JetStream with a tiny rehydrate
// cap, pushes an offloaded payload through, and returns the output bytes
// (inline or read back from the spill store).
func streamThrough(t *testing.T, cfg *ConverterNodeConfig, payload []byte) []byte {
	t.Helper()
	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	defer cleanup()

	store := newMemStore()
	entry := &ConverterEntry{NodeID: "cv1", Config: cfg, PredIsConsumer: true}
	s := newTestConverterService(store, entry)
	s.nc = nc
	s.pub = messaging.NewPublisher(js)
	s.rehydrateMax = 100 // far below the payloads → streaming path
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()

	out := make(chan *envelope.Envelope, 4)
	sub, err := nc.Subscribe("vrsky.data.tenant-x.pipeline.conn-1", func(m *nats.Msg) {
		var env envelope.Envelope
		if json.Unmarshal(m.Data, &env) == nil && env.Metadata != nil {
			if v, _ := env.Metadata["_last_processed_by"].(string); v == "cv1" {
				out <- &env
			}
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	msg := refEnvelopeMsg(t, store, payload)
	if _, err := js.Publish(msg.Subject, msg.Data); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-out:
		if got.PayloadRef != "" {
			body, _, _ := store.Get(context.Background(), got.PayloadRef)
			return body
		}
		return got.Payload
	case <-time.After(5 * time.Second):
		t.Fatal("no republished envelope from the streaming path")
		return nil
	}
}

// bufferedExpected computes what the BUFFERED path produces for the same input,
// using the very functions it runs — the parity oracle.
func bufferedExpected(t *testing.T, cfg *ConverterNodeConfig, payload []byte) []byte {
	t.Helper()
	var data interface{}
	if err := json.Unmarshal(payload, &data); err != nil {
		t.Fatalf("test payload: %v", err)
	}
	arr := data.([]interface{})
	mapped := make([]interface{}, 0, len(arr))
	for _, item := range arr {
		if obj, ok := item.(map[string]interface{}); ok && len(cfg.Mappings) > 0 {
			m, _ := applyMappings(obj, cfg)
			mapped = append(mapped, m)
		} else {
			mapped = append(mapped, item)
		}
	}
	if cfg.OutputFormat == "" {
		b, _ := json.Marshal(mapped)
		return b
	}
	rows := toRows(mapped)
	formatted, _, _ := convertFormat(rows, cfg)
	return []byte(formatted)
}

func streamingParityPayload() []byte {
	// Heterogeneous values + padding to clear the 100-byte cap.
	return []byte(`[{"name":"a","n":1,"pad":"` + string(bytes.Repeat([]byte("x"), 200)) + `"},` +
		`{"name":"b,with:delim","n":2,"pad":"y"},{"name":"c\"quoted\"","n":3,"pad":"z"}]`)
}

func TestConverter_StreamedJSONMatchesBuffered(t *testing.T) {
	cfg := &ConverterNodeConfig{Mappings: []FieldMapping{{Source: "name", Target: "full_name", Type: "rename"}}}
	payload := streamingParityPayload()
	got := streamThrough(t, cfg, payload)
	want := bufferedExpected(t, cfg, payload)
	if !bytes.Equal(got, want) {
		t.Errorf("streamed JSON differs from buffered:\n got: %s\nwant: %s", got, want)
	}
}

func TestConverter_StreamedNDJSONMatchesBuffered(t *testing.T) {
	cfg := &ConverterNodeConfig{OutputFormat: "ndjson", Mappings: []FieldMapping{{Source: "name", Target: "full_name", Type: "rename"}}}
	payload := streamingParityPayload()
	got := streamThrough(t, cfg, payload)
	want := bufferedExpected(t, cfg, payload)
	if !bytes.Equal(got, want) {
		t.Errorf("streamed NDJSON differs from buffered:\n got: %s\nwant: %s", got, want)
	}
}

func TestConverter_StreamedCSVMatchesBuffered(t *testing.T) {
	cfg := &ConverterNodeConfig{OutputFormat: "csv"}
	payload := streamingParityPayload()
	got := streamThrough(t, cfg, payload)
	want := bufferedExpected(t, cfg, payload)
	if !bytes.Equal(got, want) {
		t.Errorf("streamed CSV differs from buffered:\n got: %s\nwant: %s", got, want)
	}
}

// XML needs whole-document context phase B doesn't cover — over-cap NAKs
// (phase-C error policy) instead of producing wrong output.
func TestConverter_StreamingDeclinesXML(t *testing.T) {
	store := newMemStore()
	entry := &ConverterEntry{NodeID: "cv1", Config: &ConverterNodeConfig{OutputFormat: "xml"}, PredIsConsumer: true}
	s := newTestConverterService(store, entry)
	s.rehydrateMax = 100

	msg := refEnvelopeMsg(t, store, streamingParityPayload())
	if err := s.handleMessage(context.Background(), msg); err == nil {
		t.Fatal("xml + over-cap must NAK (phase C error policy), not stream")
	}
}
