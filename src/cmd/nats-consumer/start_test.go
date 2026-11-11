package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// End to end through the SDK runner: a start command reads the node from the
// database, resolves the password from a stored secret, consumes from the
// external server and publishes into VRSky; a stop command ends it.
func TestStartStopCommand_StoredSecret(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testKey)
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")

	ext := startExternal(t, func(o *natsserver.Options) { o.Username, o.Password = "rh", tPassword })
	_, js := ext.admin(nats.UserInfo("rh", tPassword))
	addStream(t, js)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-1", `{"sku":"A"}`)

	ciphertext, err := crypto.Encrypt(tPassword, testKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	const secretID = "6f0b9d34-0f5c-4c8e-9a52-1a2b3c4d5e6f"
	nodes, _ := json.Marshal([]map[string]any{{
		"id": "src", "type": "consumer",
		"config": map[string]any{"type": "nats", "nats": map[string]any{
			"servers": []string{ext.url()}, "auth_type": "user_password", "username": "rh",
			"password_secret_id": secretID, "stream": tStream, "durable": tDur, "filter_subject": tSubj,
		}},
	}})

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(tConn, tTenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow(nodes))
	mock.ExpectQuery("SELECT ciphertext FROM secrets").WithArgs(secretID, tTenant).
		WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(ciphertext))
	mock.ExpectExec(`UPDATE connections SET status = \$1, started_at = NOW\(\), updated_at = NOW\(\), last_error = NULL`).
		WithArgs("running", tConn, tTenant).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE connections SET status = \$1, stopped_at = NOW\(\)`).
		WithArgs("stopped", tConn, tTenant).WillReturnResult(sqlmock.NewResult(0, 1))

	c := &natsConsumer{}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "nats-consumer", DB: db})

	// A start sent before the command subscription exists is lost, so send
	// it until the connection is active; a repeat is refused by the guard
	// without touching the database.
	cmd, _ := json.Marshal(map[string]string{"connection_id": tConn, "tenant_id": tTenant})
	harness.Eventually(t, wait, "connection active", func() bool {
		if err := h.NATS().Publish("vrsky.commands."+tTenant+".connection.start", cmd); err != nil {
			t.Fatalf("publish start: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		c.mu.RLock()
		defer c.mu.RUnlock()
		_, ok := c.active[tConn]
		return ok
	})
	got := h.ExpectEnvelope(t, harness.MatchTenant(tTenant), wait)
	if got.ID != idFor("evt-1") || string(got.Payload) != `{"sku":"A"}` {
		t.Errorf("envelope id %s payload %s", got.ID, got.Payload)
	}
	harness.Eventually(t, wait, "acked on the source", func() bool {
		info, err := js.ConsumerInfo(tStream, tDur)
		return err == nil && info.AckFloor.Stream == 1
	})

	if err := h.NATS().Publish("vrsky.commands."+tTenant+".connection.stop", cmd); err != nil {
		t.Fatalf("publish stop: %v", err)
	}
	harness.Eventually(t, wait, "stopped", func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		_, ok := c.active[tConn]
		return !ok
	})
	harness.Eventually(t, wait, "status writes done", func() bool { return mock.ExpectationsWereMet() == nil })
}

// A stop naming another tenant leaves the connection running (#498).
func TestStopCommand_OtherTenantIgnored(t *testing.T) {
	c := newTestConsumer(nil)
	ctxCancelled := false
	claim, ok := c.starts.ReserveOrRetry(&c.mu, tTenant, tConn, func() bool { return false }, func() {})
	if !ok || !claim.Activate(func() { c.active[tConn] = &activeConn{cancel: func() { ctxCancelled = true }} }) {
		t.Fatal("could not activate the connection")
	}
	data, _ := json.Marshal(map[string]string{"connection_id": tConn, "tenant_id": "tenant-other"})
	c.handleStopCommand(&nats.Msg{Subject: "vrsky.commands.tenant-other.connection.stop", Data: data})
	if ctxCancelled {
		t.Fatal("a stop for another tenant cancelled the connection")
	}
}

func postTest(t *testing.T, c *natsConsumer, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	c.handleTestConnection()(rr, httptest.NewRequest(http.MethodPost, "/test-connection/", bytes.NewReader(b)))
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("response %q: %v", rr.Body.String(), err)
	}
	return out
}

func TestTestConnection(t *testing.T) {
	ext := startExternal(t, func(o *natsserver.Options) { o.Username, o.Password = "rh", "right-password" })
	_, js := ext.admin(nats.UserInfo("rh", "right-password"))
	addStream(t, js)

	c := newTestConsumer(nil)
	c.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	draft := map[string]any{
		"servers": []string{ext.url()}, "auth_type": "user_password", "username": "rh",
		"password": "right-password", "stream": tStream, "durable": tDur,
	}

	out := postTest(t, c, draft)
	if out["ok"] != true || !strings.Contains(out["note"].(string), "creates it") {
		t.Fatalf("missing durable: %v", out)
	}
	if _, err := js.ConsumerInfo(tStream, tDur); err == nil {
		t.Fatal("the test created the durable")
	}

	addDurable(t, js, time.Second)
	if out := postTest(t, c, draft); out["ok"] != true || out["note"] != nil {
		t.Fatalf("existing durable: %v", out)
	}

	draft["password"] = tPassword
	out = postTest(t, c, draft)
	if out["ok"] != false || !strings.Contains(out["error"].(string), "refused the credentials") {
		t.Fatalf("bad password: %v", out)
	}
	if strings.Contains(out["error"].(string), tPassword) {
		t.Fatal("the error quotes the password")
	}

	draft["stream"] = "NOPE"
	draft["password"] = "right-password"
	if out := postTest(t, c, draft); out["ok"] != false || !strings.Contains(out["error"].(string), "does not exist") {
		t.Fatalf("unknown stream: %v", out)
	}

	c.allowPrivate = false
	draft["stream"] = tStream
	if out := postTest(t, c, draft); out["ok"] != false || !strings.Contains(out["error"].(string), "private") {
		t.Fatalf("private address without the dev flag: %v", out)
	}

	rr := httptest.NewRecorder()
	c.handleTestConnection()(rr, httptest.NewRequest(http.MethodGet, "/test-connection/", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d", rr.Code)
	}
}

// A host that does not resolve: the answer says so without naming the
// resolver or any address.
func TestTestConnection_TransportErrorWithoutAddresses(t *testing.T) {
	c := newTestConsumer(nil)
	out := postTest(t, c, map[string]any{
		"servers": []string{"tls://nats.does-not-exist.invalid:4222"}, "stream": tStream, "durable": tDur,
	})
	msg, _ := out["error"].(string)
	if out["ok"] != false || !strings.Contains(msg, "could not reach the NATS server") {
		t.Fatalf("answer = %v", out)
	}
	if regexp.MustCompile(`\d+\.\d+\.\d+\.\d+|:53\b|lookup`).MatchString(msg) {
		t.Errorf("the answer names an address or the resolver: %q", msg)
	}
}
