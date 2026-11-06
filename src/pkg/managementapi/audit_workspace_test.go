package managementapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// Creating a workspace is filed in the new workspace's log with the creator
// as actor, and no request without a valid credential is filed in any
// workspace's log, whatever X-Tenant-ID says (#719, backlog 185 and 186).
// All cases go through AuditMiddleware and the real routes (serveAudited).

// newWorkspace is the id the repository gives a workspace created in a test.
const newWorkspace = "55555555-5555-5555-5555-555555555555"

// createTenantRepo is the audit test repository with a CreateTenant that
// returns a workspace id of its own (MockRepository's is fixed and shared),
// or err when set.
type createTenantRepo struct {
	*rbacMock
	err error
}

func (r *createTenantRepo) CreateTenant(_ context.Context, userID, name, slug string) (*Tenant, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &Tenant{ID: newWorkspace, Name: name, Slug: slug, OwnerID: userID, SubscriptionPlan: PlanTrial, Status: "active"}, nil
}

func newCreateTenantHandler(t *testing.T, err error) *Handler {
	t.Helper()
	return NewHandler(&createTenantRepo{rbacMock: newAuditActorEnv(t).repo, err: err}, NewValidator())
}

func TestAudit_WorkspaceCreationFiledUnderNewWorkspace(t *testing.T) {
	// No workspace in the context is what production does for /tenants; a
	// selected one (the creator's own or another) must not take the entry.
	for _, workspace := range []string{"", billTenant, billTenantB} {
		t.Run("workspace "+workspace, func(t *testing.T) {
			h := newCreateTenantHandler(t, nil)
			aw := &fakeAuditWriter{}
			rec := serveAudited(t, h, aw, http.MethodPost, "/api/v1/tenants", workspace, "tokOwner", map[string]string{"name": "New shop"})
			if rec.Code != http.StatusCreated {
				t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body.String())
			}
			got := aw.wait(t, 1)
			if len(got) != 1 {
				t.Fatalf("want one entry, got %d: %+v", len(got), got)
			}
			en := got[0]
			if en.TenantID != newWorkspace {
				t.Errorf("filed under %q, want the new workspace %q", en.TenantID, newWorkspace)
			}
			if en.ActorKind != "user" || en.UserID == nil || *en.UserID != "owner" || en.ActorLabel != "owner@example.com" {
				t.Errorf("actor (%q, %v, %q), want the creator", en.ActorKind, en.UserID, en.ActorLabel)
			}
			if en.Action != "tenant.create" || en.ResourceType != "tenant" || en.ResourceID != newWorkspace {
				t.Errorf("action %q on %s/%s, want tenant.create on tenant/%s", en.Action, en.ResourceType, en.ResourceID, newWorkspace)
			}
			if en.StatusCode != http.StatusCreated {
				t.Errorf("status_code %d, want 201", en.StatusCode)
			}
		})
	}
}

// A creation that did not happen has no workspace to file it under, and the
// selected one does not take it.
func TestAudit_RefusedWorkspaceCreationFilesNothing(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		body       any
		wantStatus int
	}{
		{"name too short", nil, map[string]string{"name": "ab"}, http.StatusBadRequest},
		{"slug taken", ErrSlugAlreadyExists, map[string]string{"name": "New shop"}, http.StatusConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newCreateTenantHandler(t, c.err)
			aw := &fakeAuditWriter{}
			rec := serveAudited(t, h, aw, http.MethodPost, "/api/v1/tenants", billTenant, "tokOwner", c.body)
			if rec.Code != c.wantStatus {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.wantStatus, rec.Body.String())
			}
			if got := aw.settle(); len(got) != 0 {
				t.Fatalf("a refused creation filed entries: %+v", got)
			}
		})
	}
}

