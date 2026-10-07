package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/idempotency"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Idempotency-Key handling (plans/webhook-idempotency.md), on the embedded
// JetStream: what reaches the stream is the proof, not what the handler says.

const idemSecret = "shh-secret"

// idemEnv is a webhook consumer with the in-memory key store, one or more
// registered connections, and a way to count what landed on the stream.
type idemEnv struct {
	c     *webhookConsumer
	h     *harness.ConsumerHarness
	store *idempotency.MemoryStore
	js    nats.JetStreamContext
}

// newIdemEnv registers conns (id → signed?) for tenant. keys is the store the
// consumer runs with; nil means the in-memory one. It is fixed before the
// consumer starts: the expiry goroutine reads it, so swapping it later would
// be a race the production code never has.
func newIdemEnv(t *testing.T, tenant string, conns map[string]bool, keys idempotency.Store) *idemEnv {
	t.Helper()
	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgmtDB.Close() })
	mock.MatchExpectationsInOrder(false)
	for id, signed := range conns {
		nodes := `[{"id":"c1","type":"consumer","config":{"type":"http","http":{}}}]`
		if signed {
			nodes = `[{"id":"c1","type":"consumer","config":{"type":"http","http":{"signature":{"header":"X-Signature","algorithm":"sha256","encoding":"hex","secret":"` + idemSecret + `"}}}}]`
		}
		mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").
			WithArgs(id, tenant).
			WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
				AddRow(id, tenant, "WH "+id, []byte(nodes), []byte(`[]`)))
		mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	for i := 0; i < 8; i++ { // last_payload writes: one per publish, never checked here
		mock.ExpectExec("UPDATE connections SET last_payload").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	store := idempotency.NewMemoryStore()
	if keys == nil {
		keys = store
	}
	c := &webhookConsumer{keys: keys}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "webhook-consumer", DB: mgmtDB})
	for id := range conns {
		startConn(t, h, c, id, tenant)
	}
	js, err := h.NATS().JetStream()
	if err != nil {
		t.Fatal(err)
	}
	return &idemEnv{c: c, h: h, store: store, js: js}
}

func (e *idemEnv) post(connID, body, key string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/webhook/"+connID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.c.handleWebhook()(rec, req)
	return rec
}

