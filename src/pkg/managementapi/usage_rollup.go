package managementapi

import (
	"context"
	"fmt"
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

// Counters the rollup snapshots. increase() is reset-aware, so it survives
// worker/API restarts that zero the counter.
const (
	usageMessagesMetric = "vrsky_messages_published_total"
	usageDeploysMetric  = "vrsky_connection_deploys_total"
)

// dayDeltaQuery builds a PromQL instant query for a counter's increase over the
// window (windowSec) ending at endUnix, summed by tenant_id. Anchoring the
// window to a calendar-day boundary with the @ modifier — rather than a rolling
// [24h] — keeps each usage_daily row scoped to exactly one UTC day, which is
// what billing needs.
func dayDeltaQuery(metric string, windowSec, endUnix int64) string {
	return fmt.Sprintf("sum by (tenant_id) (increase(%s[%ds] @ %d))", metric, windowSec, endUnix)
}

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

// runOnce refreshes today's row (midnight → now) and finalizes yesterday's full
// calendar day. Running hourly keeps today's totals fresh and converging to the
// true day total; finalizing yesterday closes the midnight gap so a complete
// day is never left as a partial rolling window. Errors are logged, not fatal —
// a transient Prometheus or DB blip should not kill the loop.
func (u *UsageRollup) runOnce(ctx context.Context) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)

	storage, err := u.repo.ListTenantStorage(ctx)
	if err != nil {
		u.logger.Error("usage rollup: list tenant storage", "error", err)
		return
	}

	// Today: window [midnight, now]; storage = current snapshot.
	todayWindow := int64(now.Sub(today).Seconds())
	if todayWindow < 1 {
		todayWindow = 1
	}
	u.rollupDay(ctx, today, todayWindow, now.Unix(), storage, true)

	// Yesterday: full day [yesterday-midnight, today-midnight]. Counter deltas
	// are a fixed past window (stable across re-runs); storage is preserved from
	// yesterday's own last snapshot rather than overwritten with today's.
	u.rollupDay(ctx, yesterday, 86400, today.Unix(), storage, false)
}

// rollupDay upserts one day's row for every tenant. windowSec/endUnix define the
// counter-delta window. When setStorage is true the current storage snapshot is
// written; otherwise the day's existing stored storage is preserved (so
// finalizing a past day doesn't clobber it with today's value).
func (u *UsageRollup) rollupDay(ctx context.Context, day time.Time, windowSec, endUnix int64, storage map[string]int64, setStorage bool) {
	var messages, deploys map[string]float64
	if u.prom != nil {
		var err error
		if messages, err = u.prom.QueryByLabel(ctx, dayDeltaQuery(usageMessagesMetric, windowSec, endUnix), "tenant_id"); err != nil {
			u.logger.Warn("usage rollup: query messages", "day", day.Format("2006-01-02"), "error", err)
			messages = map[string]float64{}
		}
		if deploys, err = u.prom.QueryByLabel(ctx, dayDeltaQuery(usageDeploysMetric, windowSec, endUnix), "tenant_id"); err != nil {
			u.logger.Warn("usage rollup: query deploys", "day", day.Format("2006-01-02"), "error", err)
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
		storageVal := storage[id]
		if !setStorage {
			// Preserve the day's own stored storage if a row already exists.
			if rows, err := u.repo.ListUsageDaily(ctx, id, day, day); err == nil && len(rows) == 1 {
				storageVal = rows[0].StorageBytes
			}
		}
		if err := u.repo.UpsertUsageDaily(ctx, id, day,
			roundCounter(messages[id]), roundCounter(deploys[id]), storageVal); err != nil {
			u.logger.Error("usage rollup: upsert", "tenant_id", id, "day", day.Format("2006-01-02"), "error", err)
			failed++
			continue
		}
		written++
	}
	u.logger.Info("usage rollup day complete", "day", day.Format("2006-01-02"),
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
