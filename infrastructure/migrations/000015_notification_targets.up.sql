-- Phase 3A (#84): per-tenant alert notification targets.
--
-- One table:
--   notification_targets — where a tenant's alerts get delivered (Slack
--                          incoming webhook, email recipient, PagerDuty
--                          routing key, or a generic webhook).
--
-- Sensitive values (Slack webhook URL, PagerDuty routing key, webhook HMAC
-- secret) are NEVER stored in plaintext: secret_id references a row in the
-- secrets table (#66) holding aes256:base64(...) ciphertext. Non-secret
-- settings (email address, minimum severity, platform flag) live in config.
--
-- Routing model: alerts carrying a tenant_id label are dispatched to that
-- tenant's enabled targets; platform-level alerts (disk, NATS, mgmt-api,
-- certs) go to targets with config->>'platform' = 'true'.

CREATE TABLE notification_targets (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT         NOT NULL,                          -- human label, e.g. "Ops Slack #alerts"
    type        TEXT         NOT NULL,                          -- slack | email | pagerduty | webhook
    config      JSONB        NOT NULL DEFAULT '{}'::JSONB,      -- non-secret: email/url/min_severity/platform
    secret_id   UUID         NULL,                              -- -> secrets.id (webhook URL / routing key / HMAC secret)
    enabled     BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, name),
    CONSTRAINT notification_targets_type_check
        CHECK (type IN ('slack', 'email', 'pagerduty', 'webhook'))
);

CREATE INDEX idx_notification_targets_tenant ON notification_targets(tenant_id);
