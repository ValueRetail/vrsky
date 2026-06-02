package managementapi

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/oauth"
)

// trackStore is a minimal oauth.Store used only by these refresher tests.
// It tracks calls so the test can assert behaviour without a real DB or
// the full sqlmock setup (those live in pkg/oauth and in verification).
type trackStore struct {
	mu              sync.Mutex
	expiringIDs     []string         // returned by ScanExpiring
	scanCalls       int64
	updateCalls     int64
	markFailureMap  map[string]string // grantID -> recorded reason
	tenantByGrant   map[string]string

	// What Refresh should do for each grantID. nil = success.
	refreshErr map[string]error
}

func newTrackStore() *trackStore {
	return &trackStore{
		markFailureMap: map[string]string{},
		tenantByGrant:  map[string]string{},
		refreshErr:     map[string]error{},
	}
}

func (s *trackStore) GetProviderConfig(ctx context.Context, tenantID, providerID string) (*oauth.ProviderConfig, error) {
	return &oauth.ProviderConfig{
		ID: providerID, TenantID: tenantID, ProviderType: "fake", ClientID: "c", AuthURL: "u", TokenURL: "t",
	}, nil
}
func (s *trackStore) ResolveClientSecret(ctx context.Context, cfg *oauth.ProviderConfig) (string, error) {
	return "secret", nil
}
func (s *trackStore) CreateGrant(ctx context.Context, g *oauth.Grant, accessTok, refreshTok string) error {
	return nil
}
func (s *trackStore) UpdateTokens(ctx context.Context, grantID, accessTok, refreshTok string, expiresAt *time.Time) error {
	atomic.AddInt64(&s.updateCalls, 1)
	return nil
}
func (s *trackStore) GetGrant(ctx context.Context, tenantID, grantID string) (*oauth.Grant, error) {
	// The refresher calls Refresh -> refreshLocked -> GetGrant. We need a
	// grant with a refresh token so refreshLocked doesn't bail early.
	// We then fake the outcome of the provider exchange via refreshErr by
	// failing here when configured.
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.refreshErr[grantID]; ok {
		return nil, err
	}
	now := time.Now().Add(-time.Minute) // expired so NeedsRefresh = true
	return &oauth.Grant{
		ID: grantID, TenantID: tenantID, ProviderID: "prov", ProviderType: "fake",
		AccessToken: "old", RefreshToken: "r", ExpiresAt: &now,
	}, nil
}
func (s *trackStore) GetGrantMeta(ctx context.Context, tenantID, grantID string) (*oauth.Grant, error) {
	g, err := s.GetGrant(ctx, tenantID, grantID)
	if err != nil {
		return nil, err
	}
	g.AccessToken = ""
	g.RefreshToken = ""
	return g, nil
}
func (s *trackStore) ListGrants(ctx context.Context, tenantID string) ([]*oauth.Grant, error) {
	return nil, nil
}
func (s *trackStore) MarkRevoked(ctx context.Context, tenantID, grantID string) error { return nil }
func (s *trackStore) MarkRefreshFailure(ctx context.Context, tenantID, grantID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markFailureMap[grantID] = reason
	return nil
}
func (s *trackStore) ScanExpiring(ctx context.Context, within time.Duration, limit int) ([]string, error) {
	atomic.AddInt64(&s.scanCalls, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := append([]string(nil), s.expiringIDs...)
	return ids, nil
}

// Build a refresher whose underlying Client is configured against a Store
// that responds to "refresh" by returning the configured error (so the
// test can drive failure cases without standing up an HTTP server). We do
// this by intercepting GetGrant: the refreshErr map there short-circuits.
func newTestRefresher(t *testing.T, store *trackStore) (*OAuthRefresher, *oauth.Client) {
	t.Helper()
	reg := oauth.NewProviderRegistry()
	reg.Register(oauth.Provider{Type: "fake", SupportsRefresh: true})
	client := oauth.New(store, reg)
	r := NewOAuthRefresher(client, store,
		WithRefresherTick(20*time.Millisecond),
		WithRefresherHorizon(5*time.Minute),
		WithRefresherWorkers(2),
	)
	r.SetTenantLookup(func(ctx context.Context, grantID string) (string, error) {
		store.mu.Lock()
		defer store.mu.Unlock()
		if tid, ok := store.tenantByGrant[grantID]; ok {
			return tid, nil
		}
		return "tenant-1", nil
	})
	return r, client
}

func TestRefresher_StartStop_IsClean(t *testing.T) {
	store := newTrackStore()
	r, _ := newTestRefresher(t, store)
	r.Start()
	// give the scan loop a chance to fire at least once
	time.Sleep(60 * time.Millisecond)
	r.Stop()
	// Stop is idempotent.
	r.Stop()

	if atomic.LoadInt64(&store.scanCalls) == 0 {
		t.Errorf("expected at least one ScanExpiring call before Stop")
	}
}

// When the underlying Client.Refresh returns ErrRefreshExpired, the refresher
// records a specific "refresh_token_expired" reason via MarkRefreshFailure
// so the UI can flag "Reconnect required".
func TestRefresher_RecordsRefreshExpired(t *testing.T) {
	store := newTrackStore()
	store.refreshErr["g-1"] = oauth.ErrRefreshExpired

	r, _ := newTestRefresher(t, store)
	r.Start()
	defer r.Stop()

	r.Enqueue("tenant-1", "g-1", "test")

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		reason, ok := store.markFailureMap["g-1"]
		store.mu.Unlock()
		if ok && reason == "refresh_token_expired" {
			return // happy path
		}
		time.Sleep(10 * time.Millisecond)
	}
	store.mu.Lock()
	reason := store.markFailureMap["g-1"]
	store.mu.Unlock()
	t.Errorf("expected reason=refresh_token_expired, got %q", reason)
}

