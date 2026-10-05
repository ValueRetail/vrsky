package managementapi

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/auth"

	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// Agent repository against a real, migrated Postgres. The sqlmock tests prove
// the query strings carry the tenant; this proves Postgres actually enforces
// what the schema promises — the scoped UPDATEs miss another tenant's row, the
// partial unique index releases a revoked name, and only the hash is stored.
//
//	VRSKY_TEST_POSTGRES_URL=postgres://postgres:…@localhost:5432/postgres?sslmode=disable \
//	  go test ./pkg/managementapi -run TestAgentRepoDB -v
//
// It runs in a database of its own (pkg/testdb) and is skipped without one.
func TestAgentRepoDB_TenantScopingAndLifecycle(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := &PostgresRepository{db: db}

	// Two tenants, each owned by its own user. The database is this test's
	// alone and is dropped afterwards, so nothing needs cleaning up row by row.
	var tA, tB, owner string
	if err := db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('agent-test-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for _, dst := range []*string{&tA, &tB} {
		if err := db.QueryRowContext(ctx, `INSERT INTO tenants (name, slug, owner_id)
			VALUES ('agent-test', 'agent-test-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(dst); err != nil {
			t.Fatalf("seed tenant: %v", err)
		}
	}
	// A token stores only its hash.
	tok, err := repo.CreateAgentRegistrationToken(ctx, tA, "LAGER-01", nil, owner)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	var storedHash string
	if err := db.QueryRowContext(ctx, `SELECT token_hash FROM agent_registration_tokens WHERE id = $1`, tok.ID).Scan(&storedHash); err != nil {
		t.Fatalf("read token row: %v", err)
	}
	if storedHash != auth.HashToken(tok.Token) {
		t.Errorf("stored token_hash is not auth.HashToken(token)")
	}

	// Seed an agent in A the way registration will (that code lands with the
	// gateway), then drive it through the repository.
	var agentID string
	if err := db.QueryRowContext(ctx, `INSERT INTO agents (tenant_id, name, credential_hash, directories)
		VALUES ($1, 'LAGER-01', encode(sha256(gen_random_uuid()::text::bytea), 'hex'), '[{"name":"inbox","mode":"read"}]')
		RETURNING id`, tA).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// --- Tenant B cannot see or touch it. ---
	if list, err := repo.ListAgents(ctx, tB); err != nil || len(list) != 0 {
		t.Errorf("B's list = %+v, %v; want empty", list, err)
	}
	if _, err := repo.GetAgent(ctx, tB, agentID); !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("B GetAgent(A's): err = %v, want ErrAgentNotFound", err)
	}
	if _, err := repo.RenameAgent(ctx, tB, agentID, "pwned"); !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("B RenameAgent(A's): err = %v, want ErrAgentNotFound", err)
	}
	if err := repo.RevokeAgent(ctx, tB, agentID); !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("B RevokeAgent(A's): err = %v, want ErrAgentNotFound", err)
	}
	// Groups decide which tills a pipeline delivers to: B putting A's agent
	// into one of B's groups would send B's data to A's machine.
	// A sets them first, so that "B's call returned not-found" is not taken
	// for "B's call changed nothing": the row is read back further down.
	if _, err := repo.SetAgentGroups(ctx, tA, agentID, []string{"all-tills"}); err != nil {
		t.Fatalf("A SetAgentGroups: %v", err)
	}
	if _, err := repo.SetAgentGroups(ctx, tB, agentID, []string{"b-tills"}); !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("B SetAgentGroups(A's): err = %v, want ErrAgentNotFound", err)
	}
	if groups, err := repo.ListAgentGroups(ctx, tB); err != nil || len(groups) != 0 {
		t.Errorf("B sees groups %+v (err %v), want none — the groups belong to A's agents", groups, err)
	}
	if groups, err := repo.ListAgentGroups(ctx, tA); err != nil || len(groups) != 1 || groups[0].Name != "all-tills" || groups[0].Members != 1 {
		t.Errorf("A's groups = %+v (err %v), want all-tills with 1 member", groups, err)
	}
	a, err := repo.GetAgent(ctx, tA, agentID)
	if err != nil {
		t.Fatalf("A GetAgent: %v", err)
	}
	if a.Name != "LAGER-01" || a.RevokedAt != nil || len(a.Groups) != 1 || a.Groups[0] != "all-tills" {
		t.Fatalf("A's agent changed by B's calls: %+v", a)
	}
	if len(a.Directories) != 1 || a.Directories[0].Name != "inbox" || a.Directories[0].Mode != "read" {
		t.Errorf("directories = %+v", a.Directories)
	}
	if a.Online {
		t.Error("an agent that has never been seen reports online")
	}

	// A malformed ID is not-found, not a 500 from a uuid cast.
	if _, err := repo.GetAgent(ctx, tA, "not-a-uuid"); !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("GetAgent(malformed id): err = %v, want ErrAgentNotFound", err)
	}

	// --- Name uniqueness among live agents, released on revoke. ---
	var second string
	if err := db.QueryRowContext(ctx, `INSERT INTO agents (tenant_id, name, credential_hash)
		VALUES ($1, 'till-2', encode(sha256(gen_random_uuid()::text::bytea), 'hex')) RETURNING id`, tA).Scan(&second); err != nil {
		t.Fatalf("seed second agent: %v", err)
	}
	if _, err := repo.RenameAgent(ctx, tA, second, "lager-01"); !errors.Is(err, ErrAgentNameTaken) {
		t.Errorf("rename onto a live name (case-insensitive): err = %v, want ErrAgentNameTaken", err)
	}
	if err := repo.RevokeAgent(ctx, tA, agentID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := repo.RevokeAgent(ctx, tA, agentID); !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("second revoke: err = %v, want ErrAgentNotFound", err)
	}
	if _, err := repo.RenameAgent(ctx, tA, second, "LAGER-01"); err != nil {
		t.Errorf("the revoked agent's name should be free again: %v", err)
	}

	// Last-seen drives online.
	if _, err := db.ExecContext(ctx, `UPDATE agents SET last_seen_at = NOW() WHERE id = $1`, second); err != nil {
		t.Fatal(err)
	}
	if a, err := repo.GetAgent(ctx, tA, second); err != nil || !a.Online {
		t.Errorf("recently seen agent: online = %v, err = %v; want online", a != nil && a.Online, err)
	}
}
