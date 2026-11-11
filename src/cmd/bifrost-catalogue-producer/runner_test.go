package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A failed config or secret read must not ack the message (#704); a
// connection that isn't ours still acks.
func TestConfigRead_FailureIsNotAcked(t *testing.T) {
	harness.RunConfigReadCases(t, harness.ConfigReadSpec{
		New: func(t *testing.T, db *sql.DB) sdk.Producer {
			p := testProducer()
			p.db = db
			return p
		},
		Columns:          []string{"nodes", "edges"},
		OwnNode:          `{"type":"bifrost_catalogue","bifrost_catalogue":{"token_secret_id":"00000000-0000-0000-0000-000000000001"}}`,
		OwnNodeHasSecret: true,
		ForeignNode:      `{"type":"http","http":{"url":"https://example.com"},"auth_value_secret_id":"00000000-0000-0000-0000-000000000002"}`,
		Positive: func(t *testing.T, db *sql.DB) (sdk.Producer, string, func() bool) {
			stub := newBifrostStub(t)
			stub.token = harness.TestSecret // the node's token_secret_id resolves to this
			p := testProducer()
			p.db = db
			node := fmt.Sprintf(`{"type":"bifrost_catalogue","bifrost_catalogue":{"base_url":%q,"token_secret_id":%q}}`, stub.URL(), harness.TestSecretID)
			return p, node, func() bool { return strings.Contains(stub.kinds(), "commit") && len(stub.liveSnapshot()) == 1 }
		},
	})
}

var fastBackoff = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond}

// dlqLastError collects X-Vrsky-Last-Error of every dead-lettered message.
func dlqLastError(t *testing.T, nc *nats.Conn) chan string {
	t.Helper()
	ch := make(chan string, 4)
	sub, err := nc.Subscribe("vrsky.dlq.>", func(m *nats.Msg) {
		select {
		case ch <- m.Header.Get("X-Vrsky-Last-Error"):
		default:
		}
	})
	if err != nil {
		t.Fatalf("subscribe dlq: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return ch
}

// runThroughRunner publishes one envelope through the real SDK runner against
// the embedded JetStream and a node pointing at stub.
func runThroughRunner(t *testing.T, stub *bifrostStub, extra, payload string) (*harness.ProducerHarness, chan string) {
	t.Helper()
	const connID = "bifrost-conn"
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true") // Configure reads it; the stub is on 127.0.0.1
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"p1","type":"producer","config":` + nodeJSON(stub.URL(), extra) + `}]`
	ct := secretCiphertext(t)
	for i := 0; i < 20; i++ { // ServesConnection and Deliver read, five attempts
		mock.ExpectQuery("FROM connections WHERE id").
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
		mock.ExpectQuery(`FROM secrets`).WithArgs(harness.TestSecretID, "tenant-x").
			WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(ct))
	}
	p := testProducer()
	p.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "bifrost-catalogue-producer", DB: db, Connection: connID, Backoff: fastBackoff})
	lastErr := dlqLastError(t, h.NATS())

	env := envelope.New()
	env.IntegrationID = connID
	env.TenantID = "tenant-x"
	env.Payload = []byte(payload)
	h.Publish(t, env)
	return h, lastErr
}

// Through the runner: a refusal is acked and dropped (no DLQ copy), a
// transient failure is retried until it works, and a failure that does not
// clear ends in the DLQ with Bifrost's reason in X-Vrsky-Last-Error.
func TestRunner_Outcomes(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		stub := newBifrostStub(t)
		h, _ := runThroughRunner(t, stub, `"mode":"delta"`, items(5))
		harness.Eventually(t, 5*time.Second, "committed", func() bool { return len(stub.liveSnapshot()) == 5 })
		h.ExpectNoDLQ(t, 500*time.Millisecond)
		if got := stub.kinds(); got != "begin items commit" {
			t.Errorf("calls = %q", got)
		}
	})

	t.Run("refusal is dropped, not retried", func(t *testing.T) {
		stub := newBifrostStub(t)
		stub.seedN(100)
		before := stub.liveSnapshot()
		h, _ := runThroughRunner(t, stub, `"mode":"full"`, items(10))
		harness.Eventually(t, 5*time.Second, "refused", func() bool { return strings.Contains(stub.kinds(), "commit") })
		h.ExpectNoDLQ(t, time.Second)
		if got := stub.kinds(); strings.Count(got, "begin") != 1 {
			t.Errorf("calls = %q, want one attempt", got)
		}
		sameCatalogue(t, stub.liveSnapshot(), before)
	})

	t.Run("transient failure is retried to success", func(t *testing.T) {
		stub := newBifrostStub(t)
		stub.failOnce("items", 1, fault{status: 503, body: `{"error":"busy"}`})
		stub.failOnce("commit", 2, fault{status: 500, body: `{"error":"boom"}`}) // 2nd attempt's commit
		h, _ := runThroughRunner(t, stub, `"mode":"full"`, items(5))
		harness.Eventually(t, 10*time.Second, "committed", func() bool { return len(stub.liveSnapshot()) == 5 })
		h.ExpectNoDLQ(t, 500*time.Millisecond)
		if n := stub.openImports(); n != 0 {
			t.Errorf("%d imports left open", n)
		}
	})

	t.Run("credential error ends in the DLQ", func(t *testing.T) {
		stub := newBifrostStub(t)
		for i := 1; i <= 5; i++ {
			stub.failOnce("begin", i, fault{status: 403, body: `{"error":"forbidden"}`})
		}
		h, lastErr := runThroughRunner(t, stub, `"mode":"delta"`, items(2))
		got := h.ExpectDLQ(t, 15*time.Second)
		if !strings.Contains(string(got.Payload), "A00000") {
			t.Errorf("DLQ payload = %q", got.Payload)
		}
		select {
		case e := <-lastErr:
			if !strings.Contains(e, "bifrost begin 403") || !strings.Contains(e, "retry the message from the DLQ") || strings.Contains(e, stubToken) {
				t.Errorf("X-Vrsky-Last-Error = %q", e)
			}
		case <-time.After(2 * time.Second):
			t.Error("no X-Vrsky-Last-Error")
		}
	})
}

// Configure builds the production client: it refuses loopback unless the
// operator's dev flag is set.
func TestConfigure_ClientRefusesLoopback(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "")
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	p := &catalogueProducer{}
	if err := p.Configure(context.Background(), &sdk.Resources{DB: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}); err != nil {
		t.Fatal(err)
	}
	if p.allowPrivate {
		t.Fatal("allowPrivate is on without the env var")
	}
	stub := newBifrostStub(t)
	if resp, err := p.httpClient.Get(stub.URL() + "/healthz"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the configured client connected to loopback")
	}
	if got := stub.kinds(); got != "" {
		t.Errorf("stub saw %q", got)
	}

	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
	p2 := &catalogueProducer{}
	if err := p2.Configure(context.Background(), &sdk.Resources{DB: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}); err != nil {
		t.Fatal(err)
	}
	if !p2.allowPrivate {
		t.Fatal("allowPrivate is off with the env var set")
	}
	if resp, err := p2.httpClient.Get(stub.URL() + "/healthz"); err != nil {
		t.Errorf("with the dev flag the client could not reach the stub: %v", err)
	} else {
		_ = resp.Body.Close()
	}
}
