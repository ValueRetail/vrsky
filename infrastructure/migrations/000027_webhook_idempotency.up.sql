-- Idempotency keys for inbound webhooks (plans/webhook-idempotency.md).
-- A sender's outbox delivers at least once; the key it repeats on a retry is
-- remembered per connection so the retry is acknowledged without being
-- published again. Rows are expired by the webhook consumer after 30 days.
CREATE TABLE webhook_idempotency_keys (
    tenant_id       VARCHAR(255) NOT NULL,                 -- connections.tenant_id is varchar
    connection_id   UUID         NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    idempotency_key VARCHAR(64)  NOT NULL,
    envelope_id     UUID         NOT NULL,                 -- what the first delivery became
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (connection_id, idempotency_key)
);
CREATE INDEX idx_webhook_idempotency_created ON webhook_idempotency_keys(created_at);
