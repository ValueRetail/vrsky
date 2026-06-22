-- Per-tenant usage metering (Phase 4A / #92).
--
-- A daily rollup job (management-api) snapshots per-tenant usage here once an
-- hour for the current UTC day: message + deploy counts come from Prometheus
-- (increase() over the day), storage from tenant_quotas.storage_bytes. The table
-- is the durable record — Prometheus counters reset on worker restart and have
-- limited retention, but these rows persist, satisfying the "counters survive
-- restart (Prometheus + Postgres snapshot)" acceptance criterion. The UI reads
-- current-month totals + daily rows from here; CSV export streams the same.

CREATE TABLE IF NOT EXISTS usage_daily (
    tenant_id          UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    day                DATE        NOT NULL,
    messages_published BIGINT      NOT NULL DEFAULT 0,
    deploys            BIGINT      NOT NULL DEFAULT 0,
    storage_bytes      BIGINT      NOT NULL DEFAULT 0,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, day)
);

-- Range scans over a billing period span all tenants for a day range.
CREATE INDEX IF NOT EXISTS idx_usage_daily_day ON usage_daily (day);
