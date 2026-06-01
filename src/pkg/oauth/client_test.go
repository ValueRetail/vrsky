package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =====================================================================
// In-memory test store
// =====================================================================
//
// inMemStore satisfies oauth.Store without a database. Tokens are stored
// as plaintext (no need to exercise the real secrets/crypto layer here —
// the Postgres store has separate tests for that).

type inMemStore struct {
	mu           sync.Mutex
	providers    map[string]*ProviderConfig // by ID
	clientSecret string
	grants       map[string]*Grant       // by ID
	tokens       map[string]storedTokens // by grant ID
	failures     map[string]storedFailure
	updateCount  int64 // counts UpdateTokens calls — used by the singleflight test
}

type storedTokens struct {
	accessTok  string
	refreshTok string
	expiresAt  *time.Time
}

type storedFailure struct {
	at     time.Time
	reason string
}

func newInMemStore() *inMemStore {
	return &inMemStore{
		providers: map[string]*ProviderConfig{},
		grants:    map[string]*Grant{},
		tokens:    map[string]storedTokens{},
		failures:  map[string]storedFailure{},
	}
}

func (s *inMemStore) GetProviderConfig(_ context.Context, tenantID, providerID string) (*ProviderConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, ok := s.providers[providerID]
	if !ok || cfg.TenantID != tenantID {
		return nil, ErrProviderNotFound
	}
	return cfg, nil
}
func (s *inMemStore) ResolveClientSecret(_ context.Context, _ *ProviderConfig) (string, error) {
	return s.clientSecret, nil
}
func (s *inMemStore) CreateGrant(_ context.Context, g *Grant, accessTok, refreshTok string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g.ID = fmt.Sprintf("grant-%d", len(s.grants)+1)
	s.grants[g.ID] = g
	s.tokens[g.ID] = storedTokens{accessTok: accessTok, refreshTok: refreshTok, expiresAt: g.ExpiresAt}
	return nil
}
func (s *inMemStore) UpdateTokens(_ context.Context, grantID, accessTok, refreshTok string, expiresAt *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	atomic.AddInt64(&s.updateCount, 1)
	if _, ok := s.grants[grantID]; !ok {
		return ErrGrantNotFound
	}
	s.tokens[grantID] = storedTokens{accessTok: accessTok, refreshTok: refreshTok, expiresAt: expiresAt}
	now := time.Now()
	s.grants[grantID].ExpiresAt = expiresAt
	s.grants[grantID].LastRefreshedAt = &now
	delete(s.failures, grantID)
	return nil
}
func (s *inMemStore) GetGrant(_ context.Context, tenantID, grantID string) (*Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.grants[grantID]
	if !ok || g.TenantID != tenantID {
		return nil, ErrGrantNotFound
	}
	// Return a copy with tokens populated.
	cp := *g
	tok := s.tokens[grantID]
	cp.AccessToken = tok.accessTok
	cp.RefreshToken = tok.refreshTok
	cp.ExpiresAt = tok.expiresAt
	return &cp, nil
}
func (s *inMemStore) GetGrantMeta(ctx context.Context, tenantID, grantID string) (*Grant, error) {
	g, err := s.GetGrant(ctx, tenantID, grantID)
	if err != nil {
		return nil, err
	}
	g.AccessToken = ""
	g.RefreshToken = ""
	return g, nil
}
func (s *inMemStore) ListGrants(_ context.Context, tenantID string) ([]*Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Grant
	for _, g := range s.grants {
		if g.TenantID == tenantID {
			cp := *g
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (s *inMemStore) MarkRevoked(_ context.Context, tenantID, grantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.grants[grantID]
	if !ok || g.TenantID != tenantID {
		return ErrGrantNotFound
	}
	now := time.Now()
	g.RevokedAt = &now
	return nil
}
func (s *inMemStore) MarkRefreshFailure(_ context.Context, tenantID, grantID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[grantID] = storedFailure{at: time.Now(), reason: reason}
	return nil
}
func (s *inMemStore) ScanExpiring(_ context.Context, within time.Duration, _ int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(within)
	var out []string
	for id, g := range s.grants {
		if g.RevokedAt != nil || g.ExpiresAt == nil {
			continue
		}
		tok := s.tokens[id]
		if tok.refreshTok == "" {
			continue
		}
		if g.ExpiresAt.Before(cutoff) {
			out = append(out, id)
		}
	}
	return out, nil
}

// =====================================================================
// Fake provider httptest.Server
// =====================================================================

type fakeProvider struct {
	srv *httptest.Server

	// Counters / state captured for assertions.
	mu             sync.Mutex
	tokenHits      int
	revokeHits     int
	lastVerifier   string
	lastGrant      string
	lastRevokeBody url.Values
	tokenLatency   time.Duration

	// Configured behaviour.
	clientID       string
	expectedSecret string
	failNextTokens int    // returns 500 this many times before succeeding
	refreshError   string // when set, the token endpoint replies with this OAuth error JSON during refresh
	rotateRefresh  bool   // when true, the refresh response also includes a new refresh_token
}

func newFakeProvider(t *testing.T, clientID, clientSecret string) *fakeProvider {
	t.Helper()
	fp := &fakeProvider{clientID: clientID, expectedSecret: clientSecret}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", fp.handleAuthorize)
	mux.HandleFunc("/token", fp.handleToken)
	mux.HandleFunc("/revoke", fp.handleRevoke)
	fp.srv = httptest.NewServer(mux)
	t.Cleanup(fp.srv.Close)
	return fp
}

func (fp *fakeProvider) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	// Echo back to the redirect URI with a code + the state. The verifier
	// stays out of band (the real provider doesn't see it until /token).
	q := r.URL.Query()
	redirect := q.Get("redirect_uri")
	state := q.Get("state")
	u, _ := url.Parse(redirect)
	rq := u.Query()
	rq.Set("code", "auth-code-123")
	rq.Set("state", state)
	u.RawQuery = rq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (fp *fakeProvider) handleToken(w http.ResponseWriter, r *http.Request) {
	if fp.tokenLatency > 0 {
		time.Sleep(fp.tokenLatency)
	}
	fp.mu.Lock()
	fp.tokenHits++
	if fp.failNextTokens > 0 {
		fp.failNextTokens--
		fp.mu.Unlock()
		http.Error(w, "transient", http.StatusInternalServerError)
		return
	}
	rerr := fp.refreshError
	fp.mu.Unlock()

	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	grantType := form.Get("grant_type")

	// Accept either auth style: golang.org/x/oauth2 auto-detects by probing
	// with HTTP Basic first; if the provider rejects it, it retries with
	// client_id/client_secret in the form body. Real providers usually pick
	// one and document it; for testing we want both to succeed so the probe
	// doesn't cause a phantom second HTTP call.
	gotClientID, gotSecret, ok := r.BasicAuth()
	if !ok {
		gotClientID = form.Get("client_id")
		gotSecret = form.Get("client_secret")
	}
	if gotClientID != fp.clientID || gotSecret != fp.expectedSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}

	if grantType == "authorization_code" {
		fp.mu.Lock()
		fp.lastVerifier = form.Get("code_verifier")
		fp.mu.Unlock()
		resp := map[string]interface{}{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "https://graph.microsoft.com/.default offline_access",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	if grantType == "refresh_token" {
		if rerr != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"%s"}`, rerr)
			return
		}
		resp := map[string]interface{}{
			"access_token": fmt.Sprintf("access-%d", fp.tokenHits),
			"token_type":   "Bearer",
			"expires_in":   3600,
		}
		fp.mu.Lock()
		if fp.rotateRefresh {
			resp["refresh_token"] = fmt.Sprintf("refresh-%d", fp.tokenHits)
		}
		fp.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	http.Error(w, "unsupported grant_type", http.StatusBadRequest)
}

func (fp *fakeProvider) handleRevoke(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	fp.mu.Lock()
	fp.revokeHits++
	fp.lastRevokeBody = form
	fp.lastGrant = form.Get("token")
	fp.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// makeProvider seeds the in-mem store with a provider config and registry
// that point at fp's URLs.
func makeProvider(fp *fakeProvider, store *inMemStore) (*ProviderRegistry, string) {
	reg := NewProviderRegistry()
	reg.Register(Provider{
		Type:            "fake",
		AuthURL:         fp.srv.URL + "/authorize",
		TokenURL:        fp.srv.URL + "/token",
		RevokeURL:       fp.srv.URL + "/revoke",
		Scopes:          []string{"read"},
		SupportsRefresh: true,
	})
	store.clientSecret = fp.expectedSecret
	cfg := &ProviderConfig{
		ID:           "prov-1",
		TenantID:     "tenant-1",
		Name:         "Fake Provider",
		ProviderType: "fake",
		ClientID:     fp.clientID,
		AuthURL:      fp.srv.URL + "/authorize",
		TokenURL:     fp.srv.URL + "/token",
		RevokeURL:    fp.srv.URL + "/revoke",
		Scopes:       []string{"read"},
		RedirectURL:  "https://app.example.com/callback",
	}
	store.providers["prov-1"] = cfg
	return reg, cfg.ID
}

// =====================================================================
// Tests
// =====================================================================

func TestStartAuth_BuildsURLWithPKCE(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	store := newInMemStore()
	reg, providerID := makeProvider(fp, store)

	c := New(store, reg)
	authURL, state, verifier, err := c.StartAuth(context.Background(), "tenant-1", providerID, StartOptions{})
	if err != nil {
		t.Fatalf("StartAuth: %v", err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("response_type") != "code" {
		t.Errorf("response_type missing")
	}
	if q.Get("state") != state {
		t.Errorf("state mismatch in URL")
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("challenge method should be S256")
	}
	if q.Get("code_challenge") != pkceChallenge(verifier) {
		t.Errorf("challenge does not match verifier")
	}
	if q.Get("scope") == "" {
		t.Errorf("scope param missing")
	}
}

func TestComplete_PersistsGrantAndTokens(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	store := newInMemStore()
	reg, providerID := makeProvider(fp, store)

	c := New(store, reg)
	_, state, verifier, err := c.StartAuth(context.Background(), "tenant-1", providerID, StartOptions{})
	if err != nil {
		t.Fatalf("StartAuth: %v", err)
	}
	g, err := c.Complete(context.Background(), "tenant-1", providerID, "auth-code-123", verifier, state, state, StartOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if g.AccessToken != "access-1" || g.RefreshToken != "refresh-1" {
		t.Errorf("tokens not propagated to returned Grant: %+v", g)
	}
	if fp.lastVerifier != verifier {
		t.Errorf("provider received wrong verifier: got %q want %q", fp.lastVerifier, verifier)
	}
	stored := store.tokens[g.ID]
	if stored.accessTok != "access-1" || stored.refreshTok != "refresh-1" {
		t.Errorf("tokens not persisted: %+v", stored)
	}
}

func TestComplete_StateMismatchRejected(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	store := newInMemStore()
	reg, providerID := makeProvider(fp, store)
	c := New(store, reg)

	_, err := c.Complete(context.Background(), "tenant-1", providerID, "code", "verifier", "expected", "actual", StartOptions{})
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("expected ErrStateMismatch, got %v", err)
	}
}

// Token returns the stored access token when not near expiry.
func TestToken_HotPathReturnsCached(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	store := newInMemStore()
	reg, providerID := makeProvider(fp, store)

	c := New(store, reg, WithRefreshSkew(30*time.Second))
	_, state, verifier, _ := c.StartAuth(context.Background(), "tenant-1", providerID, StartOptions{})
	g, _ := c.Complete(context.Background(), "tenant-1", providerID, "auth-code-123", verifier, state, state, StartOptions{})

	before := fp.tokenHits
	tok, err := c.Token(context.Background(), "tenant-1", g.ID)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "access-1" {
		t.Errorf("got %q want access-1", tok)
	}
	if fp.tokenHits != before {
		t.Errorf("Token() must not hit provider when not near expiry")
	}
}

// Token triggers a refresh when the access token is near expiry, and stores
// the rotated refresh token when the provider rotates.
func TestToken_RefreshesOnExpiry(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	fp.rotateRefresh = true
	store := newInMemStore()
	reg, providerID := makeProvider(fp, store)

	// Clock starts now; expires_at returned by the fake provider is +3600s.
	now := time.Now()
	c := New(store, reg, WithClock(func() time.Time { return now }), WithRefreshSkew(60*time.Second))
	_, state, verifier, _ := c.StartAuth(context.Background(), "tenant-1", providerID, StartOptions{})
	g, _ := c.Complete(context.Background(), "tenant-1", providerID, "auth-code-123", verifier, state, state, StartOptions{})

	// Move "now" to 30s before expiry — inside the skew window.
	now = g.ExpiresAt.Add(-30 * time.Second)

	tok, err := c.Token(context.Background(), "tenant-1", g.ID)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok == "access-1" {
		t.Errorf("expected refreshed access token, still got %q", tok)
	}
	stored := store.tokens[g.ID]
	if stored.refreshTok == "refresh-1" {
		t.Errorf("rotated refresh token not persisted")
	}
}

// Concurrent Refresh() calls on the same grant collapse to a single provider
// HTTP call via singleflight. We test Refresh directly so the result is
// independent of the wall-clock-based expiry comparison in Token (Token also
// goes through singleflight; this just keeps the assertion crisp).
func TestRefresh_SingleflightDedupesConcurrent(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	fp.tokenLatency = 200 * time.Millisecond
	store := newInMemStore()
	reg, providerID := makeProvider(fp, store)

	c := New(store, reg)
	_, state, verifier, _ := c.StartAuth(context.Background(), "tenant-1", providerID, StartOptions{})
	g, _ := c.Complete(context.Background(), "tenant-1", providerID, "auth-code-123", verifier, state, state, StartOptions{})

	before := fp.tokenHits

	const concurrency = 50
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := c.Refresh(context.Background(), "tenant-1", g.ID); err != nil {
				errs <- err
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("Refresh error: %v", e)
	}
	if got := fp.tokenHits - before; got != 1 {
		t.Errorf("expected exactly 1 provider call from concurrent refresh, got %d", got)
	}
	if got := atomic.LoadInt64(&store.updateCount); got != 1 {
		t.Errorf("expected exactly 1 UpdateTokens call, got %d", got)
	}
}

// invalid_grant during refresh becomes ErrRefreshExpired.
func TestRefresh_InvalidGrantBecomesErrRefreshExpired(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	store := newInMemStore()
	reg, providerID := makeProvider(fp, store)
	c := New(store, reg)
	_, state, verifier, _ := c.StartAuth(context.Background(), "tenant-1", providerID, StartOptions{})
	g, _ := c.Complete(context.Background(), "tenant-1", providerID, "auth-code-123", verifier, state, state, StartOptions{})

	fp.mu.Lock()
	fp.refreshError = "invalid_grant"
	fp.mu.Unlock()

	_, err := c.Refresh(context.Background(), "tenant-1", g.ID)
	if !errors.Is(err, ErrRefreshExpired) {
		t.Fatalf("expected ErrRefreshExpired, got %v", err)
	}
}

// Provider 5xx during refresh becomes ErrProviderError (retriable class).
func TestRefresh_ProviderErrorIsRetriable(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	store := newInMemStore()
	reg, providerID := makeProvider(fp, store)
	c := New(store, reg)
	_, state, verifier, _ := c.StartAuth(context.Background(), "tenant-1", providerID, StartOptions{})
	g, _ := c.Complete(context.Background(), "tenant-1", providerID, "auth-code-123", verifier, state, state, StartOptions{})

	fp.mu.Lock()
	fp.failNextTokens = 3
	fp.mu.Unlock()

	_, err := c.Refresh(context.Background(), "tenant-1", g.ID)
	if !errors.Is(err, ErrProviderError) {
		t.Fatalf("expected ErrProviderError, got %v", err)
	}
}

// Revoke calls the provider's revoke endpoint and marks the grant locally.
func TestRevoke_CallsProviderAndMarksLocal(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	store := newInMemStore()
	reg, providerID := makeProvider(fp, store)
	c := New(store, reg)
	_, state, verifier, _ := c.StartAuth(context.Background(), "tenant-1", providerID, StartOptions{})
	g, _ := c.Complete(context.Background(), "tenant-1", providerID, "auth-code-123", verifier, state, state, StartOptions{})

	if err := c.Revoke(context.Background(), "tenant-1", g.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if fp.revokeHits != 1 {
		t.Errorf("provider revoke endpoint hit %d times, want 1", fp.revokeHits)
	}
	if got := fp.lastRevokeBody.Get("token_type_hint"); got != "refresh_token" {
		t.Errorf("expected refresh_token revoked first, got token_type_hint=%q", got)
	}
	if store.grants[g.ID].RevokedAt == nil {
		t.Errorf("grant not marked revoked locally")
	}
}

// Revoke marks the grant locally even when the provider call fails — the
// operator's "forget this grant" intent always wins.
func TestRevoke_LocalMarkSurvivesProviderError(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	store := newInMemStore()
	reg, providerID := makeProvider(fp, store)
	c := New(store, reg)
	_, state, verifier, _ := c.StartAuth(context.Background(), "tenant-1", providerID, StartOptions{})
	g, _ := c.Complete(context.Background(), "tenant-1", providerID, "auth-code-123", verifier, state, state, StartOptions{})

	// Replace the revoke handler with one that always 500s.
	fp.srv.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusInternalServerError)
	})
	bad := httptest.NewServer(mux)
	defer bad.Close()
	store.providers[providerID].RevokeURL = bad.URL + "/revoke"
	reg.Register(Provider{Type: "fake", AuthURL: bad.URL + "/authorize", TokenURL: bad.URL + "/token", RevokeURL: bad.URL + "/revoke", Scopes: []string{"read"}, SupportsRefresh: true})

	err := c.Revoke(context.Background(), "tenant-1", g.ID)
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("expected provider error, got %v", err)
	}
	if store.grants[g.ID].RevokedAt == nil {
		t.Errorf("local revoke must still take effect")
	}
}

// Token on a revoked grant returns ErrGrantRevoked.
func TestToken_RevokedGrantIsRejected(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	store := newInMemStore()
	reg, providerID := makeProvider(fp, store)
	c := New(store, reg)
	_, state, verifier, _ := c.StartAuth(context.Background(), "tenant-1", providerID, StartOptions{})
	g, _ := c.Complete(context.Background(), "tenant-1", providerID, "auth-code-123", verifier, state, state, StartOptions{})
	_ = store.MarkRevoked(context.Background(), "tenant-1", g.ID)

	_, err := c.Token(context.Background(), "tenant-1", g.ID)
	if !errors.Is(err, ErrGrantRevoked) {
		t.Errorf("expected ErrGrantRevoked, got %v", err)
	}
}

// A grant that came from a non-refresh provider returns ErrNoRefreshToken
// from Refresh.
func TestRefresh_NoRefreshTokenIsTyped(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	store := newInMemStore()
	reg, providerID := makeProvider(fp, store)
	c := New(store, reg)
	_, state, verifier, _ := c.StartAuth(context.Background(), "tenant-1", providerID, StartOptions{})
	g, _ := c.Complete(context.Background(), "tenant-1", providerID, "auth-code-123", verifier, state, state, StartOptions{})

	// Wipe the refresh token in the store.
	tok := store.tokens[g.ID]
	tok.refreshTok = ""
	store.tokens[g.ID] = tok

	_, err := c.Refresh(context.Background(), "tenant-1", g.ID)
	if !errors.Is(err, ErrNoRefreshToken) {
		t.Errorf("expected ErrNoRefreshToken, got %v", err)
	}
}
