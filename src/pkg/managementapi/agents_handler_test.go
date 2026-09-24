package managementapi

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// agentRBACMock is the RBAC mock plus an in-memory AgentStore. The store scopes
// by the tenant it is GIVEN, the way the SQL does, so the handler tests below
// check the thing only the handler can get wrong: which tenant it passes. That
// the SQL itself scopes by tenant is checked separately, against the real query
// strings (TestAgentRepo_*), and by make lint-tenant.
type agentRBACMock struct {
	*rbacMock
	mu     sync.Mutex
	agents map[string]*Agent // agentID → agent
	tokens []*AgentRegistrationToken
}

func newAgentRBACMock() *agentRBACMock {
	return &agentRBACMock{rbacMock: newRBACMock(), agents: map[string]*Agent{}}
}

func (m *agentRBACMock) CreateAgentRegistrationToken(_ context.Context, tenantID, suggested, _ string) (*AgentRegistrationToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := &AgentRegistrationToken{ID: "tok-" + tenantID, TenantID: tenantID, Token: AgentRegistrationTokenPrefix + "raw",
		SuggestedName: suggested, ExpiresAt: time.Now().Add(AgentRegistrationTokenTTL)}
	m.tokens = append(m.tokens, t)
	return t, nil
}

func (m *agentRBACMock) ListAgents(_ context.Context, tenantID string) ([]*Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Agent{}
	for _, a := range m.agents {
		if a.TenantID == tenantID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m *agentRBACMock) GetAgent(_ context.Context, tenantID, id string) (*Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.agents[id]; ok && a.TenantID == tenantID {
		return a, nil
	}
	return nil, ErrAgentNotFound
}

func (m *agentRBACMock) RenameAgent(_ context.Context, tenantID, id, name string) (*Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok || a.TenantID != tenantID || a.RevokedAt != nil {
		return nil, ErrAgentNotFound
	}
	for _, o := range m.agents {
		if o.ID != id && o.TenantID == tenantID && o.RevokedAt == nil && strings.EqualFold(o.Name, name) {
			return nil, ErrAgentNameTaken
		}
	}
	a.Name = name
	return a, nil
}

func (m *agentRBACMock) RevokeAgent(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok || a.TenantID != tenantID || a.RevokedAt != nil {
		return ErrAgentNotFound
	}
	now := time.Now()
	a.RevokedAt = &now
	return nil
}

func decodeAgentData(t *testing.T, body []byte, v any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("decode data: %v (%s)", err, env.Data)
	}
}

func TestAgents_CreateRegistrationToken_ReturnsRawOnce(t *testing.T) {
	repo := newAgentRBACMock()
	repo.addUserSession("tokAdmin", "user-A", tenantA, "admin")
	h := NewHandler(repo, NewValidator())

	w := runHTTPAs(t, h, http.MethodPost, "/api/v1/agents/registration-tokens", tenantA, "tokAdmin",
		map[string]string{"suggested_name": "  LAGER-SERVER-01  "})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d (%s)", w.Code, w.Body.String())
	}
	var tok AgentRegistrationToken
	decodeAgentData(t, w.Body.Bytes(), &tok)
	if !strings.HasPrefix(tok.Token, AgentRegistrationTokenPrefix) {
		t.Errorf("token = %q, want the %s prefix", tok.Token, AgentRegistrationTokenPrefix)
	}
	if tok.SuggestedName != "LAGER-SERVER-01" {
		t.Errorf("suggested name = %q, want it trimmed", tok.SuggestedName)
	}
	if tok.TenantID != tenantA {
		t.Errorf("token minted for tenant %q, want the caller's %q", tok.TenantID, tenantA)
	}

	// Nothing that lists agents may carry a token.
	w = runHTTPAs(t, h, http.MethodGet, "/api/v1/agents", tenantA, "tokAdmin", nil)
	if strings.Contains(w.Body.String(), AgentRegistrationTokenPrefix) {
		t.Errorf("agent list exposes a registration token: %s", w.Body.String())
	}
}

// Minting a token creates the means to write into this workspace's pipelines,
// so it is admin-only. An editor must be refused.
func TestAgents_CreateRegistrationToken_RequiresAdmin(t *testing.T) {
	repo := newAgentRBACMock()
	repo.addUserSession("tokEditor", "user-E", tenantA, "editor")
	h := NewHandler(repo, NewValidator())

	w := runHTTPAs(t, h, http.MethodPost, "/api/v1/agents/registration-tokens", tenantA, "tokEditor", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("editor minting a token: want 403, got %d (%s)", w.Code, w.Body.String())
	}
	if len(repo.tokens) != 0 {
		t.Errorf("a token was minted despite the refusal: %+v", repo.tokens)
	}
}

