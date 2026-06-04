package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// startConn fires a start command and waits for the webhook to register.
func startConn(t *testing.T, h *harness.ConsumerHarness, c *webhookConsumer, connID, tenant string) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start command: %v", err)
	}
	harness.Eventually(t, 5*time.Second, "webhook registered", func() bool {
		return c.getActiveConnection(connID) != nil
	})
}

// TestWebhookConsumer_RoundTrip drives the SDK-refactored webhook-consumer
// end-to-end with zero Docker: a start command registers the webhook, then a
// POST to the registered /webhook handler publishes the body onto the stream.
func TestWebhookConsumer_RoundTrip(t *testing.T) {
	const (
		connID = "conn-wh-1"
		tenant = "tenant-x"
	)

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)

	nodes := `[{"id":"c1","type":"consumer","config":{"type":"http","http":{}}}]`
	mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(connID, tenant, "WH Conn", []byte(nodes), []byte(`[]`)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE connections SET last_payload").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &webhookConsumer{}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "webhook-consumer", DB: mgmtDB})
	startConn(t, h, c, connID, tenant)

	req := httptest.NewRequest("POST", "/webhook/"+connID, strings.NewReader(`{"hello":"webhook"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleWebhook()(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if got.IntegrationID != connID {
		t.Errorf("integration id = %q, want %q", got.IntegrationID, connID)
	}
	if string(got.Payload) != `{"hello":"webhook"}` {
		t.Errorf("payload = %q", got.Payload)
	}
}

// TestWebhookConsumer_HMAC verifies the #67 signature path is preserved: a
// request with a valid HMAC-SHA256 signature is accepted and published; one
// with a bad signature is rejected with 401 and nothing is published. The
// plaintext secret is carried inline in the node config (the resolver leaves
// non-_secret_id keys untouched).
func TestWebhookConsumer_HMAC(t *testing.T) {
	const (
		connID = "conn-wh-2"
		tenant = "tenant-x"
		secret = "shh-secret"
	)

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)

	nodes := `[{"id":"c1","type":"consumer","config":{"type":"http","http":{"signature":{"header":"X-Signature","algorithm":"sha256","encoding":"hex","secret":"shh-secret"}}}}]`
	mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(connID, tenant, "WH Conn", []byte(nodes), []byte(`[]`)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	// Exactly one successful publish → one last_payload write (the rejected
	// request must not reach the DB).
	mock.ExpectExec("UPDATE connections SET last_payload").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &webhookConsumer{}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "webhook-consumer", DB: mgmtDB})
	startConn(t, h, c, connID, tenant)

	body := `{"event":"signed"}`
	macSum := hmac.New(sha256.New, []byte(secret))
	macSum.Write([]byte(body))
	validSig := hex.EncodeToString(macSum.Sum(nil))

	// Bad signature → 401, no publish.
	bad := httptest.NewRequest("POST", "/webhook/"+connID, strings.NewReader(body))
	bad.Header.Set("X-Signature", "deadbeef")
	badRec := httptest.NewRecorder()
	c.handleWebhook()(badRec, bad)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("bad-signature status = %d, want 401", badRec.Code)
	}

	// Valid signature → 202, published.
	good := httptest.NewRequest("POST", "/webhook/"+connID, strings.NewReader(body))
	good.Header.Set("X-Signature", validSig)
	goodRec := httptest.NewRecorder()
	c.handleWebhook()(goodRec, good)
	if goodRec.Code != http.StatusAccepted {
		t.Fatalf("valid-signature status = %d, want 202; body=%s", goodRec.Code, goodRec.Body.String())
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if string(got.Payload) != body {
		t.Errorf("payload = %q, want %q", got.Payload, body)
	}
}
