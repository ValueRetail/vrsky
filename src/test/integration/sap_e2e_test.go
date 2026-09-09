//go:build integration
// +build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	_ "github.com/lib/pq"
)

// The first test that proves a record travels the whole platform.
//
// Everything else in the suite checks a component, or the seam between two of
// them (test/contract). None of it starts the real services. This one does:
//
//	mock-sap  --GET-->  sap-s4hana-consumer  --NATS-->  sap-s4hana-producer  --POST-->  mock-sap
//
// with the real binaries, the real message bus, and the real management
// database. Nothing is stubbed inside the platform; only SAP itself is fake,
// and cmd/mock-sap was already written for exactly that.
//
// It self-skips unless SAP_E2E_TEST is set, matching the other integration
// tests here, so a plain `go test ./...` never touches Docker. It runs on a
// schedule rather than per-PR (.github/workflows/e2e-nightly.yml): several
// processes starting up and talking over a bus is the classic source of a
// flaky test, and a false alarm that blocks merges is worse than no test —
// once it has proven stable, promote it.
//
// Bring the world up with:
//
//	docker compose up -d nats postgres-management management-api \
//	    mock-sap sap-s4hana-consumer sap-s4hana-producer
const (
	// connections.tenant_id is a plain VARCHAR with no foreign key (migration
	// 000001), so no tenants row is needed — and seeding one would drag in the
	// users table it references, which has nothing to do with what this tests.
	e2eTenant = "e2e-tenant"
	// The id column IS a uuid, so this one has to look like one.
	e2eConnection = "22222222-2222-2222-2222-222222222222"
)

