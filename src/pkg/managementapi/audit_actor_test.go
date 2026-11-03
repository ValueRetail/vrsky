package managementapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Every audited request that authenticated records who made it, and owner
// writes under /api/v1/tenants/{tenant_id}/* land in that workspace's log
// (#695, backlog 168 and 169). All cases go through AuditMiddleware and the
// real routes (serveAudited).

// billKey is billTenant's API key. Its owner's slug ("acme") is not in the
// token, so a label test cannot pass by parsing the token.
const (
	billKey     = "vrsky_shop_0123456789abcdef0123456789abcdef"
	inactiveKey = "vrsky_gone_0123456789abcdef0123456789abcdef"
)

func newAuditActorEnv(t *testing.T) *billingEnv {
	t.Helper()
	e := newAuditBillingEnv(t)
	e.repo.addAPIKey(billKey, billTenant)
	e.repo.apiKeys[hashOf(billKey)].Slug = "acme"
	e.repo.addAPIKey(inactiveKey, billTenant)
	e.repo.apiKeys[hashOf(inactiveKey)].Slug = "acme"
	e.repo.apiKeys[hashOf(inactiveKey)].Status = "terminating"
	e.repo.addUserSession("tokViewer", "viewer", billTenant, "viewer")
	return e
}

// entryJSON is the entry as it would be stored and listed, for checks that
// a value appears nowhere in it.
func entryJSON(t *testing.T, en *AuditEntry) string {
	t.Helper()
	b, err := json.Marshal(en)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAudit_EntryRecordsTheActor(t *testing.T) {
	ingest := "/api/v1/tenant/" + billTenantB + "/data"
	cases := []struct {
		name, method, path, workspace, bearer string
		body                                  any
		wantKind, wantUser, wantLabel         string // wantUser "" = no user_id
	}{
		{"editor writes a connection (session, header route)", http.MethodPost, "/api/v1/connections", billTenant, "tokEditor",
			map[string]any{"name": ""}, "user", "editor", "editor@example.com"},
		{"owner rotates a secret (session, header route)", http.MethodPost, "/api/v1/secrets/s1/rotate", billTenant, "tokOwner",
			map[string]any{}, "user", "owner", "owner@example.com"},
		{"API key writes a connection (header route)", http.MethodPost, "/api/v1/connections", billTenant, billKey,
			map[string]any{"name": ""}, "api_key", "", "API key (acme)"},
		{"API key sends data to another workspace (ingestion route)", http.MethodPost, ingest, billTenant, billKey,
			map[string]any{"x": 1}, "api_key", "", "API key (acme)"},
		{"owner asks for a plan (session, /tenants route)", http.MethodPost, "/api/v1/tenants/" + billTenant + "/plan-requests", "", "tokOwner",
			map[string]any{"plan": PlanPaid}, "user", "owner", "owner@example.com"},
		{"refused editor keeps the actor", http.MethodPost, "/api/v1/tenants/" + billTenant + "/plan-requests", "", "tokEditor",
			map[string]any{"plan": PlanPaid}, "user", "editor", "editor@example.com"},
		{"refused viewer keeps the actor (header route)", http.MethodPost, "/api/v1/connections", billTenant, "tokViewer",
			map[string]any{"name": ""}, "user", "viewer", "viewer@example.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAuditActorEnv(t)
			aw := &fakeAuditWriter{}
			rec := serveAudited(t, e.h, aw, c.method, c.path, c.workspace, c.bearer, c.body)
			got := aw.wait(t, 1)
			if len(got) != 1 {
				t.Fatalf("want one entry, got %d: %+v (status %d)", len(got), got, rec.Code)
			}
			en := got[0]
			if en.TenantID != billTenant {
				t.Errorf("filed under %q, want the caller's verified workspace %q", en.TenantID, billTenant)
			}
			if en.ActorKind != c.wantKind {
				t.Errorf("actor_kind %q, want %q", en.ActorKind, c.wantKind)
			}
			if c.wantUser == "" {
				if en.UserID != nil {
					t.Errorf("user_id %q, want none for an API key", *en.UserID)
				}
			} else if en.UserID == nil || *en.UserID != c.wantUser {
				t.Errorf("user_id %v, want %q", en.UserID, c.wantUser)
			}
			if en.ActorLabel != c.wantLabel {
				t.Errorf("actor_label %q, want %q", en.ActorLabel, c.wantLabel)
			}
			// Neither the key nor its hash may be stored anywhere in the entry.
			js := entryJSON(t, en)
			for _, secret := range []string{billKey, hashOf(billKey)} {
				if strings.Contains(js, secret) {
					t.Errorf("entry carries key material: %s", js)
				}
			}
		})
	}
}

