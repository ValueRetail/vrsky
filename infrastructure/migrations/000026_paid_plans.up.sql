-- Paid plans (plans/paid-plans.md): a workspace starts on a 14-day trial, is
-- suspended when the trial ends, and is activated by a platform operator once
-- the customer has been invoiced. Payments stay outside the product; these
-- columns are what a Stripe webhook would set later.

ALTER TABLE tenants
    ADD COLUMN billing_status VARCHAR(16) NOT NULL DEFAULT 'trial'
        CHECK (billing_status IN ('trial', 'paid', 'suspended')),
    ADD COLUMN trial_ends_at  TIMESTAMPTZ,
    ADD COLUMN billing_note   TEXT;

-- The tiers. 0 means unlimited, as it already does in tenant_quotas.
CREATE TABLE plan_limits (
    plan_name                   VARCHAR(32) PRIMARY KEY,
    max_msg_per_sec             INT         NOT NULL,
    max_integrations            INT         NOT NULL,
    max_storage_bytes           BIGINT      NOT NULL,
    included_messages_per_month BIGINT      NOT NULL,
    sort_order                  INT         NOT NULL
);
INSERT INTO plan_limits VALUES
    ('trial',      25,  2,  1073741824,    100000,   1),
    ('paid',       200, 20, 107374182400,  10000000, 2),
    ('enterprise', 0,   0,  0,             0,        3);

-- Every workspace that exists today is grandfathered as paid: nothing that
-- runs now may stop because billing arrived. Their quotas follow the plan.
UPDATE tenants SET billing_status = 'paid', subscription_plan = 'paid', trial_ends_at = NULL
WHERE deleted_at IS NULL;
UPDATE tenant_quotas q
   SET plan_name = p.plan_name, max_msg_per_sec = p.max_msg_per_sec,
       max_integrations = p.max_integrations, max_storage_bytes = p.max_storage_bytes,
       updated_at = NOW()
  FROM plan_limits p
 WHERE p.plan_name = 'paid';
ALTER TABLE tenants ALTER COLUMN subscription_plan SET DEFAULT 'trial';

-- The operator's inbox: one row per request, closed when the operator acts.
CREATE TABLE plan_requests (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    requested_plan VARCHAR(32) NOT NULL REFERENCES plan_limits(plan_name),
    message        TEXT        NOT NULL DEFAULT '',
    requested_by   UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    handled_at     TIMESTAMPTZ,
    handled_by     UUID        REFERENCES users(id) ON DELETE SET NULL,
    outcome        VARCHAR(32)
);
CREATE INDEX idx_plan_requests_tenant ON plan_requests(tenant_id);
CREATE UNIQUE INDEX idx_plan_requests_one_open ON plan_requests(tenant_id) WHERE handled_at IS NULL;

-- Which pipelines the suspension stopped, so activation can start them again.
ALTER TABLE connections ADD COLUMN stopped_by_billing BOOLEAN NOT NULL DEFAULT false;