func sign(body string) string {
	m := hmac.New(sha256.New, []byte(idemSecret))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

// streamMsgs is how many messages the data stream holds, after giving
// JetStream a moment to settle.
func (e *idemEnv) streamMsgs(t *testing.T) uint64 {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	info, err := e.js.StreamInfo(messaging.MainStreamName)
	if err != nil {
		t.Fatal(err)
	}
	return info.State.Msgs
}

func envelopeID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		EnvelopeID string `json:"envelope_id"`
		Replayed   bool   `json:"replayed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return out.EnvelopeID
}

func TestWebhook_IdempotencyKey_RepeatIsAcceptedNotPublished(t *testing.T) {
	e := newIdemEnv(t, "tenant-i", map[string]bool{"conn-i1": false}, nil)
	body := `{"sale":"T02-000009"}`

	first := e.post("conn-i1", body, "01a1-key")
	if first.Code != http.StatusAccepted || first.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("first: %d %q %s", first.Code, first.Header().Get("Idempotency-Replayed"), first.Body.String())
	}
	got := e.h.ExpectEnvelope(t, harness.MatchTenant("tenant-i"), 5*time.Second)
	if got.ID != envelopeID(t, first) || got.Metadata["idempotency_key"] != "01a1-key" {
		t.Fatalf("published envelope id=%s metadata=%v; body said %s", got.ID, got.Metadata, first.Body.String())
	}

	// Seconds later, hours later: the same answer, nothing new on the stream.
	for i := 0; i < 3; i++ {
		again := e.post("conn-i1", body, "01a1-key")
		if again.Code != http.StatusAccepted || again.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatalf("repeat %d: %d replayed=%q %s", i+1, again.Code, again.Header().Get("Idempotency-Replayed"), again.Body.String())
		}
		if envelopeID(t, again) != got.ID || !strings.Contains(again.Body.String(), `"replayed":true`) {
			t.Fatalf("repeat %d body = %s, want the first envelope id and replayed:true", i+1, again.Body.String())
		}
	}
	if n := e.streamMsgs(t); n != 1 {
		t.Fatalf("stream holds %d messages, want 1", n)
	}
}

func TestWebhook_IdempotencyKey_DifferentKeysPublishTwice(t *testing.T) {
	e := newIdemEnv(t, "tenant-i", map[string]bool{"conn-i2": false}, nil)
	a := e.post("conn-i2", `{"sale":"1"}`, "key-a")
	b := e.post("conn-i2", `{"sale":"2"}`, "key-b")
	if a.Code != http.StatusAccepted || b.Code != http.StatusAccepted || envelopeID(t, a) == envelopeID(t, b) {
		t.Fatalf("a=%d b=%d ids %s / %s", a.Code, b.Code, envelopeID(t, a), envelopeID(t, b))
	}
	if n := e.streamMsgs(t); n != 2 {
		t.Fatalf("stream holds %d messages, want 2", n)
	}
}

func TestWebhook_IdempotencyKey_PublishFailureForgetsTheKey(t *testing.T) {
	e := newIdemEnv(t, "tenant-i", map[string]bool{"conn-i3": false}, nil)
	real := e.c.publish
	e.c.publish = func(context.Context, *envelope.Envelope) error { return errors.New("nats down") }
	failed := e.post("conn-i3", `{"sale":"3"}`, "key-3")
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("publish failure: want 500, got %d", failed.Code)
	}
	if e.store.Len() != 0 {
		t.Fatal("a key was remembered although nothing was published")
	}
	e.c.publish = real
	retry := e.post("conn-i3", `{"sale":"3"}`, "key-3")
	if retry.Code != http.StatusAccepted || retry.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("retry: %d replayed=%q — must be a real second attempt", retry.Code, retry.Header().Get("Idempotency-Replayed"))
	}
	if n := e.streamMsgs(t); n != 1 {
		t.Fatalf("stream holds %d messages, want 1", n)
	}
}

func TestWebhook_IdempotencyKey_IsPerConnection(t *testing.T) {
	e := newIdemEnv(t, "tenant-i", map[string]bool{"conn-i4a": false, "conn-i4b": false}, nil)
	a := e.post("conn-i4a", `{"sale":"4"}`, "shared-key")
	b := e.post("conn-i4b", `{"sale":"4"}`, "shared-key")
	if a.Code != http.StatusAccepted || b.Code != http.StatusAccepted || b.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("a=%d b=%d replayed=%q", a.Code, b.Code, b.Header().Get("Idempotency-Replayed"))
	}
	if n := e.streamMsgs(t); n != 2 {
		t.Fatalf("stream holds %d messages, want 2", n)
	}
}

// The key is consulted only after the signature: an unsigned probe can
// neither learn that a key exists nor plant one.
func TestWebhook_IdempotencyKey_CheckedAfterSignature(t *testing.T) {
	e := newIdemEnv(t, "tenant-i", map[string]bool{"conn-i5": true}, nil)
	body := `{"sale":"5"}`
	bad := e.post("conn-i5", body, "key-5", "X-Signature", "deadbeef")
	if bad.Code != http.StatusUnauthorized || bad.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("bad signature: %d replayed=%q", bad.Code, bad.Header().Get("Idempotency-Replayed"))
	}
	if e.store.Len() != 0 {
		t.Fatal("an unsigned request planted a key")
	}
	good := e.post("conn-i5", body, "key-5", "X-Signature", sign(body))
	if good.Code != http.StatusAccepted || good.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("good signature after a bad one: %d replayed=%q", good.Code, good.Header().Get("Idempotency-Replayed"))
	}
	// Now a bad signature with the KNOWN key is still 401, not a replay.
	probe := e.post("conn-i5", body, "key-5", "X-Signature", "deadbeef")
	if probe.Code != http.StatusUnauthorized || probe.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("probe with a known key: %d replayed=%q, want 401 and nothing learned", probe.Code, probe.Header().Get("Idempotency-Replayed"))
	}
	if n := e.streamMsgs(t); n != 1 {
		t.Fatalf("stream holds %d messages, want 1", n)
	}
}

func TestWebhook_IdempotencyKey_TooLongAndAbsent(t *testing.T) {
	e := newIdemEnv(t, "tenant-i", map[string]bool{"conn-i6": false}, nil)
	long := e.post("conn-i6", `{"sale":"6"}`, strings.Repeat("k", idempotency.MaxKeyLength+1))
	if long.Code != http.StatusBadRequest {
		t.Fatalf("65-char key: want 400, got %d", long.Code)
	}
	// No header: today's behaviour — a fresh random id every time, nothing remembered.
	a := e.post("conn-i6", `{"sale":"6"}`, "")
	b := e.post("conn-i6", `{"sale":"6"}`, "")
	if a.Code != http.StatusAccepted || b.Code != http.StatusAccepted || envelopeID(t, a) == envelopeID(t, b) || e.store.Len() != 0 {
		t.Fatalf("no header: a=%d b=%d ids %s/%s stored=%d", a.Code, b.Code, envelopeID(t, a), envelopeID(t, b), e.store.Len())
	}
	if n := e.streamMsgs(t); n != 2 {
		t.Fatalf("stream holds %d messages, want 2", n)
	}
	// With a key the envelope id is derived from it: the NATS message id.
	k := e.post("conn-i6", `{"sale":"7"}`, "key-7")
	if envelopeID(t, k) != idempotencyEnvelopeID("conn-i6", "key-7") {
		t.Fatalf("keyed envelope id %s is not derived from the key", envelopeID(t, k))
	}
}

// forgetfulStore never remembers: it stands in for the moment two identical
// requests race past the check, or a store that is down. What then stops the
// duplicate is JetStream's own dedup on the stable Nats-Msg-Id.
type forgetfulStore struct{}

func (forgetfulStore) Seen(context.Context, string, string, string) (string, bool, error) {
	return "", false, nil
}
func (forgetfulStore) Remember(context.Context, string, string, string, string) error { return nil }
func (forgetfulStore) Expire(context.Context, time.Time) (int64, error)               { return 0, nil }

func TestWebhook_IdempotencyKey_SetsNatsMsgID(t *testing.T) {
	e := newIdemEnv(t, "tenant-i", map[string]bool{"conn-i7": false}, forgetfulStore{})
	for i := 0; i < 3; i++ {
		if rec := e.post("conn-i7", `{"sale":"8"}`, "key-8"); rec.Code != http.StatusAccepted {
			t.Fatalf("request %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if n := e.streamMsgs(t); n != 1 {
		t.Fatalf("stream holds %d messages, want 1 — JetStream must dedupe the stable message id", n)
	}
}
