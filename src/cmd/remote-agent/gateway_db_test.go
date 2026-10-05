package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
	"github.com/ValueRetail/vrsky/pkg/auth"

	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// The gateway against a real, migrated Postgres: what sqlmock cannot show.
// sqlmock checks the SQL text and arguments; only a real database shows that
// the conditions in that SQL actually hold — that two registrations racing on
// one token cannot both win, and that the tenant condition on the agent lookup
// really excludes another workspace's agent.
//
//	VRSKY_TEST_POSTGRES_URL=postgres://postgres:…@localhost:5432/postgres?sslmode=disable \
//	  go test ./cmd/remote-agent -run TestGatewayDB -v
//
// It runs in a database of its own (pkg/testdb) and is skipped without one.
func TestGatewayDB_RegistrationAndTenantBoundary(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
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

// The registration limit against the real statements: only a real database
// shows that a token found valid is what hands the attempt back, and that a
// request refused by the limit left its token unused.
func TestGatewayDB_OnlyFailedRegistrationsCountAgainstTheLimit(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	now := time.Now()
	g := newGateway()
	g.db = db
	g.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	g.now = func() time.Time { return now }

	var owner, tenant string
	if err := db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('gw-limit-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO tenants (name, slug, owner_id)
		VALUES ('gw-limit', 'gw-limit-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(&tenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	mint := func() string {
		raw := agentproto.RegTokenPrefix + "gwlimit-" + randHex(t)
		if _, err := db.ExecContext(ctx, `INSERT INTO agent_registration_tokens (tenant_id, token_hash, expires_at)
			VALUES ($1, $2, NOW() + interval '1 hour')`, tenant, auth.HashToken(raw)); err != nil {
			t.Fatalf("mint: %v", err)
		}
		return raw
	}
	// Every request comes from the same address, as a shop's tills do.
	register := func(token, name string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(agentproto.RegisterRequest{RegistrationToken: token, Name: name, Hostname: "h"})
		req := httptest.NewRequest(http.MethodPost, "/agent/v1/register", bytes.NewReader(body))
		req.Header.Set("X-Real-IP", "203.0.113.9")
		rec := httptest.NewRecorder()
		g.agentRoutes().ServeHTTP(rec, req)
		return rec
	}

	// --- A rollout: three times the allowance, all genuine, none refused. ---
	for i := 0; i < 3*defaultRegisterMaxFailures; i++ {
		if rec := register(mint(), fmt.Sprintf("till-%02d", i)); rec.Code != http.StatusCreated {
			t.Fatalf("genuine registration %d from one address: want 201, got %d %s", i+1, rec.Code, rec.Body.String())
		}
	}

	// --- A valid token with a taken name is a mistake, not a failed attempt. ---
	clash := mint()
	for i := 0; i < 2*defaultRegisterMaxFailures; i++ {
		if rec := register(clash, "TILL-00"); rec.Code != http.StatusConflict {
			t.Fatalf("name clash %d: want 409, got %d %s", i+1, rec.Code, rec.Body.String())
		}
	}

	// --- After all that the address still has its whole allowance, and no more. ---
	for i := 0; i < defaultRegisterMaxFailures; i++ {
		if rec := register(agentproto.RegTokenPrefix+"never-minted", "x"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failed attempt %d: want 401, got %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if rec := register(agentproto.RegTokenPrefix+"never-minted", "x"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt past the allowance: want 429, got %d %s", rec.Code, rec.Body.String())
	}

	// --- While blocked, a valid token is refused too, and is NOT used up. ---
	if rec := register(clash, "till-late"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("valid token from a blocked address: want 429, got %d %s", rec.Code, rec.Body.String())
	}
	now = now.Add(registerRefillEvery)
	if rec := register(clash, "till-late"); rec.Code != http.StatusCreated {
		t.Fatalf("the refused token should still work once the block eases: %d %s", rec.Code, rec.Body.String())
	}
	var agents int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM agents WHERE tenant_id = $1`, tenant).Scan(&agents)
	if want := 3*defaultRegisterMaxFailures + 1; agents != want {
		t.Fatalf("tenant has %d agents, want %d", agents, want)
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
