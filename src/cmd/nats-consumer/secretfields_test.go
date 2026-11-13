package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// The stored secret an editor tries to read back (#853).
const (
	leakID    = "0f0f0f0f-1111-2222-3333-444444444444"
	leakValue = "write-only-secret-value"
)

// secretsDB is a database where every secret lookup answers leakValue, so a
// resolver that expanded a placeholder it should refuse would show it. nodes,
// when set, is the stored connection.
func secretsDB(t *testing.T, nodes []byte) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	t.Setenv(crypto.KeyEnvVar, testKey)
	ct, err := crypto.Encrypt(leakValue, testKey)
	if err != nil {
		t.Fatal(err)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.MatchExpectationsInOrder(false)
	if nodes != nil {
		mock.ExpectQuery("SELECT nodes FROM connections").WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow(nodes))
	}
	for i := 0; i < 4; i++ {
		mock.ExpectQuery("SELECT ciphertext FROM secrets").WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(ct))
	}
	return db, mock
}

// The reproduction from the issue: Test Connection with a placeholder in a
// setting the connector validates and quotes answered with the secret.
func TestTestConnection_PlaceholderOutsideCredentialsIsRefused(t *testing.T) {
	for _, field := range []string{"auth_type", "deliver_policy", "api_prefix", "inbox_prefix", "stream", "durable"} {
		t.Run(field, func(t *testing.T) {
			db, _ := secretsDB(t, nil)
			var logs bytes.Buffer
			c := newTestConsumer(nil)
			c.logger = slog.New(slog.NewTextHandler(&logs, nil))
			c.db = db
			draft := map[string]any{
				"tenant_id": tTenant, "servers": []string{"tls://127.0.0.1:1"},
				"stream": tStream, "durable": tDur,
			}
			draft[field] = "{secret:" + leakID + "}"
			out := postTest(t, c, draft)
			msg, _ := out["error"].(string)
			if out["ok"] != false || !strings.Contains(msg, "nats."+field+" holds a {secret:…} placeholder") {
				t.Fatalf("answer = %v", out)
			}
			if strings.Contains(msg, leakValue) || strings.Contains(logs.String(), leakValue) {
				t.Fatalf("the secret came back: %v / %s", out, logs.String())
			}
		})
	}
}

// A stray *_secret_id in the block is dropped, never resolved into the
// setting it names. (It used to be dropped by onlyCredentialSecrets here;
// pkg/crypto does it now for every connector.)
func TestTestConnection_StrayReferenceIsDropped(t *testing.T) {
	for _, field := range []string{"auth_type", "stream", "api_prefix"} {
		t.Run(field, func(t *testing.T) {
			db, _ := secretsDB(t, nil)
			c := newTestConsumer(nil)
			c.db = db
			out := postTest(t, c, map[string]any{
				"tenant_id": tTenant, "servers": []string{"tls://127.0.0.1:1"}, "stream": tStream, "durable": tDur,
				field + "_secret_id": leakID,
			})
			if msg, _ := out["error"].(string); out["ok"] != false || strings.Contains(msg, leakValue) {
				t.Fatalf("answer = %v", out)
			}
		})
	}
}

// What resolves in a nats block: the three credentials and the CA, and no
// other reference.
func TestNATSBlockSecrets(t *testing.T) {
	db, _ := secretsDB(t, nil)
	in := []byte(`{"stream":"S","password_secret_id":"a","creds_secret_id":"b","auth_type_secret_id":"c","stream_secret_id":"d","nkey_seed_secret_id":"e","ca_cert_secret_id":"f","api_prefix_secret_id":"g"}`)
	resolved, err := crypto.ResolveBlockSecrets(context.Background(), crypto.NewSQLSecretReader(db), tTenant, "nats", in)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(resolved, &out); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"password", "creds", "nkey_seed", "ca_cert"} {
		if out[k] != leakValue {
			t.Errorf("%s = %v, want the stored secret", k, out[k])
		}
	}
	if out["stream"] != "S" || out["auth_type"] != nil || out["api_prefix"] != nil {
		t.Errorf("resolved = %v", out)
	}
	for k := range out {
		if strings.HasSuffix(k, "_secret_id") {
			t.Errorf("%s survived", k)
		}
	}
}

// The same placeholder in a stored node: the start fails with a status error
// that names the setting, and neither last_error nor the log carries the
// value.
func TestStart_PlaceholderOutsideCredentialsIsAStatusError(t *testing.T) {
	nodes, _ := json.Marshal([]map[string]any{{
		"id": "src", "type": "consumer",
		"config": map[string]any{"type": "nats", "nats": map[string]any{
			"servers": []string{"tls://127.0.0.1:1"}, "stream": tStream, "durable": tDur,
			"deliver_policy": "{secret:" + leakID + "}",
		}},
	}})
	db, mock := secretsDB(t, nodes)
	rec := &statusRec{}
	mock.ExpectExec(`UPDATE connections SET status = \$1, last_error = \$2`).
		WithArgs(argCapture{rec, 0}, argCapture{rec, 1}, argCapture{rec, 2}, argCapture{rec, 3}).
		WillReturnResult(sqlmock.NewResult(0, 1))

	var logs bytes.Buffer
	c := newTestConsumer(nil)
	c.logger = slog.New(slog.NewTextHandler(&logs, nil))
	c.db = db
	c.restoreConnection(context.Background(), tConn, tTenant)
	harness.Eventually(t, wait, "status written", func() bool { _, ok := rec.errorWrite(); return ok })
	reason, _ := rec.errorWrite()
	if !strings.Contains(reason, "nats.deliver_policy holds a {secret:…} placeholder") {
		t.Errorf("last_error = %q", reason)
	}
	if strings.Contains(reason, leakValue) || strings.Contains(logs.String(), leakValue) {
		t.Fatalf("the secret came back: %q / %s", reason, logs.String())
	}
}
