package managementapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Every audited request that authenticated records who made it, and owner
// writes under /api/v1/tenants/{tenant_id}/* land in that workspace's log
// (#695, backlog 168 and 169). All cases go through AuditMiddleware and the
// real routes (serveAudited).

const billKey = "vrsky_shop_0123456789abcdef0123456789abcdef"

func newAuditActorEnv(t *testing.T) *billingEnv {
	t.Helper()
	e := newAuditBillingEnv(t)
	e.repo.addAPIKey(billKey, billTenant)
	e.repo.apiKeys[hashOf(billKey)].Slug = "shop"
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
			map[string]any{"name": ""}, "api_key", "", "API key (shop)"},
		{"API key sends data to another workspace (ingestion route)", http.MethodPost, ingest, billTenant, billKey,
			map[string]any{"x": 1}, "api_key", "", "API key (shop)"},
		{"owner asks for a plan (session, /tenants route)", http.MethodPost, "/api/v1/tenants/" + billTenant + "/plan-requests", "", "tokOwner",
			map[string]any{"plan": PlanPaid}, "user", "owner", "owner@example.com"},
		{"refused editor keeps the actor", http.MethodPost, "/api/v1/tenants/" + billTenant + "/plan-requests", "", "tokEditor",
			map[string]any{"plan": PlanPaid}, "user", "editor", "editor@example.com"},
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
// refused request puts nothing in the target workspace's log. With no
// workspace selected (production) nothing is written; with one selected it
// stays under that one, as before.
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
			got := aw.wait(t, 1)
			if len(got) != 1 || got[0].TenantID != billTenantB {
				t.Fatalf("want the refusal once under the caller's workspace %q, got %+v", billTenantB, got)
			}
		})
	}
}

// Which workspace an entry is filed under when more than one is known: the
// route's explicit path override (AuditUnderPathTenant), then the workspace
// TenantMemberMiddleware let the caller into, then the context tenant.
func TestAuditBag_TenantPrecedence(t *testing.T) {
	cases := []struct {
		name, override, member, context, want string
	}{
		{"context only", "", "", "ctx", "ctx"},
		{"member over context", "", "member", "ctx", "member"},
		{"override over member and context", "path", "member", "ctx", "path"},
		{"override over context", "path", "", "ctx", "path"},
		{"member with no context", "", "member", "", "member"},
		{"nothing", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &auditBag{details: map[string]any{}, tenantOverride: c.override, memberTenant: c.member}
			if got := b.tenant(c.context); got != c.want {
				t.Fatalf("tenant() = %q, want %q", got, c.want)
			}
		})
	}
}