// Other refresh errors get the raw error string recorded — useful so an
// operator can see "provider 503" rather than a generic flag.
func TestRefresher_RecordsGenericProviderError(t *testing.T) {
	store := newTrackStore()
	store.refreshErr["g-2"] = errors.New("network unreachable")

	r, _ := newTestRefresher(t, store)
	r.Start()
	defer r.Stop()
	r.Enqueue("tenant-1", "g-2", "test")

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		reason, ok := store.markFailureMap["g-2"]
		store.mu.Unlock()
		if ok && reason != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("MarkRefreshFailure not called for failing grant")
}

// Ticker scan enqueues expiring IDs which workers pick up. ScanExpiring is
// global (no tenant), so the ticker path must use SetTenantLookup to
// resolve a grant ID to its tenant before calling Client.Refresh.
func TestRefresher_TickerScansAndEnqueues(t *testing.T) {
	store := newTrackStore()
	store.expiringIDs = []string{"g-tick-1"}
	store.tenantByGrant["g-tick-1"] = "tenant-A"
	store.refreshErr["g-tick-1"] = errors.New("known fail") // simplifies assertion

	r, _ := newTestRefresher(t, store)
	r.Start()
	defer r.Stop()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		_, ok := store.markFailureMap["g-tick-1"]
		store.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("ticker did not lead to a refresh attempt on the expiring grant")
}

// Enqueue is non-blocking even when the queue is full. We achieve this by
// constructing a refresher with a tiny buffer via direct field set (the
// channel is created in NewOAuthRefresher). Easier: spam a real refresher
// and trust that 1024 enqueues don't deadlock.
func TestRefresher_EnqueueIsNonBlocking(t *testing.T) {
	store := newTrackStore()
	r, _ := newTestRefresher(t, store)
	// Don't Start — full queue, nothing draining. Enqueue should never block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1024; i++ {
			r.Enqueue("tenant-1", "g", "spam")
		}
		close(done)
	}()
	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Errorf("Enqueue blocked — channel send wasn't guarded by default branch")
	}
}