// An owner's writes under /api/v1/tenants/{tenant_id}/* are filed under that
// workspace with the owner as actor. Production puts no workspace in the
// context for these paths (TenantIDMiddleware skips them); a selected
// workspace, even another one, must not take the entry either.
func TestAudit_OwnerTenantWritesFileUnderPathTenant(t *testing.T) {
	routes := []struct {
		name, method, path, wantAction string
		body                           any
		wantStatus                     int
	}{
		{"plan request", http.MethodPost, "/api/v1/tenants/" + billTenant + "/plan-requests", "plan.request", map[string]any{"plan": PlanPaid}, http.StatusCreated},
		{"API-key rotation", http.MethodPost, "/api/v1/tenants/" + billTenant + "/api-key/rotate", "tenant.rotate", nil, http.StatusOK},
		{"member role change", http.MethodPut, "/api/v1/tenants/" + billTenant + "/members/editor", "member.role_change", map[string]any{"role": "viewer"}, http.StatusNoContent},
	}
	for _, rt := range routes {
		for _, workspace := range []string{"", billTenantB} {
			t.Run(rt.name+", workspace "+workspace, func(t *testing.T) {
				e := newAuditActorEnv(t)
				aw := &fakeAuditWriter{}
				rec := serveAudited(t, e.h, aw, rt.method, rt.path, workspace, "tokOwner", rt.body)
				if rec.Code != rt.wantStatus {
					t.Fatalf("status %d, want %d: %s", rec.Code, rt.wantStatus, rec.Body.String())
				}
				got := aw.wait(t, 1)
				if len(got) != 1 {
					t.Fatalf("want one entry, got %d: %+v", len(got), got)
				}
				en := got[0]
				if en.TenantID != billTenant {
					t.Errorf("filed under %q, want the path's %q", en.TenantID, billTenant)
				}
				if en.UserID == nil || *en.UserID != "owner" || en.ActorLabel != "owner@example.com" {
					t.Errorf("actor (%v, %q), want the owner", en.UserID, en.ActorLabel)
				}
				if en.Action != rt.wantAction {
					t.Errorf("action %q, want %q", en.Action, rt.wantAction)
				}
				// The rotated key is in the response once and nowhere else.
				var resp struct {
					RawKey string `json:"raw_key"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &resp)
				if resp.RawKey != "" && strings.Contains(entryJSON(t, en), resp.RawKey) {
					t.Errorf("entry carries the new key: %+v", en)
				}
			})
		}
	}
}

// The tenant is recorded only after the membership check: a non-member's
// refused request puts nothing in the target workspace's log. A workspace in
// the context does not change that: the caller authenticated, and was not
// verified against it either.
func TestAudit_NonMemberCannotFileIntoTenantsPath(t *testing.T) {
	paths := []struct {
		name, method, path string
		body               any
	}{
		{"plan request", http.MethodPost, "/api/v1/tenants/" + billTenant + "/plan-requests", map[string]any{"plan": PlanPaid}},
		{"API-key rotation", http.MethodPost, "/api/v1/tenants/" + billTenant + "/api-key/rotate", nil},
		{"member add", http.MethodPost, "/api/v1/tenants/" + billTenant + "/members", map[string]any{"email": "x@example.com", "role": "owner"}},
		{"workspace delete", http.MethodDelete, "/api/v1/tenants/" + billTenant, nil},
	}
	for _, p := range paths {
		t.Run(p.name+", no workspace", func(t *testing.T) {
			e := newAuditActorEnv(t)
			aw := &fakeAuditWriter{}
			rec := serveAudited(t, e.h, aw, p.method, p.path, "", "tokOwnerB", p.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body.String())
			}
			if got := aw.settle(); len(got) != 0 {
				t.Fatalf("a refused non-member filed entries: %+v", got)
			}
		})
		t.Run(p.name+", own workspace selected", func(t *testing.T) {
			e := newAuditActorEnv(t)
			aw := &fakeAuditWriter{}
			rec := serveAudited(t, e.h, aw, p.method, p.path, billTenantB, "tokOwnerB", p.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body.String())
			}
			if got := aw.settle(); len(got) != 0 {
				t.Fatalf("a refused non-member filed entries: %+v", got)
			}
		})
	}
}

// A caller who authenticated but was not verified against the workspace in
// X-Tenant-ID must not file entries into its log, refused or not. The
// review of #697 found these probes.
func TestAudit_UnverifiedCallerFilesNothingIntoHeaderTenant(t *testing.T) {
	cases := []struct {
		name, method, path, workspace, bearer string
		body                                  any
		wantStatus                            int
	}{
		{"session non-member on a header route", http.MethodPost, "/api/v1/connections", billTenant, "tokOwnerB",
			map[string]any{"name": ""}, http.StatusUnauthorized},
		{"API key with another workspace's header", http.MethodPost, "/api/v1/connections", billTenantB, billKey,
			map[string]any{"name": ""}, http.StatusUnauthorized},
		{"non-operator on the platform plan route with a foreign header", http.MethodPut, "/api/v1/platform/tenants/" + billTenantB + "/plan", opsWorkspace, "tokOwnerB",
			map[string]string{"plan": PlanPaid}, http.StatusForbidden},
		{"non-operator on the platform plan route with own header", http.MethodPut, "/api/v1/platform/tenants/" + billTenant + "/plan", billTenantB, "tokOwnerB",
			map[string]string{"plan": PlanPaid}, http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAuditActorEnv(t)
			aw := &fakeAuditWriter{}
			rec := serveAudited(t, e.h, aw, c.method, c.path, c.workspace, c.bearer, c.body)
			if rec.Code != c.wantStatus {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.wantStatus, rec.Body.String())
			}
			if got := aw.settle(); len(got) != 0 {
				t.Fatalf("an unverified caller filed entries: %+v", got)
			}
		})
	}
}

// Data ingestion files under the key's own workspace, whatever X-Tenant-ID
// says.
func TestAudit_IngestionFilesUnderTheKeysWorkspace(t *testing.T) {
	e := newAuditActorEnv(t)
	aw := &fakeAuditWriter{}
	serveAudited(t, e.h, aw, http.MethodPost, "/api/v1/tenant/"+opsWorkspace+"/data", billTenantB, billKey, map[string]any{"x": 1})
	got := aw.wait(t, 1)
	if len(got) != 1 || got[0].TenantID != billTenant || got[0].ActorKind != "api_key" {
		t.Fatalf("want one api_key entry under the key's workspace %q, got %+v", billTenant, got)
	}
}

// A member of a workspace that no longer exists gets a 404 and no entry: the
// tenant is recorded after the tenant lookup too, not only the role check.
func TestAudit_MemberOfMissingTenantFilesNothing(t *testing.T) {
	const ghost = "44444444-4444-4444-4444-444444444444"
	e := newAuditActorEnv(t)
	e.repo.roles["owner|"+ghost] = "owner"
	aw := &fakeAuditWriter{}
	rec := serveAudited(t, e.h, aw, http.MethodPost, "/api/v1/tenants/"+ghost+"/plan-requests", "", "tokOwner", map[string]any{"plan": PlanPaid})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if got := aw.settle(); len(got) != 0 {
		t.Fatalf("a request for a missing workspace filed entries: %+v", got)
	}
}

// An inactive key is not an actor: the request is treated as anonymous.
func TestAudit_InactiveKeyIsNotRecorded(t *testing.T) {
	for _, path := range []string{"/api/v1/connections", "/api/v1/tenant/" + billTenantB + "/data"} {
		t.Run(path, func(t *testing.T) {
			e := newAuditActorEnv(t)
			aw := &fakeAuditWriter{}
			rec := serveAudited(t, e.h, aw, http.MethodPost, path, billTenant, inactiveKey, map[string]any{"name": ""})
			if rec.Code < 400 {
				t.Fatalf("status %d, want a refusal", rec.Code)
			}
			// Anonymous refusals keep today's filing under the context
			// tenant (backlog 176), so there is one entry to inspect.
			for _, en := range aw.wait(t, 1) {
				if en.ActorKind == "api_key" || en.ActorLabel != "" || en.UserID != nil {
					t.Fatalf("inactive key recorded as actor: %+v", en)
				}
			}
		})
	}
}

// Accepting an invite is filed in the joined workspace's log, not the one
// selected in X-Tenant-ID.
func TestAudit_InviteAcceptFilesUnderJoinedWorkspace(t *testing.T) {
	repo := &inviteSessionRepo{inviteRepo: newInviteRepo(), user: &User{ID: "joiner", Email: "joiner@example.com", Status: UserStatusActive, EmailVerified: true}}
	inv, err := repo.CreateInvite(context.Background(), billTenant, "joiner@example.com", "viewer", "owner")
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(repo, NewValidator())
	aw := &fakeAuditWriter{}
	rec := serveAudited(t, h, aw, http.MethodPost, "/api/v1/invites/accept", billTenantB, "tokJoiner", map[string]string{"token": inv.Token})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got := aw.wait(t, 1)
	if len(got) != 1 || got[0].TenantID != billTenant || got[0].Action != "member.invite.accept" ||
		got[0].UserID == nil || *got[0].UserID != "joiner" {
		t.Fatalf("want member.invite.accept by joiner under %q, got %+v", billTenant, got)
	}
}

// inviteSessionRepo is inviteRepo with one valid session, so the accept
// route runs behind the real session middleware.
type inviteSessionRepo struct {
	*inviteRepo
	user *User
}

func (r *inviteSessionRepo) ValidateSession(_ context.Context, _ string) (*Session, *User, error) {
	return &Session{ID: "s-joiner", UserID: r.user.ID, IsActive: true, ExpiresAt: time.Now().Add(time.Hour)}, r.user, nil
}

// Which workspace an entry is filed under: the route's explicit path
// override (AuditUnderPathTenant), then the workspace the caller was verified
// against, then nothing for an authenticated caller, then (anonymous only)
// the context tenant.
func TestAuditBag_TenantPrecedence(t *testing.T) {
	cases := []struct {
		name, override, verified, context, want string
		actor                                   bool
	}{
		{"anonymous, context only", "", "", "ctx", "ctx", false},
		{"verified over context", "", "verified", "ctx", "verified", true},
		{"override over verified and context", "path", "verified", "ctx", "path", true},
		{"override over context", "path", "", "ctx", "path", true},
		{"verified with no context", "", "verified", "", "verified", true},
		{"authenticated but unverified: not the context", "", "", "ctx", "", true},
		{"nothing", "", "", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &auditBag{details: map[string]any{}, tenantOverride: c.override, verifiedTenant: c.verified}
			if c.actor {
				b.actor = &auditActor{kind: "user", userID: "u", label: "u@example.com"}
			}
			if got := b.tenant(c.context); got != c.want {
				t.Fatalf("tenant() = %q, want %q", got, c.want)
			}
		})
	}
}