func TestAgents_Revoke_SecondCall404(t *testing.T) {
	repo := newAgentRBACMock()
	repo.addUserSession("tokAdmin", "user-A", tenantA, "admin")
	repo.agents["agent-1"] = &Agent{ID: "agent-1", TenantID: tenantA, Name: "pc"}
	h := NewHandler(repo, NewValidator())

	if w := runHTTPAs(t, h, http.MethodDelete, "/api/v1/agents/agent-1", tenantA, "tokAdmin", nil); w.Code != http.StatusNoContent {
		t.Fatalf("first revoke: want 204, got %d (%s)", w.Code, w.Body.String())
	}
	if w := runHTTPAs(t, h, http.MethodDelete, "/api/v1/agents/agent-1", tenantA, "tokAdmin", nil); w.Code != http.StatusNotFound {
		t.Fatalf("second revoke: want 404, got %d", w.Code)
	}
}

func TestAgents_Rename_ConflictOnDuplicateLiveName(t *testing.T) {
	repo := newAgentRBACMock()
	repo.addUserSession("tokEditor", "user-E", tenantA, "editor")
	repo.agents["a1"] = &Agent{ID: "a1", TenantID: tenantA, Name: "till-1"}
	repo.agents["a2"] = &Agent{ID: "a2", TenantID: tenantA, Name: "till-2"}
	h := NewHandler(repo, NewValidator())

	w := runHTTPAs(t, h, http.MethodPatch, "/api/v1/agents/a2", tenantA, "tokEditor", map[string]string{"name": "TILL-1"})
	if w.Code != http.StatusConflict {
		t.Fatalf("rename onto a live name: want 409, got %d (%s)", w.Code, w.Body.String())
	}
	w = runHTTPAs(t, h, http.MethodPatch, "/api/v1/agents/a2", tenantA, "tokEditor", map[string]string{"name": "   "})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("blank name: want 400, got %d", w.Code)
	}
	w = runHTTPAs(t, h, http.MethodPatch, "/api/v1/agents/a2", tenantA, "tokEditor", map[string]string{"name": "lager"})
	if w.Code != http.StatusOK {
		t.Fatalf("valid rename: want 200, got %d (%s)", w.Code, w.Body.String())
	}
}

// TestIsolation_AgentsAreTenantScoped is the security case for the management
// side. A member of tenant B, using B's own header, must not be able to see,
// rename or revoke tenant A's agent, and gets the same 404 as for an ID that
// does not exist.
func TestIsolation_AgentsAreTenantScoped(t *testing.T) {
	repo := newAgentRBACMock()
	repo.addUserSession("tokB", "user-B", tenantB, "admin")
	repo.agents["agent-A"] = &Agent{ID: "agent-A", TenantID: tenantA, Name: "A's warehouse PC"}
	h := NewHandler(repo, NewValidator())

	w := runHTTPAs(t, h, http.MethodGet, "/api/v1/agents", tenantB, "tokB", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("B list: want 200, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "agent-A") {
		t.Fatalf("B's list leaks A's agent: %s", w.Body.String())
	}

	w = runHTTPAs(t, h, http.MethodPatch, "/api/v1/agents/agent-A", tenantB, "tokB", map[string]string{"name": "pwned"})
	if w.Code != http.StatusNotFound {
		t.Errorf("B renaming A's agent: want 404, got %d", w.Code)
	}
	w = runHTTPAs(t, h, http.MethodDelete, "/api/v1/agents/agent-A", tenantB, "tokB", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("B revoking A's agent: want 404, got %d", w.Code)
	}
	if a := repo.agents["agent-A"]; a.Name != "A's warehouse PC" || a.RevokedAt != nil {
		t.Errorf("A's agent was modified by B: %+v", a)
	}

	// B spoofing X-Tenant-ID to A — which B is not a member of — is stopped
	// by the membership middleware before any handler runs.
	w = runHTTPAs(t, h, http.MethodDelete, "/api/v1/agents/agent-A", tenantA, "tokB", nil)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Errorf("B spoofing A's header to revoke: want 401/403, got %d", w.Code)
	}
	if repo.agents["agent-A"].RevokedAt != nil {
		t.Error("A's agent was revoked via a spoofed header")
	}
}

// --- Repository SQL (the real query strings, via sqlmock) ---

