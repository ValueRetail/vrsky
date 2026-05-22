package managementapi

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

// Per-tenant quotas (Phase 1I / #74).
//
// Three resource axes are tracked:
//
//	max_msg_per_sec   token-bucket enforced in-process (refilled every
//	                  second), checked from test-message + auto-generator
//	                  + any caller that wants to bound a tenant's burst
//	max_integrations  count of active connections; checked on create
//	max_storage_bytes hourly background job computes usage and flips
//	                  storage_exceeded; uploads fast-fail on the flag
//
// All three are also visible to the UI via GET /api/v1/tenants/{id}/quotas
// (any member) and configurable by owners via PUT.

// TenantQuotas mirrors one row of the tenant_quotas table.
type TenantQuotas struct {
	TenantID         string    `json:"tenant_id"`
	PlanName         string    `json:"plan_name"`
	MaxMsgPerSec     int       `json:"max_msg_per_sec"`
	MaxIntegrations  int       `json:"max_integrations"`
	MaxStorageBytes  int64     `json:"max_storage_bytes"`
	StorageBytes     int64     `json:"storage_bytes"`
	StorageExceeded  bool      `json:"storage_exceeded"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ErrQuotaExceeded is the sentinel returned by every Check* function in
// this package; handlers translate it to 429.
var ErrQuotaExceeded = errors.New("quota exceeded")

// QuotaRetryAfter is the Retry-After hint surfaced via the response
// header when the message-rate bucket is empty. Aligned with the bucket
// refill interval so the next attempt has a real chance of succeeding.
const QuotaRetryAfter = 1 * time.Second

// ===== Repository =====

// GetTenantQuotas reads the (cached-by-caller) quota row for a tenant.
func (r *PostgresRepository) GetTenantQuotas(ctx context.Context, tenantID string) (*TenantQuotas, error) {
	q := &TenantQuotas{TenantID: tenantID}
	err := r.db.QueryRowContext(ctx, `
		SELECT plan_name, max_msg_per_sec, max_integrations, max_storage_bytes,
		       storage_bytes, storage_exceeded, updated_at
		FROM tenant_quotas WHERE tenant_id = $1
	`, tenantID).Scan(
		&q.PlanName, &q.MaxMsgPerSec, &q.MaxIntegrations, &q.MaxStorageBytes,
		&q.StorageBytes, &q.StorageExceeded, &q.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		// Auto-create a default row so newly-onboarded tenants don't 404
		// the first time the middleware checks them.
		_, _ = r.db.ExecContext(ctx,
			`INSERT INTO tenant_quotas (tenant_id) VALUES ($1) ON CONFLICT (tenant_id) DO NOTHING`,
			tenantID,
		)
		return r.GetTenantQuotas(ctx, tenantID)
	}
	if err != nil {
		return nil, err
	}
	return q, nil
}

// UpdateTenantQuotas overwrites the configurable fields for one tenant.
// storage_bytes and storage_exceeded are excluded; they're maintained by
// the background job.
func (r *PostgresRepository) UpdateTenantQuotas(ctx context.Context, q *TenantQuotas) error {
	return r.db.QueryRowContext(ctx, `
		UPDATE tenant_quotas
		   SET plan_name         = $2,
		       max_msg_per_sec   = $3,
		       max_integrations  = $4,
		       max_storage_bytes = $5,
		       updated_at        = NOW()
		 WHERE tenant_id = $1
		RETURNING storage_bytes, storage_exceeded, updated_at
	`, q.TenantID, q.PlanName, q.MaxMsgPerSec, q.MaxIntegrations, q.MaxStorageBytes).Scan(
		&q.StorageBytes, &q.StorageExceeded, &q.UpdatedAt,
	)
}

// SetTenantStorageUsage is called by the hourly job. It updates the
// observed bytes count and toggles the storage_exceeded flag based on
// whether the new value crosses max_storage_bytes.
func (r *PostgresRepository) SetTenantStorageUsage(ctx context.Context, tenantID string, bytes int64) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE tenant_quotas
		   SET storage_bytes    = $2,
		       storage_exceeded = (CASE WHEN $2 > max_storage_bytes THEN TRUE ELSE FALSE END),
		       updated_at       = NOW()
		 WHERE tenant_id = $1
	`, tenantID, bytes)
	return err
}

