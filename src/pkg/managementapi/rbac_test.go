package managementapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// rbacMock is a focused MockRepository extension that lets us script the
// session / API-key / role lookups RequireTenantRoleFromHeader makes
// without depending on a real DB.
type rbacMock struct {
	*MockRepository
	sessions map[string]*sessionForToken // tokenHash → (session, user)
	roles    map[string]string           // userID|tenantID → role
	apiKeys  map[string]*Tenant          // tokenHash → tenant
}

type sessionForToken struct {
	session *Session
	user    *User
}

func newRBACMock() *rbacMock {
	return &rbacMock{
		MockRepository: NewMockRepository(),
		sessions:       map[string]*sessionForToken{},
		roles:          map[string]string{},
		apiKeys:        map[string]*Tenant{},
	}
}

func (m *rbacMock) addUserSession(rawToken, userID, tenantID, role string) {
	hash := auth.HashToken(rawToken)
	u := &User{ID: userID, Email: userID + "@example.com", Status: UserStatusActive, EmailVerified: true}
	s := &Session{ID: "sess-" + userID, UserID: userID, TokenHash: hash, ExpiresAt: time.Now().Add(1 * time.Hour), IsActive: true}
	m.sessions[hash] = &sessionForToken{session: s, user: u}
	if role != "" {
		m.roles[userID+"|"+tenantID] = role
	}
}

func (m *rbacMock) addAPIKey(rawToken, tenantID string) {
	hash := auth.HashToken(rawToken)
	m.apiKeys[hash] = &Tenant{ID: tenantID, Status: "active"}
}

func (m *rbacMock) ValidateSession(ctx context.Context, tokenHash string) (*Session, *User, error) {
	entry, ok := m.sessions[tokenHash]
	if !ok {
		return nil, nil, ErrUnauthorized()
	}
	return entry.session, entry.user, nil
}

func (m *rbacMock) GetUserTenantRole(ctx context.Context, userID, tenantID string) (string, error) {
	return m.roles[userID+"|"+tenantID], nil
}

func (m *rbacMock) GetTenantByAPIKeyHash(ctx context.Context, keyHash string) (*Tenant, error) {
	t, ok := m.apiKeys[keyHash]
	if !ok {
		return nil, ErrTenantNotFound
	}
	return t, nil
}

// ErrUnauthorized is a small helper so the test mock doesn't depend on
// production error types.
func ErrUnauthorized() error { return &UnauthorizedError{Message: "no session"} }

func runRBAC(t *testing.T, repo Repository, method, path, tenantID, bearer, minRole string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if tenantID != "" {
		req = req.WithContext(ContextWithTenantID(req.Context(), tenantID))
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	final := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(http.StatusOK) })
	RequireTenantRoleFromHeader(repo, minRole)(final).ServeHTTP(w, req)
	return w.Code
}

func TestRBAC_ViewerCannotMutate(t *testing.T) {
	repo := newRBACMock()
	repo.addUserSession("viewer-token", "user-v", "tenant-A", "viewer")

	got := runRBAC(t, repo, http.MethodPost, "/api/v1/connections", "tenant-A", "viewer-token", "editor")
	if got != http.StatusForbidden {
		t.Fatalf("viewer should get 403, got %d", got)
	}
}

func TestRBAC_EditorCanWrite(t *testing.T) {
	repo := newRBACMock()
	repo.addUserSession("editor-token", "user-e", "tenant-A", "editor")

	got := runRBAC(t, repo, http.MethodPost, "/api/v1/connections", "tenant-A", "editor-token", "editor")
	if got != http.StatusOK {
		t.Fatalf("editor should get 200, got %d", got)
	}
}

func TestRBAC_EditorCannotDelete(t *testing.T) {
	repo := newRBACMock()
	repo.addUserSession("editor-token", "user-e", "tenant-A", "editor")

	got := runRBAC(t, repo, http.MethodDelete, "/api/v1/connections/abc", "tenant-A", "editor-token", "admin")
	if got != http.StatusForbidden {
		t.Fatalf("editor should be denied admin route, got %d", got)
	}
}

func TestRBAC_AdminCanDelete(t *testing.T) {
	repo := newRBACMock()
	repo.addUserSession("admin-token", "user-a", "tenant-A", "admin")

	got := runRBAC(t, repo, http.MethodDelete, "/api/v1/connections/abc", "tenant-A", "admin-token", "admin")
	if got != http.StatusOK {
		t.Fatalf("admin should pass admin gate, got %d", got)
	}
}

func TestRBAC_APIKeyCountsAsAdmin(t *testing.T) {
	repo := newRBACMock()
	repo.addAPIKey("api-key-token", "tenant-A")

	got := runRBAC(t, repo, http.MethodPost, "/api/v1/connections", "tenant-A", "api-key-token", "editor")
	if got != http.StatusOK {
		t.Fatalf("api key should pass editor gate, got %d", got)
	}
	got = runRBAC(t, repo, http.MethodDelete, "/api/v1/connections/abc", "tenant-A", "api-key-token", "admin")
	if got != http.StatusOK {
		t.Fatalf("api key should pass admin gate, got %d", got)
	}
	got = runRBAC(t, repo, http.MethodDelete, "/api/v1/tenants/x", "tenant-A", "api-key-token", "owner")
	if got != http.StatusForbidden {
		t.Fatalf("api key must NOT pass owner gate, got %d", got)
	}
}

func TestRBAC_NoCredentialsIs401(t *testing.T) {
	repo := newRBACMock()
	got := runRBAC(t, repo, http.MethodPost, "/api/v1/connections", "tenant-A", "" /* no token */, "editor")
	if got != http.StatusUnauthorized {
		t.Fatalf("anon should get 401, got %d", got)
	}
}

func TestRBAC_NoTenantHeaderIs400(t *testing.T) {
	repo := newRBACMock()
	got := runRBAC(t, repo, http.MethodPost, "/api/v1/connections", "" /* no tenant */, "anything", "editor")
	if got != http.StatusBadRequest {
		t.Fatalf("missing tenant should get 400, got %d", got)
	}
}

func TestRBAC_SessionForDifferentTenantIs401(t *testing.T) {
	repo := newRBACMock()
	repo.addUserSession("token", "user-x", "tenant-OTHER", "owner")
	got := runRBAC(t, repo, http.MethodPost, "/api/v1/connections", "tenant-A", "token", "editor")
	if got != http.StatusUnauthorized {
		t.Fatalf("session valid but not a member of the target tenant should be 401, got %d", got)
	}
}

func TestRBAC_RoleHierarchy(t *testing.T) {
	// Cross-check the constants we documented.
	if roleHierarchy["viewer"] >= roleHierarchy["editor"] {
		t.Fatalf("viewer should rank below editor")
	}
	if roleHierarchy["editor"] >= roleHierarchy["admin"] {
		t.Fatalf("editor should rank below admin")
	}
	if roleHierarchy["admin"] >= roleHierarchy["owner"] {
		t.Fatalf("admin should rank below owner")
	}
}
