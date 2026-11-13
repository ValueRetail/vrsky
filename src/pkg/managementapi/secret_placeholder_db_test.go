package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// Against a real database, with a real stored secret: a {secret:} placeholder
// outside a credential setting is refused on save (create and update), on
// start (for a node stored before the check existed) and by Test Connection,
// and no answer carries the secret's value (#853).
func TestSecretPlaceholderDB_SaveStartAndTestRefuse(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewPostgresRepository(db)

	var owner string
	if err := db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('ph-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	ten, err := repo.CreateTenant(ctx, owner, "Shop", "ph-"+time.Now().Format("150405.000"))
	if err != nil {
		t.Fatal(err)
	}
	const key = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const value = "the-partner-password-nobody-may-read"
	t.Setenv(crypto.KeyEnvVar, key)
	ct, err := crypto.Encrypt(value, key)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := repo.CreateSecret(ctx, ten.ID, "nats-password", ct)
	if err != nil {
		t.Fatal(err)
	}

	// No worker answers: Test Connection must refuse before it calls one.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Test Connection called the worker")
	}))
	defer dead.Close()
	t.Setenv("TEST_URL_NATS", dead.URL)

	h := NewHandler(repo, NewValidator())
	h.SetDB(db)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/connections", h.CreateConnection)
	mux.HandleFunc("PUT /api/v1/connections/{id}", h.UpdateConnection)
	mux.HandleFunc("POST /api/v1/connections/{id}/start", h.StartConnection)
	mux.HandleFunc("POST /api/v1/connections/test", h.TestConnection)
	do := func(method, path, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(ContextWithTenantID(req.Context(), ten.ID))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if strings.Contains(rr.Body.String(), value) {
			t.Fatalf("%s %s answered with the secret: %s", method, path, rr.Body.String())
		}
		return rr.Code, rr.Body.String()
	}
	natsBlock := func(extra string) string {
		return `{"servers":["tls://nats.example.com:4222"],"stream":"S","durable":"d","auth_type":"user_password",` +
			`"username":"u","password_secret_id":"` + sec.ID + `"` + extra + `}`
	}
	pipeline := func(block string) string {
		return `{"name":"rh","nodes":[` +
			`{"id":"src","type":"consumer","config":{"type":"nats","nats":` + block + `}},` +
			`{"id":"dst","type":"producer","config":{"type":"http","http":{"url":"https://example.com/in"}}}],` +
			`"edges":[{"id":"e","source":"src","target":"dst"}]}`
	}
	ph := `"{secret:` + sec.ID + `}"`

	// Save: create.
	code, body := do(http.MethodPost, "/api/v1/connections", pipeline(natsBlock(`,"deliver_policy":`+ph)))
	if code != http.StatusBadRequest || !strings.Contains(body, "node src: nats.deliver_policy holds a {secret:…} placeholder") {
		t.Fatalf("create: %d %s", code, body)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM connections WHERE tenant_id = $1`, ten.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("connections after a refused create: %d (%v)", n, err)
	}

	// A credential reference saves.
	code, body = do(http.MethodPost, "/api/v1/connections", pipeline(natsBlock("")))
	if code != http.StatusCreated {
		t.Fatalf("create with a credential reference: %d %s", code, body)
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.Data.ID == "" {
		t.Fatalf("created: %s (%v)", body, err)
	}
	id := created.Data.ID

	// Save: update.
	code, body = do(http.MethodPut, "/api/v1/connections/"+id, pipeline(natsBlock(`,"api_prefix":`+ph)))
	if code != http.StatusBadRequest || !strings.Contains(body, "nats.api_prefix holds") {
		t.Fatalf("update: %d %s", code, body)
	}

	// Start: a node stored before this check, written straight to the row.
	var nodes json.RawMessage
	if err := db.QueryRowContext(ctx, `SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`, id, ten.ID).Scan(&nodes); err != nil {
		t.Fatal(err)
	}
	stale := strings.Replace(string(nodes), `"auth_type": "user_password"`, `"auth_type": `+ph, 1)
	if stale == string(nodes) {
		stale = strings.Replace(string(nodes), `"auth_type":"user_password"`, `"auth_type":`+ph, 1)
	}
	if stale == string(nodes) {
		t.Fatalf("could not plant the placeholder in %s", nodes)
	}
	if _, err := db.ExecContext(ctx, `UPDATE connections SET nodes = $1 WHERE id = $2 AND tenant_id = $3`, stale, id, ten.ID); err != nil {
		t.Fatal(err)
	}
	code, body = do(http.MethodPost, "/api/v1/connections/"+id+"/start", "")
	if code != http.StatusBadRequest || !strings.Contains(body, "nats.auth_type holds a {secret:…} placeholder") {
		t.Fatalf("start: %d %s", code, body)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM connections WHERE id = $1 AND tenant_id = $2`, id, ten.ID).Scan(&status); err != nil || status == "running" {
		t.Fatalf("status after a refused start: %q (%v)", status, err)
	}

	// Test Connection, with the session's tenant, which holds the secret.
	code, body = do(http.MethodPost, "/api/v1/connections/test", `{"type":"nats","role":"consumer","nats":`+natsBlock(`,"inbox_prefix":`+ph)+`}`)
	if code != http.StatusOK || !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "nats.inbox_prefix holds") {
		t.Fatalf("test connection: %d %s", code, body)
	}
}
