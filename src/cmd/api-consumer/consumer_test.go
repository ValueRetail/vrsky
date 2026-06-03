package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// testKey is a 64-hex (32-byte) AES key used to encrypt the mocked secret.
const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestAPIConsumer_PollWithResolvedAuthValue drives the SDK-refactored
// api-consumer end-to-end with zero Docker and exercises the #98 vault
// unification: a connection whose endpoint carries an auth_value_secret_id is
// started via NATS; the consumer resolves the secret to plaintext, sends it as
// a Bearer token to a test API, and publishes the response onto the data stream.
func TestAPIConsumer_PollWithResolvedAuthValue(t *testing.T) {
	const (
		connID   = "conn-api-1"
		tenant   = "tenant-x"
		secretID = "11111111-1111-1111-1111-111111111111"
		token    = "super-secret-token"
	)
	t.Setenv("ENCRYPTION_KEY", testKey)

	// Test API records the Authorization header it received.
	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	ciphertext, err := crypto.Encrypt(token, testKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)

	// one_time_only so the poll runs once (no ticker); the endpoint path is the
	// full test-server URL and carries an auth_value_secret_id reference.
	nodes := fmt.Sprintf(`[{"id":"c1","type":"consumer","config":{"type":"api","api":{
		"base_url":%q,"one_time_only":true,
		"endpoints":[{"path":%q,"auth_type":"bearer","auth_value_secret_id":%q}]}}}]`,
		srv.URL, srv.URL, secretID)

	mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(connID, tenant, "API Conn", []byte(nodes), []byte(`[]`)))
	// Secret resolution: SELECT ciphertext FROM secrets WHERE id=$1 AND tenant_id=$2
	mock.ExpectQuery("SELECT ciphertext FROM secrets").
		WithArgs(secretID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(ciphertext))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE connections SET last_payload").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &apiConsumer{}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "api-consumer", DB: mgmtDB})

	// Give the command subscription a moment to register, then start.
	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start command: %v", err)
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if got.IntegrationID != connID {
		t.Errorf("integration id = %q, want %q", got.IntegrationID, connID)
	}
	if string(got.Payload) != `{"ok":true}` {
		t.Errorf("payload = %q, want %q", got.Payload, `{"ok":true}`)
	}
	// The resolved secret must have reached the API as a Bearer token.
	if a := gotAuth.Load(); a == nil || a.(string) != "Bearer "+token {
		t.Errorf("Authorization the API saw = %v, want %q", a, "Bearer "+token)
	}
}
