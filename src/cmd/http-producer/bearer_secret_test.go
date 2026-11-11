package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// Tests for #850: a static bearer token kept as a tenant secret. The
// Postgres-backed ones store the secret the way the management API does (a
// row in secrets, encrypted with the master key) and let the producer read it
// through crypto.NewSQLSecretReader, the path it uses in production. They
// skip without VRSKY_TEST_POSTGRES_URL.

// fakeToken is an obviously fake bearer token. Nothing real is ever used here.
const fakeToken = "test-bearer-0f1e2d3c4b5a-not-a-real-token"

// bearerPG is a fresh management database with the master key set.
type bearerPG struct {
	db  *sql.DB
	key string
}

func newBearerPG(t *testing.T) *bearerPG {
	t.Helper()
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	key := hex.EncodeToString(k)
	t.Setenv(crypto.KeyEnvVar, key)
	return &bearerPG{db: db, key: key}
}

// tenant creates a tenant and returns its id.
func (b *bearerPG) tenant(t *testing.T) string {
	t.Helper()
	var owner, tenant string
	if err := b.db.QueryRow(`INSERT INTO users (email, password_hash, status)
		VALUES ('bearer-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := b.db.QueryRow(`INSERT INTO tenants (name, slug, owner_id)
		VALUES ('bearer', 'bearer-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	return tenant
}

// secret stores plaintext as a secret of tenant, encrypted as the management
// API stores it, and returns its id.
func (b *bearerPG) secret(t *testing.T, tenant, plaintext string) string {
	t.Helper()
	ct, err := crypto.Encrypt(plaintext, b.key)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := b.db.QueryRow(`INSERT INTO secrets (tenant_id, name, ciphertext) VALUES ($1, $2, $3) RETURNING id`,
		tenant, "bearer-"+uuid.NewString(), ct).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// connection stores a connection of tenant with one HTTP producer node whose
// http block is httpBlock, and returns its id.
func (b *bearerPG) connection(t *testing.T, tenant, httpBlock string) string {
	t.Helper()
	id := uuid.NewString()
	nodes := `[{"id":"dst","type":"producer","config":{"type":"http","http":` + httpBlock + `}}]`
	if _, err := b.db.Exec(`INSERT INTO connections (id, tenant_id, name, status, nodes, edges)
		VALUES ($1, $2, $3, 'running', $4::jsonb, '[]'::jsonb)`, id, tenant, "bearer-"+id, nodes); err != nil {
		t.Fatal(err)
	}
	return id
}

// authRecorder is a target that records the Authorization header of every
// request and answers with status, and with body(r) when body is not nil.
type authRecorder struct {
	srv   *httptest.Server
	mu    sync.Mutex
	auths []string
}

func newAuthRecorder(t *testing.T, status int, body func(r *http.Request) string) *authRecorder {
	t.Helper()
	a := &authRecorder{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.auths = append(a.auths, r.Header.Get("Authorization"))
		a.mu.Unlock()
		w.WriteHeader(status)
		if body != nil {
			_, _ = io.WriteString(w, body(r))
		}
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *authRecorder) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.auths...)
}

// newBearerProducer builds the producer on db as Configure would, logging to
// logs, with loopback allowed for the httptest target.
func newBearerProducer(db *sql.DB, logs io.Writer) *httpProducer {
	return &httpProducer{
		db:              db,
		logger:          slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		allowPrivate:    true,
		configCache:     map[string][]*HTTPConfig{},
		configCacheTime: map[string]time.Time{},
		configCacheTTL:  time.Minute,
		eventSubs:       map[string][]chan HTTPEvent{},
		recentEvents:    map[string][]HTTPEvent{},
	}
}

func bearerEnv(tenant, connID string) *envelope.Envelope {
	env := envelope.New()
	env.IntegrationID = connID
	env.TenantID = tenant
	env.ContentType = "application/json"
	env.Payload = []byte(`{"sale":1}`)
	return env
}

// The owner's question on #850: does a {secret:<id>} placeholder in an
// Authorization header already send a stored bearer? It does. The producer
// resolves its whole node config with crypto.ResolveSecretsInJSON, which
// expands a placeholder in any string. auth_type "bearer" is still the way
// to do it (docs/connectors/http.md says why); this test records the fact.
func TestBearer_PlaceholderInAuthorizationHeaderIsExpanded(t *testing.T) {
	pg := newBearerPG(t)
	tenant := pg.tenant(t)
	secretID := pg.secret(t, tenant, fakeToken)
	target := newAuthRecorder(t, http.StatusOK, nil)
	connID := pg.connection(t, tenant,
		`{"url":"`+target.srv.URL+`","method":"POST","headers":{"Authorization":"Bearer {secret:`+secretID+`}"}}`)

	p := newBearerProducer(pg.db, io.Discard)
	if err := p.Deliver(context.Background(), bearerEnv(tenant, connID)); err != nil {
		t.Fatalf("Deliver = %v, want nil", err)
	}
	got := target.seen()
	if len(got) != 1 || got[0] != "Bearer "+fakeToken {
		t.Fatalf("target saw Authorization %q, want the stored token expanded into the header", got)
	}
}

// A node with auth_type "bearer" sends the token from its stored secret, read
// for the envelope's tenant, as Authorization: Bearer. It replaces an
// Authorization header the node also sets.
func TestBearer_StoredTokenIsSentAsAuthorization(t *testing.T) {
	pg := newBearerPG(t)
	tenant := pg.tenant(t)
	secretID := pg.secret(t, tenant, fakeToken)
	target := newAuthRecorder(t, http.StatusAccepted, nil)
	connID := pg.connection(t, tenant, `{"url":"`+target.srv.URL+`","method":"POST",`+
		`"headers":{"Authorization":"Basic not-this-one"},"auth_type":"bearer","token_secret_id":"`+secretID+`"}`)

	p := newBearerProducer(pg.db, io.Discard)
	if err := p.Deliver(context.Background(), bearerEnv(tenant, connID)); err != nil {
		t.Fatalf("Deliver = %v, want nil", err)
	}
	if got := target.seen(); len(got) != 1 || got[0] != "Bearer "+fakeToken {
		t.Fatalf("target saw Authorization %q, want the stored bearer token", got)
	}
}

// The secret is read for the envelope's tenant only. A node that names
// another tenant's secret gets nothing: the read fails, the message is held
// (retried, then the DLQ), and no request is sent.
func TestBearer_AnotherTenantsSecretIsNotResolved(t *testing.T) {
	pg := newBearerPG(t)
	owner, other := pg.tenant(t), pg.tenant(t)
	secretID := pg.secret(t, owner, fakeToken)
	target := newAuthRecorder(t, http.StatusOK, nil)
	connID := pg.connection(t, other, `{"url":"`+target.srv.URL+`","auth_type":"bearer","token_secret_id":"`+secretID+`"}`)

	var logs syncBuffer
	p := newBearerProducer(pg.db, &logs)
	err := p.Deliver(context.Background(), bearerEnv(other, connID))
	if !sdk.IsConfigUnavailable(err) {
		t.Fatalf("Deliver = %v, want a config read failure, so the message is held, not acked", err)
	}
	if got := target.seen(); len(got) != 0 {
		t.Fatalf("target got %d requests with another tenant's secret named, want none", len(got))
	}
	assertNoToken(t, "another tenant's secret", p, connID, &logs, err)
}

// A token stored with a trailing newline (pasted) is sent trimmed, not as an
// invalid header that fails every request.
func TestBearer_StoredTokenIsTrimmed(t *testing.T) {
	pg := newBearerPG(t)
	tenant := pg.tenant(t)
	secretID := pg.secret(t, tenant, fakeToken+"\n")
	target := newAuthRecorder(t, http.StatusOK, nil)
	connID := pg.connection(t, tenant, `{"url":"`+target.srv.URL+`","auth_type":"bearer","token_secret_id":"`+secretID+`"}`)

	p := newBearerProducer(pg.db, io.Discard)
	if err := p.Deliver(context.Background(), bearerEnv(tenant, connID)); err != nil {
		t.Fatalf("Deliver = %v, want nil", err)
	}
	if got := target.seen(); len(got) != 1 || got[0] != "Bearer "+fakeToken {
		t.Fatalf("target saw Authorization %q, want the token without the newline", got)
	}
}