// The tenant must reach the SQL as the first argument of every read and write.
// sqlmock rejects the call if the arguments differ, so a query that dropped or
// reordered the tenant would fail here.
func TestAgentRepo_EveryQueryIsScopedByTenant(t *testing.T) {
	repo, mock, done := newRepoMock(t)
	defer done()
	ctx := context.Background()
	cols := []string{"id", "tenant_id", "name", "hostname", "os", "arch", "agent_version",
		"directories", "last_seen_at", "registered_at", "revoked_at", "online"}
	row := []driver.Value{"a1", tenantA, "pc", "host", "windows", "amd64", "1.0.0",
		[]byte(`[{"name":"inbox","mode":"read"}]`), nil, time.Now(), nil, false}

	mock.ExpectQuery(`FROM agents\s+WHERE tenant_id = \$1\s+ORDER BY`).
		WithArgs(tenantA, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows(cols).AddRow(row...))
	list, err := repo.ListAgents(ctx, tenantA)
	if err != nil || len(list) != 1 || list[0].Directories[0].Name != "inbox" {
		t.Fatalf("ListAgents = %+v, %v", list, err)
	}

	mock.ExpectQuery(`FROM agents\s+WHERE tenant_id = \$1 AND id::text = \$3`).
		WithArgs(tenantA, sqlmock.AnyArg(), "a1").
		WillReturnRows(sqlmock.NewRows(cols).AddRow(row...))
	if _, err := repo.GetAgent(ctx, tenantA, "a1"); err != nil {
		t.Fatalf("GetAgent: %v", err)
	}

	mock.ExpectExec(`UPDATE agents SET revoked_at = NOW\(\)\s+WHERE tenant_id = \$1 AND id::text = \$2 AND revoked_at IS NULL`).
		WithArgs(tenantA, "a1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.RevokeAgent(ctx, tenantA, "a1"); err != nil {
		t.Fatalf("RevokeAgent: %v", err)
	}

	mock.ExpectExec(`UPDATE agents SET name = \$3\s+WHERE tenant_id = \$1 AND id::text = \$2 AND revoked_at IS NULL`).
		WithArgs(tenantA, "a1", "lager").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM agents\s+WHERE tenant_id = \$1 AND id::text = \$3`).
		WithArgs(tenantA, sqlmock.AnyArg(), "a1").
		WillReturnRows(sqlmock.NewRows(cols).AddRow(row...))
	if _, err := repo.RenameAgent(ctx, tenantA, "a1", "lager"); err != nil {
		t.Fatalf("RenameAgent: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

// captureArg is a sqlmock argument matcher that records what it was given.
type captureArg struct{ got *driver.Value }

func (c captureArg) Match(v driver.Value) bool { *c.got = v; return true }

// The raw token must never reach the database — only its hash. Captures the
// value actually bound to token_hash and checks it is auth.HashToken of the
// token handed back, and not the token itself.
func TestAgentRepo_StoresOnlyTheTokenHash(t *testing.T) {
	repo, mock, done := newRepoMock(t)
	defer done()

	var bound driver.Value
	mock.ExpectQuery(`INSERT INTO agent_registration_tokens`).
		WithArgs(tenantA, captureArg{&bound}, nil, nil, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "suggested_name", "expires_at"}).
			AddRow("t1", tenantA, nil, time.Now().Add(time.Hour)))

	tok, err := repo.CreateAgentRegistrationToken(context.Background(), tenantA, "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(tok.Token) != len(AgentRegistrationTokenPrefix)+64 {
		t.Errorf("token %q: want prefix + 64 hex chars", tok.Token)
	}
	if bound == tok.Token {
		t.Fatal("the raw token was written to the database")
	}
	if bound != auth.HashToken(tok.Token) {
		t.Errorf("bound token_hash = %v, want auth.HashToken(token)", bound)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

// Revoking or renaming something that isn't there — including another
// tenant's agent, which the scoped UPDATE simply does not match — is
// ErrAgentNotFound, not success.
func TestAgentRepo_NoRowsIsNotFound(t *testing.T) {
	repo, mock, done := newRepoMock(t)
	defer done()

	mock.ExpectExec(`UPDATE agents SET revoked_at`).WithArgs(tenantB, "a1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	if err := repo.RevokeAgent(context.Background(), tenantB, "a1"); err != ErrAgentNotFound {
		t.Errorf("revoke with no matching row: got %v, want ErrAgentNotFound", err)
	}
	mock.ExpectExec(`UPDATE agents SET name`).WithArgs(tenantB, "a1", "x").
		WillReturnResult(sqlmock.NewResult(0, 0))
	if _, err := repo.RenameAgent(context.Background(), tenantB, "a1", "x"); err != ErrAgentNotFound {
		t.Errorf("rename with no matching row: got %v, want ErrAgentNotFound", err)
	}
}
