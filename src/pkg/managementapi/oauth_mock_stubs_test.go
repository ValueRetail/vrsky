package managementapi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/oauth"
)

// MockRepository's OAuth-related methods need real in-memory behaviour so
// the handler tests in oauth_handler_test.go can drive them end-to-end.
// Storage is per-MockRepository instance via the package-level oauthState
// map (keyed by repo pointer) — keeps the mock backwards-compatible with
// existing tests that construct it with the zero value.
//
// rbacMock (rbac_test.go) keeps its stubs — those tests don't touch OAuth.

type mockOAuthState struct {
	mu        sync.Mutex
	providers map[string]*oauth.ProviderConfig
	grants    map[string]*oauth.Grant
	tokens    map[string]struct{ access, refresh string }
	clientSec map[string]string // provider ID -> plaintext client secret
	nextID    int
}

var (
	oauthStateMu sync.Mutex
	oauthStates  = map[*MockRepository]*mockOAuthState{}
)

func oauthStateFor(m *MockRepository) *mockOAuthState {
	oauthStateMu.Lock()
	defer oauthStateMu.Unlock()
	s, ok := oauthStates[m]
	if !ok {
		s = &mockOAuthState{
			providers: map[string]*oauth.ProviderConfig{},
			grants:    map[string]*oauth.Grant{},
			tokens:    map[string]struct{ access, refresh string }{},
			clientSec: map[string]string{},
		}
		oauthStates[m] = s
	}
	return s
}

func (s *mockOAuthState) nextProviderID() string {
	s.nextID++
	return fmt.Sprintf("prov-%d", s.nextID)
}
func (s *mockOAuthState) nextGrantID() string {
	s.nextID++
	return fmt.Sprintf("grant-%d", s.nextID)
}

var errOAuthMockNotImplemented = errors.New("oauth method not implemented on test mock")

// --- MockRepository: real in-memory OAuth implementation ---

func (m *MockRepository) GetProviderConfig(_ context.Context, tenantID, providerID string) (*oauth.ProviderConfig, error) {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	cfg, ok := st.providers[providerID]
	if !ok || cfg.TenantID != tenantID {
		return nil, oauth.ErrProviderNotFound
	}
	// return a copy so callers' mutations don't bleed into the store
	cp := *cfg
	return &cp, nil
}

func (m *MockRepository) ResolveClientSecret(_ context.Context, cfg *oauth.ProviderConfig) (string, error) {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	if s, ok := st.clientSec[cfg.ID]; ok {
		return s, nil
	}
	return "", errors.New("no client secret stored")
}

func (m *MockRepository) CreateGrant(_ context.Context, g *oauth.Grant, accessTok, refreshTok string) error {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	g.ID = st.nextGrantID()
	cp := *g
	st.grants[g.ID] = &cp
	st.tokens[g.ID] = struct{ access, refresh string }{accessTok, refreshTok}
	return nil
}

func (m *MockRepository) UpdateTokens(_ context.Context, grantID, accessTok, refreshTok string, expiresAt *time.Time) error {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	g, ok := st.grants[grantID]
	if !ok {
		return oauth.ErrGrantNotFound
	}
	st.tokens[grantID] = struct{ access, refresh string }{accessTok, refreshTok}
	now := time.Now()
	g.ExpiresAt = expiresAt
	g.LastRefreshedAt = &now
	return nil
}

func (m *MockRepository) GetGrant(_ context.Context, tenantID, grantID string) (*oauth.Grant, error) {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	g, ok := st.grants[grantID]
	if !ok || g.TenantID != tenantID {
		return nil, oauth.ErrGrantNotFound
	}
	cp := *g
	tok := st.tokens[grantID]
	cp.AccessToken = tok.access
	cp.RefreshToken = tok.refresh
	return &cp, nil
}

func (m *MockRepository) GetGrantMeta(ctx context.Context, tenantID, grantID string) (*oauth.Grant, error) {
	g, err := m.GetGrant(ctx, tenantID, grantID)
	if err != nil {
		return nil, err
	}
	g.AccessToken = ""
	g.RefreshToken = ""
	return g, nil
}

