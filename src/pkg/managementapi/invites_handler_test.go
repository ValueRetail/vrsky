package managementapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// inviteRepo is an in-memory Repository (embedding MockRepository) that also
// implements InviteStore, so Handle*Invite can be driven without a database.
type inviteRepo struct {
	*MockRepository
	users   map[string]*User // email(lower) -> user (registered users)
	invites map[string]*TenantInvite
	byToken map[string]*TenantInvite
	added   []string // userIDs added as members
	seq     int
}

func newInviteRepo() *inviteRepo {
	return &inviteRepo{
		MockRepository: NewMockRepository(),
		users:          map[string]*User{},
		invites:        map[string]*TenantInvite{},
		byToken:        map[string]*TenantInvite{},
	}
}

func (r *inviteRepo) GetUserByEmail(_ context.Context, email string) (*User, error) {
	if u, ok := r.users[email]; ok {
		return u, nil
	}
	return nil, auth.ErrUserNotFound
}

func (r *inviteRepo) AddTenantMember(_ context.Context, _ /*tenantID*/, userID, _ /*role*/ string) error {
	r.added = append(r.added, userID)
	return nil
}

func (r *inviteRepo) CreateInvite(_ context.Context, tenantID, email, role, invitedBy string) (*TenantInvite, error) {
	for _, inv := range r.invites {
		if inv.TenantID == tenantID && inv.Email == email && inv.Status == "pending" {
			return nil, ErrInvitePending
		}
	}
	r.seq++
	tok, _ := newInviteToken()
	inv := &TenantInvite{
		ID: "inv-" + itoaInvite(r.seq), TenantID: tenantID, Email: email, Role: role,
		Token: tok, Status: "pending", InvitedBy: invitedBy,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(InviteTTL),
	}
	r.invites[inv.ID] = inv
	r.byToken[tok] = inv
	return inv, nil
}