// CountActiveIntegrations returns how many connections currently exist
// for the tenant. Used by CheckIntegrationCount.
func (r *PostgresRepository) CountActiveIntegrations(ctx context.Context, tenantID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT count(*) FROM connections WHERE tenant_id = $1`,
		tenantID,
	).Scan(&n)
	return n, err
}

// ===== In-memory token bucket =====
//
// One bucket per tenant. Each bucket holds at most max_msg_per_sec
// tokens and refills fully every second. We deliberately don't share
// this across replicas — the bucket lives only inside the API process
// it was created in. A multi-replica deployment would need Redis (or a
// JetStream KV bucket) keyed by tenant_id; that swap is documented in
// the issue body and tracked as a Phase 2 follow-up.

type tokenBucket struct {
	mu         sync.Mutex
	tokens     int
	capacity   int
	lastRefill time.Time
}

func (b *tokenBucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Refill on demand: full bucket every wall-clock second.
	if elapsed := time.Since(b.lastRefill); elapsed >= time.Second {
		b.tokens = b.capacity
		b.lastRefill = time.Now()
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

// QuotaTracker is the live in-memory side of the quota system. One
// instance lives on the Handler and is shared across request handlers.
type QuotaTracker struct {
	mu       sync.Mutex
	buckets  map[string]*tokenBucket
	capacity map[string]int // last-seen max for each tenant; resets the bucket on change
}

// NewQuotaTracker constructs an empty tracker. Buckets are created lazily
// the first time a tenant calls CheckMessageRate.
func NewQuotaTracker() *QuotaTracker {
	return &QuotaTracker{
		buckets:  map[string]*tokenBucket{},
		capacity: map[string]int{},
	}
}

// CheckMessageRate returns nil if the tenant has tokens to spare,
// ErrQuotaExceeded otherwise. Maximum is taken from quotas; if max is
// <= 0 the check is bypassed (interpreted as "unlimited").
func (t *QuotaTracker) CheckMessageRate(tenantID string, q *TenantQuotas) error {
	if q.MaxMsgPerSec <= 0 {
		return nil
	}
	t.mu.Lock()
	b, ok := t.buckets[tenantID]
	if !ok || t.capacity[tenantID] != q.MaxMsgPerSec {
		// First time seeing this tenant, or the limit changed underneath
		// us — start with a full bucket.
		b = &tokenBucket{tokens: q.MaxMsgPerSec, capacity: q.MaxMsgPerSec, lastRefill: time.Now()}
		t.buckets[tenantID] = b
		t.capacity[tenantID] = q.MaxMsgPerSec
	}
	t.mu.Unlock()
	if !b.take() {
		return ErrQuotaExceeded
	}
	return nil
}

// CheckIntegrationCount refuses if creating one more connection would
// take the tenant over max_integrations. The current count is fetched
// from the connections table on each call — accurate but not free; the
// caller should call this only on create paths, not in a hot loop.
func (t *QuotaTracker) CheckIntegrationCount(ctx context.Context, repo Repository, tenantID string, q *TenantQuotas) error {
	if q.MaxIntegrations <= 0 {
		return nil
	}
	n, err := repo.CountActiveIntegrations(ctx, tenantID)
	if err != nil {
		// Don't refuse on a DB hiccup — failing closed here would punish
		// the user for our infrastructure failures. The audit log keeps
		// the trail.
		return nil
	}
	if n >= q.MaxIntegrations {
		return ErrQuotaExceeded
	}
	return nil
}

// CheckStorage refuses uploads when the hourly job has marked the tenant
// as over their storage budget.
func (t *QuotaTracker) CheckStorage(q *TenantQuotas) error {
	if q.MaxStorageBytes <= 0 {
		return nil
	}
	if q.StorageExceeded {
		return ErrQuotaExceeded
	}
	return nil
}
