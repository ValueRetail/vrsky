-- Tenant NATS instances (#21) + connection→instance placement (#19).
--
-- nats_instances tracks every per-tenant NATS instance the control plane
-- provisions. It previously existed only in the legacy init-schema.sql, so a
-- migrate-only deploy (production) never had it — the discovery/autoscaler code
-- (#21/#19) and this migration's FK both assumed it. Create it here so
-- golang-migrate is the single source of truth.
--
-- The status CHECK covers every value the code writes: provisioning/active
-- (provisioner), unhealthy (health monitor #21), decommissioned (autoscaler
-- #19), plus the legacy scaling/terminating/terminated.
CREATE TABLE IF NOT EXISTS nats_instances (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    instance_number   INTEGER NOT NULL,
    dns_name          VARCHAR(255) NOT NULL,
    status            VARCHAR(50) NOT NULL DEFAULT 'provisioning',
    integration_count INTEGER DEFAULT 0,
    message_rate_avg  BIGINT  DEFAULT 0,
    connection_count  INTEGER DEFAULT 0,
    memory_usage_mb   INTEGER DEFAULT 0,
    created_at        TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at        TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    deleted_at        TIMESTAMP WITH TIME ZONE,
    UNIQUE (tenant_id, instance_number),
    CHECK (status IN ('provisioning', 'active', 'unhealthy', 'scaling',
                      'terminating', 'terminated', 'decommissioned'))
);

CREATE INDEX IF NOT EXISTS idx_nats_instances_tenant ON nats_instances(tenant_id);
CREATE INDEX IF NOT EXISTS idx_nats_instances_status ON nats_instances(status);
CREATE INDEX IF NOT EXISTS idx_nats_instances_dns    ON nats_instances(dns_name);

-- Connection placement: which instance a connection runs on (all its nodes
-- share one). NULL = legacy / single-instance.
ALTER TABLE connections
    ADD COLUMN IF NOT EXISTS nats_instance_id UUID
    REFERENCES nats_instances(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_connections_nats_instance
    ON connections(nats_instance_id);