func sapE2EEnv(t *testing.T) (natsURL, dbURL, mockURL string) {
	t.Helper()
	if os.Getenv("SAP_E2E_TEST") == "" {
		t.Skip("SAP_E2E_TEST not set; skipping the runtime end-to-end")
	}
	return envOr("E2E_NATS_URL", "nats://nats:4222"),
		envOr("E2E_DB_URL", "postgres://postgres:management_password@postgres-management:5432/management_db?sslmode=disable"),
		envOr("E2E_MOCK_SAP_URL", "http://mock-sap:8099")
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// TestSAP_EndToEnd_RecordReachesTheDestination is the whole point: put nothing
// in by hand, start the connection, and assert SAP was written to.
func TestSAP_EndToEnd_RecordReachesTheDestination(t *testing.T) {
	natsURL, dbURL, mockURL := sapE2EEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("open management db: %v", err)
	}
	defer db.Close()
	waitForDB(ctx, t, db)

	// A clean slate on the mock, so a previous run's records cannot make this
	// one pass.
	resetMock(ctx, t, mockURL)

	seedConnection(ctx, t, db, mockURL)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM connections WHERE id = $1`, e2eConnection)
	})

	nc, err := nats.Connect(natsURL, nats.Timeout(10*time.Second))
	if err != nil {
		t.Fatalf("connect nats at %s: %v", natsURL, err)
	}
	// Registered FIRST so it runs LAST: t.Cleanup is LIFO, and the stop command
	// below has to go out before the connection closes. `defer nc.Close()` here
	// would close it before any cleanup ran — which it did in the first version
	// of this test, leaving the pipeline polling the vendor every 2s forever
	// after the test "passed".
	t.Cleanup(func() { nc.Close() })

	// This is the only thing the test does to the platform: the same command
	// the management API publishes when someone clicks Deploy.
	cmd, _ := json.Marshal(map[string]string{
		"connection_id": e2eConnection,
		"tenant_id":     e2eTenant,
	})
	subject := fmt.Sprintf("vrsky.commands.%s.connection.start", e2eTenant)
	if err := nc.Publish(subject, cmd); err != nil {
		t.Fatalf("publish start command: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	// Stop the poller when the test ends. Without this the connector keeps
	// fetching and writing indefinitely — harmless in CI, where everything is
	// torn down, and decidedly not harmless against a real vendor API.
	t.Cleanup(func() {
		stop, _ := json.Marshal(map[string]string{"connection_id": e2eConnection, "tenant_id": e2eTenant})
		if err := nc.Publish(fmt.Sprintf("vrsky.commands.%s.connection.stop", e2eTenant), stop); err != nil {
			t.Errorf("could not stop the connection (%v); it is still polling", err)
			return
		}
		if err := nc.Flush(); err != nil {
			t.Errorf("could not flush the stop command (%v); the connection may still be polling", err)
		}
	})

	// The consumer polls, publishes onto NATS, the producer picks it up and
	// writes back. Generous: a cold start plus one poll interval plus the
	// producer's own subscribe.
	var got []json.RawMessage
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		got = fetchReceived(ctx, t, mockURL)
		if len(got) > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}

	if len(got) == 0 {
		t.Fatalf("nothing was written to SAP within 90s.\n\n" +
			"The record should have travelled: mock-sap -> sap-s4hana-consumer -> NATS -> " +
			"sap-s4hana-producer -> mock-sap. Check the logs of both connectors; a config the " +
			"consumer cannot parse is logged at Debug and drops the connection silently.\n" +
			"  docker compose logs sap-s4hana-consumer sap-s4hana-producer")
	}

	// What arrived must be the records the mock served, not something the
	// pipeline invented. mock-sap's page 1 carries SalesOrder 5001 and 5002.
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal received: %v", err)
	}
	for _, want := range []string{"5001", "SalesOrderType"} {
		if !containsToken(string(body), want) {
			t.Errorf("the write to SAP does not contain %q — the pipeline delivered something, but not "+
				"the records the source served.\ngot: %s", want, body)
		}
	}
}

// TestSAP_EndToEnd_MockRecordsWrites guards the assertion mechanism itself. A
// recorder that silently stopped recording would make the test above pass for
// the wrong reason — it asserts on absence-then-presence, so a broken recorder
// reads as "nothing arrived", which at least fails loudly. This checks the
// other direction: that a write really is observable.
func TestSAP_EndToEnd_MockRecordsWrites(t *testing.T) {
	_, _, mockURL := sapE2EEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resetMock(ctx, t, mockURL)

	// The mock demands the CSRF handshake, exactly as SAP does.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, mockURL+"/probe", nil)
	req.Header.Set("X-CSRF-Token", "Fetch")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("csrf fetch: %v", err)
	}
	token := resp.Header.Get("X-CSRF-Token")
	resp.Body.Close()
	if token == "" {
		t.Fatal("mock-sap returned no CSRF token")
	}

	post, _ := http.NewRequestWithContext(ctx, http.MethodPost, mockURL+"/probe",
		jsonBody(`{"SalesOrder":"probe-1"}`))
	post.Header.Set("X-CSRF-Token", token)
	post.Header.Set("Content-Type", "application/json")
	pr, err := http.DefaultClient.Do(post)
	if err != nil {
		t.Fatalf("probe write: %v", err)
	}
	pr.Body.Close()

	got := fetchReceived(ctx, t, mockURL)
	if len(got) != 1 || !containsToken(string(got[0]), "probe-1") {
		t.Fatalf("mock-sap did not record the write; the end-to-end assertion would be meaningless. got: %v", got)
	}
	resetMock(ctx, t, mockURL)
}

// --- helpers ---

func waitForDB(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if err := db.PingContext(ctx); err == nil {
			// The connections table is created by the management-api's
			// migrations on startup, so being able to connect is not enough.
			var n int
			if err := db.QueryRowContext(ctx,
				`SELECT count(*) FROM information_schema.tables WHERE table_name = 'connections'`).Scan(&n); err == nil && n == 1 {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("management database never became ready with a migrated schema; is management-api up?")
}

// seedConnection writes the one row the platform needs: a connection whose
// source is SAP (reading from the mock) and whose destination is SAP (writing
// back to it). Nothing else is inserted — no secrets, since the mock accepts
// any credentials, which keeps this test free of the credential handling that
// is not what it is testing.
func seedConnection(ctx context.Context, t *testing.T, db *sql.DB, mockURL string) {
	t.Helper()

	nodes := fmt.Sprintf(`[
      {"id":"src","type":"consumer","config":{"type":"sap_s4hana","sap_s4hana":{
        "api_base_url":%q,"entity_set":"A_SalesOrder","odata_version":"v2",
        "auth_type":"basic","username":"e2e","password":"e2e","poll_interval_seconds":2}}},
      {"id":"dst","type":"producer","config":{"type":"sap_s4hana","sap_s4hana":{
        "api_base_url":%q,"entity_set":"A_SalesOrder",
        "auth_type":"basic","username":"e2e","password":"e2e"}}}
    ]`, mockURL, mockURL)

	if _, err := db.ExecContext(ctx,
		`INSERT INTO connections (id, tenant_id, name, status, nodes, edges)
		 VALUES ($1, $2, 'sap e2e', 'running', $3::jsonb, '[]'::jsonb)
		 ON CONFLICT (id) DO UPDATE SET nodes = EXCLUDED.nodes, status = 'running'`,
		e2eConnection, e2eTenant, nodes); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

func resetMock(ctx context.Context, t *testing.T, mockURL string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, mockURL+"/__test/received", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("reset mock-sap at %s: %v (is it up?)", mockURL, err)
	}
	resp.Body.Close()
}

func fetchReceived(ctx context.Context, t *testing.T, mockURL string) []json.RawMessage {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, mockURL+"/__test/received", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil // mid-restart; the caller retries
	}
	defer resp.Body.Close()
	var out []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil
	}
	return out
}

func jsonBody(s string) io.Reader { return strings.NewReader(s) }

func containsToken(haystack, needle string) bool { return strings.Contains(haystack, needle) }