func (r *inviteRepo) ListInvites(_ context.Context, tenantID string) ([]*TenantInvite, error) {
	var out []*TenantInvite
	for _, inv := range r.invites {
		if inv.TenantID == tenantID {
			cp := *inv
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *inviteRepo) GetInvite(_ context.Context, tenantID, id string) (*TenantInvite, error) {
	if inv, ok := r.invites[id]; ok && inv.TenantID == tenantID {
		return inv, nil
	}
	return nil, ErrInviteNotFound
}

func (r *inviteRepo) GetInviteByToken(_ context.Context, token string) (*TenantInvite, error) {
	if inv, ok := r.byToken[token]; ok {
		return inv, nil
	}
	return nil, ErrInviteNotFound
}

func (r *inviteRepo) ResendInvite(_ context.Context, tenantID, id string) (*TenantInvite, error) {
	inv, ok := r.invites[id]
	if !ok || inv.TenantID != tenantID || inv.Status != "pending" {
		return nil, ErrInviteNotFound
	}
	tok, _ := newInviteToken()
	delete(r.byToken, inv.Token)
	inv.Token = tok
	inv.ExpiresAt = time.Now().Add(InviteTTL)
	r.byToken[tok] = inv
	return inv, nil
}

func (r *inviteRepo) RevokeInvite(_ context.Context, tenantID, id string) error {
	inv, ok := r.invites[id]
	if !ok || inv.TenantID != tenantID || inv.Status != "pending" {
		return ErrInviteNotFound
	}
	inv.Status = "revoked"
	return nil
}

func (r *inviteRepo) MarkInviteAccepted(_ context.Context, id string) error {
	if inv, ok := r.invites[id]; ok {
		inv.Status = "accepted"
	}
	return nil
}

func itoaInvite(n int) string { return string(rune('0' + n)) }

func ownerCtx() context.Context {
	return context.WithValue(context.Background(), UserContextKey, &User{ID: "owner-1", Email: "owner@example.com"})
}

func TestInviteFlow_CreateListResendRevoke(t *testing.T) {
	repo := newInviteRepo()
	h := NewHandler(repo, NewValidator())
	const tid = "tenant-1"

	// Create an invite for an unregistered email.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tid+"/invites",
		bytes.NewBufferString(`{"email":"new@example.com","role":"editor"}`)).WithContext(ownerCtx())
	req.SetPathValue("tenant_id", tid)
	rec := httptest.NewRecorder()
	h.HandleCreateInvite(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data TenantInvite `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Data.Status != "pending" || created.Data.Token == "" {
		t.Fatalf("create: expected pending invite with token, got %+v", created.Data)
	}
	inviteID := created.Data.ID

	// List shows it, token redacted.
	lreq := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+tid+"/invites", nil)
	lreq.SetPathValue("tenant_id", tid)
	lrec := httptest.NewRecorder()
	h.HandleListInvites(lrec, lreq)
	if lrec.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", lrec.Code)
	}
	var listed struct {
		Data []TenantInvite `json:"data"`
	}
	_ = json.Unmarshal(lrec.Body.Bytes(), &listed)
	if len(listed.Data) != 1 || listed.Data[0].Token != "" {
		t.Fatalf("list: want 1 invite with redacted token, got %+v", listed.Data)
	}

	// Resend rotates the token.
	rreq := httptest.NewRequest(http.MethodPost, "/", nil)
	rreq.SetPathValue("tenant_id", tid)
	rreq.SetPathValue("invite_id", inviteID)
	rrec := httptest.NewRecorder()
	h.HandleResendInvite(rrec, rreq)
	if rrec.Code != http.StatusOK {
		t.Fatalf("resend: want 200, got %d: %s", rrec.Code, rrec.Body.String())
	}

	// Revoke.
	dreq := httptest.NewRequest(http.MethodDelete, "/", nil)
	dreq.SetPathValue("tenant_id", tid)
	dreq.SetPathValue("invite_id", inviteID)
	drec := httptest.NewRecorder()
	h.HandleRevokeInvite(drec, dreq)
	if drec.Code != http.StatusOK {
		t.Fatalf("revoke: want 200, got %d: %s", drec.Code, drec.Body.String())
	}
	if repo.invites[inviteID].Status != "revoked" {
		t.Fatalf("revoke: invite status = %s, want revoked", repo.invites[inviteID].Status)
	}
}

func TestInviteAccept_MatchingUserBecomesMember(t *testing.T) {
	repo := newInviteRepo()
	h := NewHandler(repo, NewValidator())
	const tid = "tenant-9"

	inv, _ := repo.CreateInvite(context.Background(), tid, "invitee@example.com", "viewer", "owner-1")

	// Accept as a logged-in user whose email matches.
	body, _ := json.Marshal(acceptInviteRequest{Token: inv.Token})
	ctx := context.WithValue(context.Background(), UserContextKey, &User{ID: "u-7", Email: "invitee@example.com"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/invites/accept", bytes.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.HandleAcceptInvite(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(repo.added) != 1 || repo.added[0] != "u-7" {
		t.Fatalf("accept: expected u-7 added as member, got %v", repo.added)
	}
	if repo.invites[inv.ID].Status != "accepted" {
		t.Fatalf("accept: invite status = %s, want accepted", repo.invites[inv.ID].Status)
	}
}

func TestInviteAccept_EmailMismatchRejected(t *testing.T) {
	repo := newInviteRepo()
	h := NewHandler(repo, NewValidator())
	inv, _ := repo.CreateInvite(context.Background(), "t", "invitee@example.com", "viewer", "owner-1")

	body, _ := json.Marshal(acceptInviteRequest{Token: inv.Token})
	ctx := context.WithValue(context.Background(), UserContextKey, &User{ID: "u-8", Email: "someone-else@example.com"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/invites/accept", bytes.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.HandleAcceptInvite(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("accept mismatch: want 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(repo.added) != 0 {
		t.Fatalf("accept mismatch: no member should be added, got %v", repo.added)
	}
}

func TestInviteCreate_RegisteredUserAddedDirectly(t *testing.T) {
	repo := newInviteRepo()
	repo.users["existing@example.com"] = &User{ID: "u-existing", Email: "existing@example.com"}
	h := NewHandler(repo, NewValidator())
	const tid = "tenant-2"

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tid+"/invites",
		bytes.NewBufferString(`{"email":"existing@example.com","role":"admin"}`)).WithContext(ownerCtx())
	req.SetPathValue("tenant_id", tid)
	rec := httptest.NewRecorder()
	h.HandleCreateInvite(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(repo.added) != 1 || repo.added[0] != "u-existing" {
		t.Fatalf("expected existing user added directly, got %v", repo.added)
	}
	if len(repo.invites) != 0 {
		t.Fatalf("expected no pending invite for a registered user, got %d", len(repo.invites))
	}
}