func (m *MockRepository) ListGrants(_ context.Context, tenantID string) ([]*oauth.Grant, error) {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []*oauth.Grant
	for _, g := range st.grants {
		if g.TenantID == tenantID {
			cp := *g
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MockRepository) MarkRevoked(_ context.Context, tenantID, grantID string) error {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	g, ok := st.grants[grantID]
	if !ok || g.TenantID != tenantID {
		return oauth.ErrGrantNotFound
	}
	now := time.Now()
	g.RevokedAt = &now
	return nil
}

func (m *MockRepository) MarkRefreshFailure(_ context.Context, tenantID, grantID, reason string) error {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	g, ok := st.grants[grantID]
	if !ok || g.TenantID != tenantID {
		return nil
	}
	now := time.Now()
	g.RefreshFailedAt = &now
	g.RefreshFailureReason = reason
	return nil
}

func (m *MockRepository) ScanExpiring(_ context.Context, within time.Duration, _ int) ([]string, error) {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	cutoff := time.Now().Add(within)
	var out []string
	for id, g := range st.grants {
		if g.RevokedAt != nil || g.ExpiresAt == nil {
			continue
		}
		if g.ExpiresAt.Before(cutoff) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (m *MockRepository) CreateOAuthProvider(_ context.Context, cfg *oauth.ProviderConfig, clientSecret string) error {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	cfg.ID = st.nextProviderID()
	cfg.ClientSecretID = "secret-" + cfg.ID
	cp := *cfg
	st.providers[cfg.ID] = &cp
	st.clientSec[cfg.ID] = clientSecret
	return nil
}

func (m *MockRepository) UpdateOAuthProvider(_ context.Context, cfg *oauth.ProviderConfig, newClientSecret string) error {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	existing, ok := st.providers[cfg.ID]
	if !ok || existing.TenantID != cfg.TenantID {
		return oauth.ErrProviderNotFound
	}
	cp := *cfg
	st.providers[cfg.ID] = &cp
	if newClientSecret != "" {
		st.clientSec[cfg.ID] = newClientSecret
	}
	return nil
}

func (m *MockRepository) DeleteOAuthProvider(_ context.Context, tenantID, providerID string) error {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	p, ok := st.providers[providerID]
	if !ok || p.TenantID != tenantID {
		return oauth.ErrProviderNotFound
	}
	for _, g := range st.grants {
		if g.ProviderID == providerID && g.RevokedAt == nil {
			return errors.New("cannot delete provider with active grants")
		}
	}
	delete(st.providers, providerID)
	delete(st.clientSec, providerID)
	return nil
}

func (m *MockRepository) ListOAuthProviders(_ context.Context, tenantID string) ([]*oauth.ProviderConfig, error) {
	st := oauthStateFor(m)
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []*oauth.ProviderConfig
	for _, p := range st.providers {
		if p.TenantID == tenantID {
			cp := *p
			out = append(out, &cp)
		}
	}
	return out, nil
}

// --- rbacMock keeps not-implemented stubs (rbac_test.go doesn't exercise OAuth) ---

func (m *rbacMock) GetProviderConfig(ctx context.Context, tenantID, providerID string) (*oauth.ProviderConfig, error) {
	return nil, errOAuthMockNotImplemented
}
func (m *rbacMock) ResolveClientSecret(ctx context.Context, cfg *oauth.ProviderConfig) (string, error) {
	return "", errOAuthMockNotImplemented
}
func (m *rbacMock) CreateGrant(ctx context.Context, g *oauth.Grant, accessTok, refreshTok string) error {
	return errOAuthMockNotImplemented
}
func (m *rbacMock) UpdateTokens(ctx context.Context, grantID, accessTok, refreshTok string, expiresAt *time.Time) error {
	return errOAuthMockNotImplemented
}
func (m *rbacMock) GetGrant(ctx context.Context, tenantID, grantID string) (*oauth.Grant, error) {
	return nil, errOAuthMockNotImplemented
}
func (m *rbacMock) GetGrantMeta(ctx context.Context, tenantID, grantID string) (*oauth.Grant, error) {
	return nil, errOAuthMockNotImplemented
}
func (m *rbacMock) ListGrants(ctx context.Context, tenantID string) ([]*oauth.Grant, error) {
	return nil, errOAuthMockNotImplemented
}
func (m *rbacMock) MarkRevoked(ctx context.Context, tenantID, grantID string) error {
	return errOAuthMockNotImplemented
}
func (m *rbacMock) MarkRefreshFailure(ctx context.Context, tenantID, grantID, reason string) error {
	return errOAuthMockNotImplemented
}
func (m *rbacMock) ScanExpiring(ctx context.Context, within time.Duration, limit int) ([]string, error) {
	return nil, errOAuthMockNotImplemented
}
func (m *rbacMock) CreateOAuthProvider(ctx context.Context, cfg *oauth.ProviderConfig, clientSecret string) error {
	return errOAuthMockNotImplemented
}
func (m *rbacMock) UpdateOAuthProvider(ctx context.Context, cfg *oauth.ProviderConfig, newClientSecret string) error {
	return errOAuthMockNotImplemented
}
func (m *rbacMock) DeleteOAuthProvider(ctx context.Context, tenantID, providerID string) error {
	return errOAuthMockNotImplemented
}
func (m *rbacMock) ListOAuthProviders(ctx context.Context, tenantID string) ([]*oauth.ProviderConfig, error) {
	return nil, errOAuthMockNotImplemented
}
