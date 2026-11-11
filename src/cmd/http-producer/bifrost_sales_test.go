package main

// The delivery half of the Bifrost sales → Retailhero template (#817). The
// converter's output for a Basket sale (test/fixtures/bifrost/
// intake-sale-batch.json, held by cmd/data-converter/bifrost_sales_test.go) is
// published into embedded JetStream and sent by the real producer to a stub of
// Retailhero's C3 intake. The stub answers as the C3 contract says an intake
// does for an operation ID it has seen: with the original result, once.
//
// Retailhero's intake (ValueRetail/Retailhero#2678) is not built. The route,
// the status codes and where the operation ID sits in the body are the
// template's assumptions, listed in docs/connectors/bifrost-sales-to-retailhero.md.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// retailheroIntake records every request and the distinct operation IDs in
// the batches it received.
type retailheroIntake struct {
	mu       sync.Mutex
	bodies   [][]byte
	ctypes   []string
	messages []string
	ops      map[string]int
}

func (s *retailheroIntake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var batch struct {
		Entries []struct {
			OperationID string `json:"operationId"`
		} `json:"entries"`
	}
	_ = json.Unmarshal(b, &batch)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, b)
	s.ctypes = append(s.ctypes, r.Header.Get("Content-Type"))
	s.messages = append(s.messages, r.Header.Get("X-Message-ID"))
	for _, e := range batch.Entries {
		s.ops[e.OperationID]++
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *retailheroIntake) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func startBifrostProducer(t *testing.T, url string) *harness.ProducerHarness {
	t.Helper()
	allowLocalTarget(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mockConnRows(mock, "conn-bifrost-1", url)
	return harness.NewProducerHarness(t, &httpProducer{}, harness.Options{Name: "http-producer", DB: db})
}

func bifrostBatch(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "bifrost", "intake-sale-batch.json"))
	if err != nil {
		t.Fatalf("read golden batch: %v", err)
	}
	return b
}

func batchEnvelope(id string, payload []byte) *envelope.Envelope {
	env := envelope.New()
	env.ID = id
	env.IntegrationID = "conn-bifrost-1"
	env.TenantID = "tenant-1"
	env.ContentType = "application/json"
	env.Payload = payload
	return env
}

// The batch reaches the intake byte for byte as JSON; the producer adds
// nothing to the body.
func TestBifrostSales_BatchReachesTheIntakeUnchanged(t *testing.T) {
	stub := &retailheroIntake{ops: map[string]int{}}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	h := startBifrostProducer(t, srv.URL)

	golden := bifrostBatch(t)
	h.Publish(t, batchEnvelope("bifrost-env-1", golden))

	harness.Eventually(t, 5*time.Second, "intake received the batch", func() bool { return stub.requests() == 1 })
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if string(stub.bodies[0]) != string(golden) {
		t.Errorf("body changed on the way:\n got: %s\nwant: %s", stub.bodies[0], golden)
	}
	if stub.ctypes[0] != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", stub.ctypes[0])
	}
	if stub.messages[0] != "bifrost-env-1" {
		t.Errorf("X-Message-ID = %q, want the envelope ID", stub.messages[0])
	}
	if n := stub.ops["bifrost-sale-4f6b2d1e-8a3c-5b7d-9e0f-1a2b3c4d5e6f-r1"]; n != 1 {
		t.Errorf("operation seen %d times, want 1 (%v)", n, stub.ops)
	}
}

// A redelivery that outlives JetStream's duplicate window arrives as a new
// envelope with the same batch. The producer sends it again, and what keeps
// the sale from being counted twice is the operation ID inside it: identical
// both times, so the intake can answer the second with the first's result.
func TestBifrostSales_ARedeliveredSaleCarriesTheSameOperationID(t *testing.T) {
	stub := &retailheroIntake{ops: map[string]int{}}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	h := startBifrostProducer(t, srv.URL)

	golden := bifrostBatch(t)
	h.Publish(t, batchEnvelope("bifrost-env-a", golden))
	h.Publish(t, batchEnvelope("bifrost-env-b", golden))

	harness.Eventually(t, 5*time.Second, "both deliveries sent", func() bool { return stub.requests() == 2 })
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.ops) != 1 {
		t.Fatalf("two deliveries of one sale gave %d distinct operation IDs: %v", len(stub.ops), stub.ops)
	}
}

// A return batch (#852) goes out the same way, unchanged, with the return's
// operation ID and the till's journal position.
func TestBifrostSales_AReturnBatchReachesTheIntakeUnchanged(t *testing.T) {
	stub := &retailheroIntake{ops: map[string]int{}}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	h := startBifrostProducer(t, srv.URL)

	golden, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "bifrost", "intake-return-batch.json"))
	if err != nil {
		t.Fatalf("read golden return batch: %v", err)
	}
	h.Publish(t, batchEnvelope("bifrost-env-r1", golden))

	harness.Eventually(t, 5*time.Second, "intake received the return", func() bool { return stub.requests() == 1 })
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if string(stub.bodies[0]) != string(golden) {
		t.Errorf("body changed on the way:\n got: %s\nwant: %s", stub.bodies[0], golden)
	}
	if n := stub.ops["bifrost-return-4cd196df-f270-5c29-b5ed-506059330c21-r1"]; n != 1 {
		t.Errorf("return operation seen %d times, want 1 (%v)", n, stub.ops)
	}
	var batch struct {
		Entries []struct {
			JournalPosition string `json:"journalPosition"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(stub.bodies[0], &batch); err != nil || len(batch.Entries) != 1 || batch.Entries[0].JournalPosition != "43" {
		t.Errorf("journalPosition = %+v (%v), want \"43\"", batch.Entries, err)
	}
}
