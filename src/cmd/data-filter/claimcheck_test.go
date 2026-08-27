package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
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

// Since phase B an over-cap payload STREAMS rather than NAKing outright — but an
// infrastructure failure on that path (the spill object is gone or the store is
// unreachable) must still NAK so the message retries or DLQs, never acks empty.
func TestFilter_OverCapMissingObjectNAKs(t *testing.T) {
	store := newMemStore()
	entry := &FilterEntry{NodeID: "f1", Config: &FilterNodeConfig{Rules: []FilterRule{{Field: "a", Operator: "equals", Value: "b"}}}, PredIsConsumer: true}
	s := newTestFilterService(store, entry)
	s.rehydrateMax = 100

	env := envelope.New()
	env.TenantID = "tenant-x"
	env.IntegrationID = "conn-1"
	env.PayloadRef = "spill/tenant-x/conn-1/vanished"
	env.PayloadSize = 4096 // over the cap → streaming path
	data, _ := json.Marshal(env)
	msg := &nats.Msg{Subject: "vrsky.data.tenant-x.pipeline.conn-1", Data: data}

	if err := s.handleMessage(context.Background(), msg); err == nil {
		t.Fatal("a missing spill object on the streaming path must NAK, not ack")
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

// --- ADR 0002 phase B: record streaming ---

// startStreamingFilter boots a filter on embedded JetStream with a tiny
// rehydrate cap, so any offloaded input takes the streaming path.
func startStreamingFilter(t *testing.T, store objectstore.ObjectStore, entry *FilterEntry) (*FilterService, *nats.Conn, nats.JetStreamContext, chan *envelope.Envelope, func()) {
	t.Helper()
	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	s := newTestFilterService(store, entry)
	s.nc = nc
	s.pub = messaging.NewPublisher(js)
	s.rehydrateMax = 100 // far below the test payloads → streaming path
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	out := make(chan *envelope.Envelope, 4)
	sub, err := nc.Subscribe("vrsky.data.tenant-x.pipeline.conn-1", func(m *nats.Msg) {
		var env envelope.Envelope
		if json.Unmarshal(m.Data, &env) == nil && env.Metadata != nil {
			if v, _ := env.Metadata["_last_processed_by"].(string); v == entry.NodeID {
				out <- &env
			}
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return s, nc, js, out, func() { _ = sub.Unsubscribe(); s.Stop(); cleanup() }
}

// An over-cap array is filtered record by record and republished — never
// buffered, and byte-identical to what the buffered path would produce.
func TestFilter_StreamsOverCapArray(t *testing.T) {
	store := newMemStore()
	entry := &FilterEntry{
		NodeID:         "f1",
		Config:         &FilterNodeConfig{Rules: []FilterRule{{Field: "keep", Operator: "equals", Value: "yes"}}},
		PredIsConsumer: true,
	}
	_, _, js, out, done := startStreamingFilter(t, store, entry)
	defer done()

	// ~4 KB payload, cap is 100 bytes → must stream.
	payload := []byte(`[{"keep":"yes","v":1},{"keep":"no","v":2},{"keep":"yes","pad":"` +
		string(bytes.Repeat([]byte("x"), 4000)) + `"}]`)
	msg := refEnvelopeMsg(t, store, payload)
	if _, err := js.Publish(msg.Subject, msg.Data); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-out:
		var body []byte
		if got.PayloadRef != "" {
			body, _, _ = store.Get(context.Background(), got.PayloadRef)
		} else {
			body = got.Payload
		}
		var rows []map[string]interface{}
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("streamed output not JSON: %v", err)
		}
		if len(rows) != 2 || rows[0]["keep"] != "yes" || rows[1]["keep"] != "yes" {
			t.Errorf("streamed filter semantics wrong: %d rows", len(rows))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no republished envelope from the streaming path")
	}
}

// A large streamed result is re-offloaded via the spool (output over inlineMax).
func TestFilter_StreamedLargeOutputSpills(t *testing.T) {
	store := newMemStore()
	entry := &FilterEntry{NodeID: "f1", Config: &FilterNodeConfig{}, PredIsConsumer: true} // pass-through
	s, _, js, out, done := startStreamingFilter(t, store, entry)
	defer done()
	s.inlineMax = 64 // output must spill

	payload := []byte(`[{"pad":"` + string(bytes.Repeat([]byte("y"), 3000)) + `"}]`)
	msg := refEnvelopeMsg(t, store, payload)
	if _, err := js.Publish(msg.Subject, msg.Data); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-out:
		if got.PayloadRef == "" {
			t.Fatalf("output of %d bytes should have spilled (inlineMax=64)", got.PayloadSize)
		}
		if got.Checksum == "" {
			t.Error("spilled output should carry a checksum")
		}
		body, _, _ := store.Get(context.Background(), got.PayloadRef)
		if !bytes.Equal(body, payload) {
			t.Errorf("pass-through streamed output differs from input: %d vs %d bytes", len(body), len(payload))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no republished envelope")
	}
}

// Flatten needs document context (phase B2) — an over-cap payload at a flatten
// node NAKs instead of streaming wrong results or buffering into an OOM.
func TestFilter_StreamingDeclinesFlatten(t *testing.T) {
	store := newMemStore()
	entry := &FilterEntry{NodeID: "f1", Config: &FilterNodeConfig{FlattenPath: "items"}, PredIsConsumer: true}
	s := newTestFilterService(store, entry)
	s.rehydrateMax = 100

	msg := refEnvelopeMsg(t, store, []byte(`[{"items":[1,2],"pad":"`+string(bytes.Repeat([]byte("p"), 500))+`"}]`))
	if err := s.handleMessage(context.Background(), msg); err == nil {
		t.Fatal("flatten + over-cap must NAK (phase C error policy), not stream")
	}
}

// A single JSON object has no records to stream — over the cap it NAKs.
func TestFilter_StreamingDeclinesSingleObject(t *testing.T) {
	store := newMemStore()
	entry := &FilterEntry{NodeID: "f1", Config: &FilterNodeConfig{Rules: []FilterRule{{Field: "a", Operator: "equals", Value: "b"}}}, PredIsConsumer: true}
	s := newTestFilterService(store, entry)
	s.rehydrateMax = 100

	msg := refEnvelopeMsg(t, store, []byte(`{"a":"b","pad":"`+string(bytes.Repeat([]byte("z"), 500))+`"}`))
	if err := s.handleMessage(context.Background(), msg); err == nil {
		t.Fatal("a single object over the cap cannot record-stream and must NAK")
	}
}

// Rules that drop every record publish nothing — and leave no partial spill
// object behind.
func TestFilter_StreamedAllDroppedPublishesNothing(t *testing.T) {
	store := newMemStore()
	entry := &FilterEntry{
		NodeID:         "f1",
		Config:         &FilterNodeConfig{Rules: []FilterRule{{Field: "keep", Operator: "equals", Value: "never"}}},
		PredIsConsumer: true,
	}
	s := newTestFilterService(store, entry)
	s.rehydrateMax = 100

	msg := refEnvelopeMsg(t, store, []byte(`[{"keep":"no","pad":"`+string(bytes.Repeat([]byte("q"), 500))+`"},{"keep":"also-no"}]`))
	objectsBefore := len(store.objects)
	if err := s.handleMessage(context.Background(), msg); err != nil {
		t.Fatalf("all-dropped is a normal outcome, not an error: %v", err)
	}
	if len(store.objects) != objectsBefore {
		t.Errorf("no output object should exist after an all-dropped stream")
	}
}

// --- ADR 0003: non-JSON input formats ---

// The filter evaluates rules against CSV rows and still emits JSON — its output
// format is deliberately unchanged (ADR 0003).
func TestFilter_CSVInputRulesApplyJSONOut(t *testing.T) {
	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	defer cleanup()

	entry := &FilterEntry{
		NodeID:         "f1",
		Config:         &FilterNodeConfig{Rules: []FilterRule{{Field: "keep", Operator: "equals", Value: "yes"}}},
		PredIsConsumer: true,
	}
	s := newTestFilterService(newMemStore(), entry)
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
			if v, _ := env.Metadata["_last_processed_by"].(string); v == "f1" {
				out <- &env
			}
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	env := envelope.New()
	env.TenantID = "tenant-x"
	env.IntegrationID = "conn-1"
	env.ContentType = "text/csv"
	env.Payload = []byte("keep,v\nyes,1\nno,2\nyes,3\n")
	data, _ := json.Marshal(env)
	if _, err := js.Publish("vrsky.data.tenant-x.pipeline.conn-1", data); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-out:
		var rows []map[string]interface{}
		if err := json.Unmarshal(got.Payload, &rows); err != nil {
			t.Fatalf("filter output should be JSON: %v (%s)", err, got.Payload)
		}
		if len(rows) != 2 {
			t.Errorf("rules should have kept 2 CSV rows, got %d: %v", len(rows), rows)
		}
		for _, r := range rows {
			if r["keep"] != "yes" {
				t.Errorf("wrong row kept: %v", r)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no republished envelope for a CSV input")
	}
}

// An over-cap CSV takes the streaming path — the ADR 0002 machinery now carries
// non-JSON formats too, so a multi-GB CSV never buffers.
func TestFilter_StreamsOverCapCSV(t *testing.T) {
	store := newMemStore()
	entry := &FilterEntry{
		NodeID:         "f1",
		Config:         &FilterNodeConfig{InputFormat: "csv", Rules: []FilterRule{{Field: "keep", Operator: "equals", Value: "yes"}}},
		PredIsConsumer: true,
	}
	_, _, js, out, done := startStreamingFilter(t, store, entry)
	defer done()

	var sb strings.Builder
	sb.WriteString("keep,pad\n")
	for i := 0; i < 200; i++ {
		keep := "no"
		if i%2 == 0 {
			keep = "yes"
		}
		sb.WriteString(keep + "," + strings.Repeat("x", 40) + "\n")
	}
	msg := refEnvelopeMsg(t, store, []byte(sb.String()))
	// refEnvelopeMsg stamps JSON; this payload is CSV.
	patchContentType(t, msg, "text/csv")
	if _, err := js.Publish(msg.Subject, msg.Data); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-out:
		body := got.Payload
		if got.PayloadRef != "" {
			body, _, _ = store.Get(context.Background(), got.PayloadRef)
		}
		var rows []map[string]interface{}
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("streamed CSV output should be JSON: %v", err)
		}
		if len(rows) != 100 {
			t.Errorf("expected 100 kept rows from a streamed CSV, got %d", len(rows))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no republished envelope from the streamed CSV")
	}
}

// patchContentType rewrites the ContentType on an already-marshalled envelope
// message, so the streaming helpers can be reused for non-JSON payloads.
func patchContentType(t *testing.T, msg *nats.Msg, ct string) {
	t.Helper()
	var env envelope.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		t.Fatalf("patch content type: %v", err)
	}
	env.ContentType = ct
	data, err := json.Marshal(&env)
	if err != nil {
		t.Fatalf("patch content type: %v", err)
	}
	msg.Data = data
}
