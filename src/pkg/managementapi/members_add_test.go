package managementapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// addMemberRepo overrides the user-lookup and membership-insert behaviour of
// the base MockRepository so we can drive HandleAddMember's branches.
type addMemberRepo struct {
	*MockRepository
	user    *User
	userErr error
	addErr  error
	addedID string
}

func (r *addMemberRepo) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	if r.userErr != nil {
		return nil, r.userErr
	}
	return r.user, nil
}

func (r *addMemberRepo) AddTenantMember(ctx context.Context, tenantID, userID, role string) error {
	if r.addErr != nil {
		return r.addErr
	}
	r.addedID = userID
	return nil
}

func newAddMemberHandler(repo Repository) *Handler {
	return NewHandler(repo, NewValidator())
}

func addMemberRequestTo(t *testing.T, h *Handler, tenantID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tenantID+"/members", bytes.NewBufferString(body))
	req.SetPathValue("tenant_id", tenantID)
	rec := httptest.NewRecorder()
	h.HandleAddMember(rec, req)
	return rec
}

func TestHandleAddMember_Success(t *testing.T) {
	repo := &addMemberRepo{MockRepository: NewMockRepository(), user: &User{ID: "user-99", Email: "teammate@example.com", FullName: "Tee"}}
	h := newAddMemberHandler(repo)

	rec := addMemberRequestTo(t, h, "tenant-1", `{"email":"teammate@example.com","role":"editor"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if repo.addedID != "user-99" {
		t.Fatalf("expected membership added for user-99, got %q", repo.addedID)
	}
	var resp SuccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
}

func TestHandleAddMember_UnknownEmail(t *testing.T) {
	repo := &addMemberRepo{MockRepository: NewMockRepository(), userErr: auth.ErrUserNotFound}
	h := newAddMemberHandler(repo)

	rec := addMemberRequestTo(t, h, "tenant-1", `{"email":"nobody@example.com","role":"viewer"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for unknown email, got %d", rec.Code)
	}
}

func TestHandleAddMember_AlreadyMember(t *testing.T) {
	repo := &addMemberRepo{MockRepository: NewMockRepository(), user: &User{ID: "user-7", Email: "dup@example.com"}, addErr: ErrAlreadyMember}
	h := newAddMemberHandler(repo)

	rec := addMemberRequestTo(t, h, "tenant-1", `{"email":"dup@example.com","role":"viewer"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 for already-member, got %d", rec.Code)
	}
}

func TestHandleAddMember_Validation(t *testing.T) {
	h := newAddMemberHandler(&addMemberRepo{MockRepository: NewMockRepository()})

	cases := map[string]string{
		"empty email":  `{"email":"","role":"viewer"}`,
		"bad role":     `{"email":"x@example.com","role":"superuser"}`,
		"invalid json": `{`,
		"empty body":   ``,
	}
	for name, body := range cases {
		rec := addMemberRequestTo(t, h, "tenant-1", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", name, rec.Code)
		}
	}
}
