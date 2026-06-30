-- Connection→NATS-instance placement (#19). With per-tenant NATS autoscaling a
-- tenant can have multiple instances; each connection runs entirely on one of
-- them. This column records the placement so workers dial the right instance
-- and the autoscaler can count per-instance load and rebalance. NULL = legacy /
-- single-instance (worker falls back to the tenant's only instance or NATS_URL).
ALTER TABLE connections
    ADD COLUMN IF NOT EXISTS nats_instance_id UUID
    REFERENCES nats_instances(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_connections_nats_instance
    ON connections(nats_instance_id);
