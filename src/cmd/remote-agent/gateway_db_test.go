package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
	"github.com/ValueRetail/vrsky/pkg/auth"
)

// The gateway against a real, migrated Postgres: what sqlmock cannot show.
// sqlmock checks the SQL text and arguments; only a real database shows that
// the conditions in that SQL actually hold — that two registrations racing on
// one token cannot both win, and that the tenant condition on the agent lookup
// really excludes another workspace's agent.
//
//	MGMT_TEST_DB_URL=postgres://postgres:x@127.0.0.1:55432/m?sslmode=disable \
//	  go test ./cmd/remote-agent -run TestGatewayDB -v
//
// Skipped when unset, like pkg/managementapi's database tests.
func TestGatewayDB_RegistrationAndTenantBoundary(t *testing.T) {
	dsn := os.Getenv("MGMT_TEST_DB_URL")
	if dsn == "" {
		t.Skip("set MGMT_TEST_DB_URL (a migrated database) to run the gateway database test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	g := newGateway()
	g.db = db
	g.logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	var owner, t1, t2 string
	if err := db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('gw-test-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for _, dst := range []*string{&t1, &t2} {
		if err := db.QueryRowContext(ctx, `INSERT INTO tenants (name, slug, owner_id)
			VALUES ('gw-test', 'gw-test-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(dst); err != nil {
			t.Fatalf("seed tenant: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM tenants WHERE id IN ($1, $2)`, t1, t2) // lint:tenant-ok — test cleanup
		_, _ = db.Exec(`DELETE FROM users WHERE id = $1`, owner)
	})

	mint := func(tenant string) string {
		raw := agentproto.RegTokenPrefix + "gwtest-" + tenant[:8] + "-" + randHex(t)
		if _, err := db.ExecContext(ctx, `INSERT INTO agent_registration_tokens (tenant_id, token_hash, expires_at)
			VALUES ($1, $2, NOW() + interval '1 hour')`, tenant, auth.HashToken(raw)); err != nil {
			t.Fatalf("mint: %v", err)
		}
		return raw
	}
	register := func(token, name string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(agentproto.RegisterRequest{RegistrationToken: token, Name: name, Hostname: "h",
			Directories: []agentproto.Directory{{Name: "out", Mode: "write"}}})
		rec := httptest.NewRecorder()
		g.agentRoutes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent/v1/register", bytes.NewReader(body)))
		return rec
	}

	// --- Two machines race on one token: exactly one registers. ---
	token := mint(t1)
	codes := make([]int, 8)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = register(token, "racer-"+string(rune('a'+i))).Code
		}(i)
	}
	wg.Wait()
	won := 0
	for _, c := range codes {
		if c == http.StatusCreated {
			won++
		} else if c != http.StatusUnauthorized {
			t.Errorf("a losing registration got %d, want 401", c)
		}
	}
	if won != 1 {
		t.Fatalf("%d registrations succeeded on one token, want exactly 1 (codes %v)", won, codes)
	}
	var agentsInT1 int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM agents WHERE tenant_id = $1`, t1).Scan(&agentsInT1)
	if agentsInT1 != 1 {
		t.Fatalf("tenant 1 has %d agents after the race, want 1", agentsInT1)
	}

	// --- The agent is created in the TOKEN's tenant, and its credential works. ---
	rec := register(mint(t2), "t2-machine")
	if rec.Code != http.StatusCreated {
		t.Fatalf("register in t2: %d %s", rec.Code, rec.Body.String())
	}
	var t2Agent agentproto.RegisterResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &t2Agent)
	id, revoked, err := g.dbLookupAgent(ctx, auth.HashToken(t2Agent.Credential))
	if err != nil || revoked || id.TenantID != t2 {
		t.Fatalf("credential resolves to %+v (revoked=%v, err=%v), want tenant 2", id, revoked, err)
	}

	// --- A pipeline in tenant 1 naming tenant 2's agent is refused. ---
	node := remoteNode{NodeID: "out", AgentID: t2Agent.AgentID, Directory: "out"}
	if msg := g.checkAgentNode(ctx, t1, node, agentproto.ModeWrite); msg == "" {
		t.Fatal("tenant 1 was allowed to use tenant 2's agent")
	}
	if msg := g.checkAgentNode(ctx, t2, node, agentproto.ModeWrite); msg != "" {
		t.Fatalf("tenant 2 was refused its own agent: %s", msg)
	}

	// --- A name clash rolls back and leaves the token usable. ---
	clash := mint(t2)
	if rec := register(clash, "T2-MACHINE"); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate name: want 409, got %d %s", rec.Code, rec.Body.String())
	}
	if rec := register(clash, "t2-second"); rec.Code != http.StatusCreated {
		t.Fatalf("the token should still work after a rolled-back clash: %d %s", rec.Code, rec.Body.String())
	}

	// --- Revocation is seen on the very next lookup. ---
	if _, err := db.ExecContext(ctx, `UPDATE agents SET revoked_at = NOW() WHERE id = $1`, t2Agent.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, revoked, _ := g.dbLookupAgent(ctx, auth.HashToken(t2Agent.Credential)); !revoked {
		t.Error("a revoked agent's credential still reads as live")
	}
	if msg := g.checkAgentNode(ctx, t2, node, agentproto.ModeWrite); msg == "" {
		t.Error("a revoked agent can still be used by a pipeline")
	}
	if _, _, err := g.dbLookupAgent(ctx, auth.HashToken(agentproto.CredentialPrefix+"never-issued")); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown credential: err = %v, want sql.ErrNoRows", err)
	}
}

func randHex(t *testing.T) string {
	t.Helper()
	c, err := newCredential()
	if err != nil {
		t.Fatal(err)
	}
	return c[len(agentproto.CredentialPrefix):][:16]
}
