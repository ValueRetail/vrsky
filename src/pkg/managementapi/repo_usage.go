package managementapi

import (
	"context"
	"time"
)

// Per-tenant usage metering (Phase 4A / #92).
//
// usage_daily holds one row per (tenant, UTC day) with message + deploy counts
// and a storage snapshot. The rollup job (usage_rollup.go) upserts the current
// day hourly; the UI/API read current-month totals + daily rows from here. See
// the 000016_usage_daily migration.

// UsageDaily is one row of the usage_daily table. Day is formatted YYYY-MM-DD.
type UsageDaily struct {
	Day               string `json:"day"`
	MessagesPublished int64  `json:"messages_published"`
	Deploys           int64  `json:"deploys"`
	StorageBytes      int64  `json:"storage_bytes"`
}

// UsageTotals aggregates a date range for one tenant. Messages and deploys are
// summed; StorageBytes is a point-in-time value (the most recent day in range),
// since summing a gauge-like snapshot is meaningless.
type UsageTotals struct {
	MessagesPublished int64 `json:"messages_published"`
	Deploys           int64 `json:"deploys"`
	StorageBytes      int64 `json:"storage_bytes"`
}

// UpsertUsageDaily writes (or refreshes) a tenant's usage row for one day. Safe
// to call repeatedly for the live day — ON CONFLICT replaces the counts.
func (r *PostgresRepository) UpsertUsageDaily(ctx context.Context, tenantID string, day time.Time, messages, deploys, storageBytes int64) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO usage_daily (tenant_id, day, messages_published, deploys, storage_bytes, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (tenant_id, day) DO UPDATE
		   SET messages_published = EXCLUDED.messages_published,
		       deploys            = EXCLUDED.deploys,
		       storage_bytes      = EXCLUDED.storage_bytes,
		       updated_at         = NOW()
	`, tenantID, day.UTC().Format("2006-01-02"), messages, deploys, storageBytes)
	return err
}

// ListUsageDaily returns a tenant's daily usage rows in [from, to] (inclusive),
// ordered by day ascending.
func (r *PostgresRepository) ListUsageDaily(ctx context.Context, tenantID string, from, to time.Time) ([]*UsageDaily, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT day, messages_published, deploys, storage_bytes
		FROM usage_daily
		WHERE tenant_id = $1 AND day >= $2 AND day <= $3
		ORDER BY day ASC
	`, tenantID, from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*UsageDaily
	for rows.Next() {
		var u UsageDaily
		var day time.Time
		if err := rows.Scan(&day, &u.MessagesPublished, &u.Deploys, &u.StorageBytes); err != nil {
			return nil, err
		}
		u.Day = day.UTC().Format("2006-01-02")
		out = append(out, &u)
	}
	return out, rows.Err()
}

// SumUsage aggregates a tenant's usage over [from, to]. Messages/deploys are
// summed; StorageBytes is the most recent day's snapshot in range.
func (r *PostgresRepository) SumUsage(ctx context.Context, tenantID string, from, to time.Time) (*UsageTotals, error) {
	t := &UsageTotals{}
	err := r.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(messages_published), 0),
		       COALESCE(SUM(deploys), 0),
		       COALESCE((SELECT storage_bytes FROM usage_daily
		                  WHERE tenant_id = $1 AND day >= $2 AND day <= $3
		                  ORDER BY day DESC LIMIT 1), 0)
		FROM usage_daily
		WHERE tenant_id = $1 AND day >= $2 AND day <= $3
	`, tenantID, from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02")).
		Scan(&t.MessagesPublished, &t.Deploys, &t.StorageBytes)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ListTenantStorage returns tenant_id → storage_bytes for every tenant with a
// quota row. The rollup uses it as both the tenant set and the storage source.
func (r *PostgresRepository) ListTenantStorage(ctx context.Context) (map[string]int64, error) {
	// lint:tenant-ok — the usage rollup is a global background job with no
	// request tenant; it intentionally reads every tenant's storage snapshot.
	rows, err := r.db.QueryContext(ctx, `SELECT tenant_id, storage_bytes FROM tenant_quotas`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var id string
		var bytes int64
		if err := rows.Scan(&id, &bytes); err != nil {
			return nil, err
		}
		out[id] = bytes
	}
	return out, rows.Err()
}