// Sign-up creates the user's first workspace; that creation is in the new
// workspace's log too, with the new user as actor.
func TestAudit_SignUpFilesWorkspaceCreationUnderNewWorkspace(t *testing.T) {
	for _, workspace := range []string{"", billTenant} {
		t.Run("workspace "+workspace, func(t *testing.T) {
			h := newCreateTenantHandler(t, nil)
			aw := &fakeAuditWriter{}
			rec := serveAudited(t, h, aw, http.MethodPost, "/api/v1/auth/register", workspace, "",
				map[string]string{"email": "new@example.com", "password": "correct-horse-battery", "full_name": "New User", "workspace_name": "New shop"})
			if rec.Code != http.StatusCreated {
				t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body.String())
			}
			var resp struct {
				UserID string `json:"user_id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.UserID == "" {
				t.Fatalf("no user_id in the response (%v): %s", err, rec.Body.String())
			}
			got := aw.wait(t, 1)
			if len(got) != 1 {
				t.Fatalf("want one entry, got %d: %+v", len(got), got)
			}
			en := got[0]
			if en.TenantID != newWorkspace {
				t.Errorf("filed under %q, want the new workspace %q", en.TenantID, newWorkspace)
			}
			if en.ActorKind != "user" || en.UserID == nil || *en.UserID != resp.UserID || en.ActorLabel != "new@example.com" {
				t.Errorf("actor (%q, %v, %q), want the new user %s", en.ActorKind, en.UserID, en.ActorLabel, resp.UserID)
			}
			if en.Action != "tenant.create" || en.ResourceID != newWorkspace || en.Details["via"] != "registration" {
				t.Errorf("action %q on %s, details %v; want tenant.create on the new workspace via registration", en.Action, en.ResourceID, en.Details)
			}
		})
	}
}

// A sign-up whose workspace could not be created has nothing to file.
func TestAudit_SignUpWithoutWorkspaceFilesNothing(t *testing.T) {
	h := newCreateTenantHandler(t, ErrSlugAlreadyExists)
	aw := &fakeAuditWriter{}
	rec := serveAudited(t, h, aw, http.MethodPost, "/api/v1/auth/register", billTenant, "",
		map[string]string{"email": "new@example.com", "password": "correct-horse-battery", "full_name": "New User", "workspace_name": "New shop"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if got := aw.settle(); len(got) != 0 {
		t.Fatalf("a sign-up without a workspace filed entries: %+v", got)
	}
}

// No credential, a bad one or an inactive key: nothing is written to the
// workspace in the context, refused or not. Production puts no workspace in
// the context for /auth, /tenants and the alerts webhook; doing it here
// anyway is the harder case.
func TestAudit_AnonymousRequestFilesNothing(t *testing.T) {
	const alertsToken = "alerts-test-token"
	t.Setenv("ALERTS_WEBHOOK_TOKEN", alertsToken)
	cases := []struct {
		name, method, path, bearer string
		body                       any
		wantStatus                 int // 0 = any
	}{
		{"no credential, header route", http.MethodPost, "/api/v1/connections", "", map[string]any{"name": ""}, http.StatusUnauthorized},
		{"unknown session, header route", http.MethodDelete, "/api/v1/agents/a1", "tokNobody", nil, http.StatusUnauthorized},
		{"unknown session, secret read", http.MethodGet, "/api/v1/secrets", "tokNobody", nil, http.StatusUnauthorized},
		{"inactive API key, header route", http.MethodPost, "/api/v1/connections", inactiveKey, map[string]any{"name": ""}, http.StatusUnauthorized},
		{"no credential, workspace create", http.MethodPost, "/api/v1/tenants", "", map[string]string{"name": "New shop"}, http.StatusUnauthorized},
		{"no credential, unknown route", http.MethodPost, "/api/v1/nothing-here", "", map[string]any{}, http.StatusNotFound},
		{"failed login", http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": "owner@example.com", "password": "wrong-password"}, http.StatusUnauthorized},
		{"forgot password", http.MethodPost, "/api/v1/auth/forgot-password", "", map[string]string{"email": "owner@example.com"}, 0},
		{"alerts webhook that passes", http.MethodPost, "/api/v1/alerts/webhook", alertsToken, map[string]any{"alerts": []any{}}, http.StatusOK},
	}
	for _, c := range cases {
		for _, workspace := range []string{billTenant, billTenantB} {
			t.Run(c.name+", workspace "+workspace, func(t *testing.T) {
				e := newAuditActorEnv(t)
				aw := &fakeAuditWriter{}
				rec := serveAudited(t, e.h, aw, c.method, c.path, workspace, c.bearer, c.body)
				if c.wantStatus != 0 && rec.Code != c.wantStatus {
					t.Fatalf("status %d, want %d: %s", rec.Code, c.wantStatus, rec.Body.String())
				}
				if got := aw.settle(); len(got) != 0 {
					t.Fatalf("an anonymous request filed entries: %+v", got)
				}
			})
		}
	}
}
