package managementapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/oauth"
)

// =====================================================================
// Admin endpoints — provider CRUD
// =====================================================================

func TestCreateOAuthProvider_Success(t *testing.T) {
	handler, _ := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "Acme Microsoft",
		"provider_type": "microsoft365",
		"client_id":     "client-abc",
		"client_secret": "supersecret",
		"redirect_url":  "https://app.example.com/oauth/callback",
	})
	r := httptest.NewRequest("POST", "/api/v1/oauth/providers", bytes.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.CreateOAuthProvider(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d, body=%s", w.Code, w.Body.String())
	}
	var resp oauthProviderResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID == "" {
		t.Errorf("missing id in response")
	}
	if resp.AuthURL == "" || resp.TokenURL == "" {
		t.Errorf("MS365 profile defaults should populate auth_url + token_url, got %+v", resp)
	}
	if !strings.Contains(strings.Join(resp.Scopes, " "), "offline_access") {
		t.Errorf("MS365 default scopes should include offline_access, got %v", resp.Scopes)
	}
	// The response must not include the client secret.
	raw := w.Body.String()
	if strings.Contains(raw, "supersecret") {
		t.Errorf("response leaks client_secret: %s", raw)
	}
}

func TestCreateOAuthProvider_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		body map[string]interface{}
	}{
		{"missing name", map[string]interface{}{"provider_type": "microsoft365", "client_id": "c", "client_secret": "s", "redirect_url": "u"}},
		{"missing provider_type", map[string]interface{}{"name": "n", "client_id": "c", "client_secret": "s", "redirect_url": "u"}},
		{"missing client_id", map[string]interface{}{"name": "n", "provider_type": "microsoft365", "client_secret": "s", "redirect_url": "u"}},
		{"missing client_secret", map[string]interface{}{"name": "n", "provider_type": "microsoft365", "client_id": "c", "redirect_url": "u"}},
		{"missing redirect_url", map[string]interface{}{"name": "n", "provider_type": "microsoft365", "client_id": "c", "client_secret": "s"}},
		{"custom without auth_url", map[string]interface{}{"name": "n", "provider_type": "custom", "client_id": "c", "client_secret": "s", "redirect_url": "u", "token_url": "https://t/"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, _ := setupTestHandler()
			body, _ := json.Marshal(tc.body)
			r := httptest.NewRequest("POST", "/api/v1/oauth/providers", bytes.NewReader(body)).
				WithContext(contextWithTenant("tenant-1"))
			w := httptest.NewRecorder()
			handler.CreateOAuthProvider(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("want 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestListOAuthProviders_EmptyAndPopulated(t *testing.T) {
	handler, _ := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	// Empty list first.
	r := httptest.NewRequest("GET", "/api/v1/oauth/providers", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ListOAuthProvidersHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var resp struct {
		Providers []oauthProviderResponse `json:"providers"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Providers) != 0 {
		t.Errorf("want empty list, got %d", len(resp.Providers))
	}

	// Create two providers.
	for _, name := range []string{"p1", "p2"} {
		body, _ := json.Marshal(map[string]interface{}{
			"name": name, "provider_type": "microsoft365", "client_id": "c", "client_secret": "s",
			"redirect_url": "https://app/oauth/callback",
		})
		req := httptest.NewRequest("POST", "/api/v1/oauth/providers", bytes.NewReader(body)).WithContext(ctx)
		handler.CreateOAuthProvider(httptest.NewRecorder(), req)
	}

	r2 := httptest.NewRequest("GET", "/api/v1/oauth/providers", nil).WithContext(ctx)
	w2 := httptest.NewRecorder()
	handler.ListOAuthProvidersHandler(w2, r2)
	_ = json.Unmarshal(w2.Body.Bytes(), &resp)
	if len(resp.Providers) != 2 {
		t.Errorf("want 2 providers, got %d", len(resp.Providers))
	}
}

func TestGetOAuthProvider_NotFound(t *testing.T) {
	handler, _ := setupTestHandler()
	r := httptest.NewRequest("GET", "/api/v1/oauth/providers/missing", nil).
		WithContext(contextWithTenant("tenant-1"))
	r.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	handler.GetOAuthProvider(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

func TestUpdateOAuthProvider_RotatesSecretWhenSupplied(t *testing.T) {
	handler, _ := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	// Create.
	createBody, _ := json.Marshal(map[string]interface{}{
		"name": "p", "provider_type": "microsoft365", "client_id": "c", "client_secret": "old",
		"redirect_url": "https://app/oauth/callback",
	})
	cw := httptest.NewRecorder()
	handler.CreateOAuthProvider(cw, httptest.NewRequest("POST", "/api/v1/oauth/providers", bytes.NewReader(createBody)).WithContext(ctx))
	var created oauthProviderResponse
	_ = json.Unmarshal(cw.Body.Bytes(), &created)

	// Update with a new secret.
	updateBody, _ := json.Marshal(map[string]interface{}{
		"name": "p", "provider_type": "microsoft365", "client_id": "c", "client_secret": "new",
		"redirect_url": "https://app/oauth/callback",
	})
	r := httptest.NewRequest("PUT", "/api/v1/oauth/providers/"+created.ID, bytes.NewReader(updateBody)).WithContext(ctx)
	r.SetPathValue("id", created.ID)
	w := httptest.NewRecorder()
	handler.UpdateOAuthProvider(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDeleteOAuthProvider_BlocksWhenActiveGrants(t *testing.T) {
	handler, repo := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	// Create provider.
	createBody, _ := json.Marshal(map[string]interface{}{
		"name": "p", "provider_type": "microsoft365", "client_id": "c", "client_secret": "s",
		"redirect_url": "https://app/oauth/callback",
	})
	cw := httptest.NewRecorder()
	handler.CreateOAuthProvider(cw, httptest.NewRequest("POST", "/api/v1/oauth/providers", bytes.NewReader(createBody)).WithContext(ctx))
	var created oauthProviderResponse
	_ = json.Unmarshal(cw.Body.Bytes(), &created)

	// Inject an active grant via the in-memory store helper.
	_ = repo.CreateGrant(ctx, &oauth.Grant{
		TenantID: "tenant-1", ProviderID: created.ID, ProviderName: "p", ProviderType: "microsoft365",
	}, "a", "r")

	r := httptest.NewRequest("DELETE", "/api/v1/oauth/providers/"+created.ID, nil).WithContext(ctx)
	r.SetPathValue("id", created.ID)
	w := httptest.NewRecorder()
	handler.DeleteOAuthProvider(w, r)
	if w.Code != http.StatusConflict {
		t.Errorf("want 409 (active grants), got %d body=%s", w.Code, w.Body.String())
	}
}

// =====================================================================
// Grant endpoints
// =====================================================================

func TestListOAuthGrants_EmptyAndPopulated(t *testing.T) {
	handler, repo := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	// Empty.
	r := httptest.NewRequest("GET", "/api/v1/oauth/grants", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ListOAuthGrants(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}

	// Populated.
	_ = repo.CreateGrant(ctx, &oauth.Grant{TenantID: "tenant-1", ProviderID: "p", ProviderName: "p", ProviderType: "fake"}, "a", "r")
	r2 := httptest.NewRequest("GET", "/api/v1/oauth/grants", nil).WithContext(ctx)
	w2 := httptest.NewRecorder()
	handler.ListOAuthGrants(w2, r2)
	var resp struct {
		Grants []oauthGrantResponse `json:"grants"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &resp)
	if len(resp.Grants) != 1 {
		t.Errorf("want 1 grant, got %d", len(resp.Grants))
	}
	// Make sure tokens never appear in the response.
	if strings.Contains(w2.Body.String(), "access_token") || strings.Contains(w2.Body.String(), "refresh_token") {
		t.Errorf("grant list response leaks tokens: %s", w2.Body.String())
	}
}

func TestGetOAuthGrant_NotFound(t *testing.T) {
	handler, _ := setupTestHandler()
	r := httptest.NewRequest("GET", "/api/v1/oauth/grants/missing", nil).WithContext(contextWithTenant("tenant-1"))
	r.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	handler.GetOAuthGrant(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

// StartOAuth requires the oauth client to be wired; the 503 path covers the
// case where the server is configured without it.
func TestStartOAuth_503WithoutClient(t *testing.T) {
	handler, _ := setupTestHandler()
	r := httptest.NewRequest("POST", "/api/v1/oauth/providers/p/start", nil).WithContext(contextWithTenant("tenant-1"))
	r.SetPathValue("id", "p")
	w := httptest.NewRecorder()
	handler.StartOAuth(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 when OAuth client missing, got %d", w.Code)
	}
}

// With a client wired, StartOAuth returns the authorize URL and sets cookies.
func TestStartOAuth_ReturnsURLAndSetsCookies(t *testing.T) {
	handler, repo := setupTestHandler()
	// Wire the OAuth client against the same mock repo.
	reg := oauth.NewProviderRegistry()
	reg.Register(oauth.Provider{
		Type: "microsoft365", AuthURL: "https://login.example/authorize",
		TokenURL: "https://login.example/token", Scopes: []string{"openid"}, SupportsRefresh: true,
	})
	handler.SetOAuthClient(oauth.New(repo, reg))

	// Create a provider so StartAuth has something to resolve.
	ctx := contextWithTenant("tenant-1")
	createBody, _ := json.Marshal(map[string]interface{}{
		"name": "p", "provider_type": "microsoft365", "client_id": "c", "client_secret": "s",
		"redirect_url": "https://app/oauth/callback",
	})
	cw := httptest.NewRecorder()
	handler.CreateOAuthProvider(cw, httptest.NewRequest("POST", "/api/v1/oauth/providers", bytes.NewReader(createBody)).WithContext(ctx))
	var created oauthProviderResponse
	_ = json.Unmarshal(cw.Body.Bytes(), &created)

	r := httptest.NewRequest("POST", "/api/v1/oauth/providers/"+created.ID+"/start", nil).WithContext(ctx)
	r.SetPathValue("id", created.ID)
	w := httptest.NewRecorder()
	handler.StartOAuth(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp startOAuthResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	// The provider config seeded the MS365 profile defaults (and those win
	// over the registry's URL), so the returned URL points at the real
	// Microsoft endpoint. The important thing is the query string carries
	// the PKCE challenge and our state.
	if !strings.Contains(resp.AuthorizeURL, "code_challenge_method=S256") {
		t.Errorf("authorize_url missing PKCE params: %s", resp.AuthorizeURL)
	}
	if !strings.Contains(resp.AuthorizeURL, "response_type=code") {
		t.Errorf("authorize_url missing response_type: %s", resp.AuthorizeURL)
	}
	// Cookies must include state + verifier.
	gotCookies := w.Header()["Set-Cookie"]
	wantNames := []string{oauthStateCookie, oauthVerifierCookie, oauthProviderIDCookie, oauthTenantIDCookie}
	for _, want := range wantNames {
		found := false
		for _, c := range gotCookies {
			if strings.HasPrefix(c, want+"=") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("cookie %q not set; got=%v", want, gotCookies)
		}
	}
}

// A successful grant must write an audit_log entry (acceptance criterion #5).
// The callback path bypasses AuditMiddleware, so logOAuthGrant emits directly;
// this verifies the entry lands with the right action + details.
func TestLogOAuthGrant_WritesAuditEntry(t *testing.T) {
	handler, repo := setupTestHandler()
	r := httptest.NewRequest("GET", "/api/v1/oauth/callback?code=x&state=y", nil)

	grant := &oauth.Grant{
		ID:             "grant-77",
		TenantID:       "tenant-1",
		ProviderType:   "microsoft365",
		ProviderName:   "Acme MS",
		UserIdentifier: "alice@acme.com",
		ScopesGranted:  []string{"offline_access", "mail.read"},
	}
	handler.logOAuthGrant(r.Context(), r, grant)

	if len(repo.auditEntries) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d", len(repo.auditEntries))
	}
	e := repo.auditEntries[0]
	if e.Action != "oauth.grant" {
		t.Errorf("action = %q, want oauth.grant", e.Action)
	}
	if e.ResourceType != "oauth_grant" || e.ResourceID != "grant-77" {
		t.Errorf("resource = %s/%s, want oauth_grant/grant-77", e.ResourceType, e.ResourceID)
	}
	if e.TenantID != "tenant-1" {
		t.Errorf("tenant = %q, want tenant-1", e.TenantID)
	}
	if e.Details["provider_type"] != "microsoft365" {
		t.Errorf("details.provider_type = %v", e.Details["provider_type"])
	}
	if e.Details["user_identifier"] != "alice@acme.com" {
		t.Errorf("details.user_identifier = %v", e.Details["user_identifier"])
	}
}

// --- Service token endpoint (workers fetch access tokens) ---

// tokenReq drives TokenForGrant with the given service token + tenant headers.
func tokenReq(handler *Handler, serviceToken, tenantID, grantID, query string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/v1/oauth/grants/"+grantID+"/token"+query, nil)
	if serviceToken != "" {
		r.Header.Set("X-Service-Token", serviceToken)
	}
	if tenantID != "" {
		r.Header.Set("X-Tenant-ID", tenantID)
	}
	r.SetPathValue("id", grantID)
	w := httptest.NewRecorder()
	handler.TokenForGrant(w, r)
	return w
}

func TestTokenForGrant_DisabledWhenSecretUnset(t *testing.T) {
	t.Setenv("OAUTH_TOKEN_SERVICE_SECRET", "") // explicitly unset
	handler, _ := setupTestHandler()
	w := tokenReq(handler, "anything", "tenant-1", "g-1", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 when secret unset, got %d", w.Code)
	}
}

func TestTokenForGrant_RejectsWrongServiceToken(t *testing.T) {
	t.Setenv("OAUTH_TOKEN_SERVICE_SECRET", "right-secret")
	handler, _ := setupTestHandler()
	w := tokenReq(handler, "wrong-secret", "tenant-1", "g-1", "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for wrong service token, got %d", w.Code)
	}
}

func TestTokenForGrant_RequiresTenantHeader(t *testing.T) {
	t.Setenv("OAUTH_TOKEN_SERVICE_SECRET", "s")
	handler, repo := setupTestHandler()
	handler.SetOAuthClient(oauth.New(repo, oauth.NewProviderRegistry()))
	w := tokenReq(handler, "s", "", "g-1", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 without X-Tenant-ID, got %d", w.Code)
	}
}

func TestTokenForGrant_NotFoundForOtherTenant(t *testing.T) {
	t.Setenv("OAUTH_TOKEN_SERVICE_SECRET", "s")
	handler, repo := setupTestHandler()
	handler.SetOAuthClient(oauth.New(repo, oauth.NewProviderRegistry()))
	w := tokenReq(handler, "s", "tenant-1", "missing-grant", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404 for unknown grant, got %d", w.Code)
	}
}

func TestTokenForGrant_ReturnsAccessTokenHappyPath(t *testing.T) {
	t.Setenv("OAUTH_TOKEN_SERVICE_SECRET", "s")
	handler, repo := setupTestHandler()
	handler.SetOAuthClient(oauth.New(repo, oauth.NewProviderRegistry()))

	// Seed a grant with a non-expiring access token so Token() returns it
	// without attempting a refresh.
	future := time.Now().Add(time.Hour)
	g := &oauth.Grant{TenantID: "tenant-1", ProviderID: "p", ProviderName: "p", ProviderType: "fake", ExpiresAt: &future}
	if err := repo.CreateGrant(context.Background(), g, "access-tok-1", "refresh-tok-1"); err != nil {
		t.Fatalf("seed grant: %v", err)
	}

	w := tokenReq(handler, "s", "tenant-1", g.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.AccessToken != "access-tok-1" {
		t.Errorf("got access_token=%q, want access-tok-1", body.AccessToken)
	}
}

// HandleOAuthCallback rejects when cookies are missing.
func TestHandleOAuthCallback_MissingCookies(t *testing.T) {
	handler, repo := setupTestHandler()
	reg := oauth.NewProviderRegistry()
	reg.Register(oauth.Provider{Type: "microsoft365", AuthURL: "x", TokenURL: "y", SupportsRefresh: true})
	handler.SetOAuthClient(oauth.New(repo, reg))

	r := httptest.NewRequest("GET", "/api/v1/oauth/callback?code=abc&state=def", nil)
	w := httptest.NewRecorder()
	handler.HandleOAuthCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}
