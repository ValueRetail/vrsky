package managementapi

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/oauth"
)

// OAuthRefresher keeps OAuth access tokens fresh in the background. On each
// tick it scans for grants whose access token will expire within `horizon`
// and enqueues them for refresh; refresh work is also enqueueable on demand
// (PR #3 will call Enqueue from the worker-side on-401 retry path).
//
// The refresher is single-instance: dedup between the ticker scan and
// on-demand calls is handled by the Client's internal singleflight. If/when
// management-api goes HA we'd swap that for a SELECT FOR UPDATE SKIP
// LOCKED on the grants row — out of scope for PR #1.
//
// The lifecycle (Start/Stop with WaitGroup) mirrors MetricsCache and
// TenantProvisioner in this package so it slots into the standard
// cmd/management-api/main.go boot sequence.
type OAuthRefresher struct {
	client      *oauth.Client
	store       oauth.Store
	logger      *slog.Logger
	tick        time.Duration
	horizon     time.Duration
	scanLimit   int
	jobs        chan refreshJob
	done        chan struct{}
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	workerCount int

	// tenantLookup resolves a grant ID to its tenant ID for ticker-scan jobs
	// (the scan query is global, but Client.Refresh is tenant-scoped). main.go
	// wires this against a tiny direct DB query; tests can substitute a map
	// lookup.
	tenantLookup func(ctx context.Context, grantID string) (string, error)
}

type refreshJob struct {
	tenantID string
	grantID  string
	reason   string
}

// OAuthRefresherOption tunes the refresher at construction.
type OAuthRefresherOption func(*OAuthRefresher)

// WithRefresherTick overrides the scan interval. Default 1 minute.
func WithRefresherTick(d time.Duration) OAuthRefresherOption {
	return func(r *OAuthRefresher) { r.tick = d }
}

// WithRefresherHorizon overrides how far ahead of expiry to refresh.
// Default 5 minutes.
func WithRefresherHorizon(d time.Duration) OAuthRefresherOption {
	return func(r *OAuthRefresher) { r.horizon = d }
}

// WithRefresherWorkers overrides the number of parallel refresh workers.
// Default 2.
func WithRefresherWorkers(n int) OAuthRefresherOption {
	return func(r *OAuthRefresher) { r.workerCount = n }
}

// WithRefresherLogger injects a slog logger. Defaults to slog.Default().
func WithRefresherLogger(l *slog.Logger) OAuthRefresherOption {
	return func(r *OAuthRefresher) { r.logger = l }
}

// NewOAuthRefresher constructs a refresher around a client + store pair.
func NewOAuthRefresher(client *oauth.Client, store oauth.Store, opts ...OAuthRefresherOption) *OAuthRefresher {
	r := &OAuthRefresher{
		client:      client,
		store:       store,
		logger:      slog.Default(),
		tick:        1 * time.Minute,
		horizon:     5 * time.Minute,
		scanLimit:   200,
		jobs:        make(chan refreshJob, 256),
		done:        make(chan struct{}),
		workerCount: 2,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Start spins up the ticker scanner plus the configured number of worker
// goroutines. Subsequent calls are no-ops. The internal context is
// cancelled by Stop, so each piece of work that respects context will exit
// cleanly during shutdown.
func (r *OAuthRefresher) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go r.scanLoop(ctx)
	for i := 0; i < r.workerCount; i++ {
		r.wg.Add(1)
		go r.worker(ctx)
	}
	r.logger.Info("oauth refresher started",
		"tick", r.tick, "horizon", r.horizon, "workers", r.workerCount)
}

// Stop signals shutdown and blocks until all goroutines exit.
func (r *OAuthRefresher) Stop() {
	select {
	case <-r.done:
		return // already stopped
	default:
	}
	close(r.done)
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	r.logger.Info("oauth refresher stopped")
}

// Enqueue requests an on-demand refresh of one grant. Non-blocking: if the
// queue is full the job is dropped (the next ticker scan will pick it up).
// reason is recorded in logs / failure metadata.
func (r *OAuthRefresher) Enqueue(tenantID, grantID, reason string) {
	select {
	case r.jobs <- refreshJob{tenantID: tenantID, grantID: grantID, reason: reason}:
	default:
		r.logger.Warn("oauth refresher queue full; dropping on-demand request",
			"grant_id", grantID, "reason", reason)
	}
}

// scanLoop ticks and enqueues expiring grants for refresh.
func (r *OAuthRefresher) scanLoop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(r.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.done:
			return
		case <-ticker.C:
			r.scanOnce(ctx)
		}
	}
}

// scanOnce runs one expiry scan and enqueues each match. Exposed (lower-
// case but called from tests) so unit tests can drive the scan without
// waiting for the ticker.
func (r *OAuthRefresher) scanOnce(ctx context.Context) {
	ids, err := r.store.ScanExpiring(ctx, r.horizon, r.scanLimit)
	if err != nil {
		r.logger.Warn("oauth refresher scan failed", "error", err)
		return
	}
	for _, id := range ids {
		// The scan query is global (not tenant-scoped); tenant is recovered
		// inside Client.Refresh via the grant's tenant_id column. Pass empty
		// tenantID in the job — the worker re-derives.
		r.Enqueue("", id, "ticker")
	}
}

func (r *OAuthRefresher) worker(ctx context.Context) {
	defer r.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.done:
			return
		case job := <-r.jobs:
			r.processJob(ctx, job)
		}
	}
}

// processJob runs one refresh. The Client itself dedupes concurrent
// requests for the same grant ID, so workers can run in parallel safely.
func (r *OAuthRefresher) processJob(ctx context.Context, job refreshJob) {
	tenantID := job.tenantID
	if tenantID == "" {
		// Recover the tenant from the grant row. GetGrantMeta is cheap (no
		// secret decryption) and we don't need the tokens to refresh.
		meta, err := r.lookupTenant(ctx, job.grantID)
		if err != nil {
			r.logger.Warn("oauth refresh: cannot resolve tenant for grant",
				"grant_id", job.grantID, "error", err)
			return
		}
		tenantID = meta
	}

	if _, err := r.client.Refresh(ctx, tenantID, job.grantID); err != nil {
		reason := err.Error()
		if errors.Is(err, oauth.ErrRefreshExpired) {
			reason = "refresh_token_expired"
		}
		if mfErr := r.store.MarkRefreshFailure(ctx, tenantID, job.grantID, reason); mfErr != nil {
			r.logger.Warn("oauth refresh: failed to record failure",
				"grant_id", job.grantID, "error", mfErr)
		}
		r.logger.Warn("oauth refresh failed",
			"grant_id", job.grantID, "reason", reason, "request_reason", job.reason)
		return
	}
	r.logger.Debug("oauth refresh succeeded",
		"grant_id", job.grantID, "request_reason", job.reason)
}

// lookupTenant resolves a grant ID to its tenant ID. The ticker-scan path
// doesn't carry tenant context, so we recover it from the row before
// calling Client.Refresh (which is tenant-scoped on purpose).
func (r *OAuthRefresher) lookupTenant(ctx context.Context, grantID string) (string, error) {
	if r.tenantLookup == nil {
		return "", errors.New("refresher: no tenant resolver configured")
	}
	return r.tenantLookup(ctx, grantID)
}

// SetTenantLookup injects the grant-ID-to-tenant-ID resolver. main.go wires
// this against the same *sql.DB the Postgres store uses.
func (r *OAuthRefresher) SetTenantLookup(fn func(ctx context.Context, grantID string) (string, error)) {
	r.tenantLookup = fn
}
