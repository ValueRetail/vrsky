package managementapi

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/promquery"
)

// UsageRollup snapshots per-tenant usage into usage_daily on a timer (Phase 4A /
// #92). Each run upserts the current UTC day: message + deploy counts come from
// Prometheus (increase() over the trailing 24h), storage from the tenant_quotas
// snapshot. Running hourly keeps the live day's "this month" totals fresh and
// re-derives the current day after a restart; prior days already persisted in
// usage_daily are the durable record.
//
// The lifecycle (Start/Stop with WaitGroup + cancel) mirrors OAuthRefresher so
// it slots into the standard cmd/management-api/main.go boot sequence.
type UsageRollup struct {
	repo   Repository
	prom   *promquery.Client // nil → storage-only rollup (no Prometheus configured)
	tick   time.Duration
	logger *slog.Logger
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// PromQL the rollup evaluates. increase() over 24h gives the day's delta and is
// resilient to counter resets on worker restart.
const (
	usageMessagesQuery = `sum by (tenant_id) (increase(vrsky_messages_published_total[24h]))`
	usageDeploysQuery  = `sum by (tenant_id) (increase(vrsky_connection_deploys_total[24h]))`
)

// UsageRollupOption tunes the rollup at construction.
type UsageRollupOption func(*UsageRollup)

// WithRollupTick overrides the rollup interval. Default 1 hour.
func WithRollupTick(d time.Duration) UsageRollupOption {
	return func(u *UsageRollup) { u.tick = d }
}

// WithRollupLogger sets the logger. Default slog.Default().
func WithRollupLogger(l *slog.Logger) UsageRollupOption {
	return func(u *UsageRollup) { u.logger = l }
}

// NewUsageRollup builds a rollup. prom may be nil (storage-only).
func NewUsageRollup(repo Repository, prom *promquery.Client, opts ...UsageRollupOption) *UsageRollup {
	u := &UsageRollup{
		repo:   repo,
		prom:   prom,
		tick:   time.Hour,
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

// Start runs an immediate rollup then repeats every tick until Stop.
func (u *UsageRollup) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	u.cancel = cancel
	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		t := time.NewTicker(u.tick)
		defer t.Stop()
		u.runOnce(ctx) // seed immediately so the dashboard isn't empty on boot
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				u.runOnce(ctx)
			}
		}
	}()
}

// Stop cancels the loop and waits for the in-flight run to finish.
func (u *UsageRollup) Stop() {
	if u.cancel != nil {
		u.cancel()
	}
	u.wg.Wait()
}

// runOnce upserts every tenant's row for the current UTC day. Errors are logged,
// not fatal — a transient Prometheus or DB blip should not kill the loop.
func (u *UsageRollup) runOnce(ctx context.Context) {
	day := time.Now().UTC()

	storage, err := u.repo.ListTenantStorage(ctx)
	if err != nil {
		u.logger.Error("usage rollup: list tenant storage", "error", err)
		return
	}

	var messages, deploys map[string]float64
	if u.prom != nil {
		if messages, err = u.prom.QueryByLabel(ctx, usageMessagesQuery, "tenant_id"); err != nil {
			u.logger.Warn("usage rollup: query messages", "error", err)
			messages = map[string]float64{}
		}
		if deploys, err = u.prom.QueryByLabel(ctx, usageDeploysQuery, "tenant_id"); err != nil {
			u.logger.Warn("usage rollup: query deploys", "error", err)
			deploys = map[string]float64{}
		}
	}

	// Tenant set = union of storage rows + any tenant seen in the metrics.
	tenants := make(map[string]struct{}, len(storage))
	for id := range storage {
		tenants[id] = struct{}{}
	}
	for id := range messages {
		tenants[id] = struct{}{}
	}
	for id := range deploys {
		tenants[id] = struct{}{}
	}

	var written, failed int
	for id := range tenants {
		if err := u.repo.UpsertUsageDaily(ctx, id, day,
			roundCounter(messages[id]), roundCounter(deploys[id]), storage[id]); err != nil {
			u.logger.Error("usage rollup: upsert", "tenant_id", id, "error", err)
			failed++
			continue
		}
		written++
	}
	u.logger.Info("usage rollup complete", "day", day.Format("2006-01-02"),
		"tenants", written, "failed", failed, "prometheus", u.prom != nil)
}

// roundCounter rounds a non-negative Prometheus increase() value to a count.
// increase() can yield small fractional values from interpolation across scrape
// boundaries; rounding gives a clean message/deploy count.
func roundCounter(v float64) int64 {
	if v <= 0 || math.IsNaN(v) {
		return 0
	}
	return int64(math.Round(v))
}
