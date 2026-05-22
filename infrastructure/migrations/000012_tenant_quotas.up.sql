-- Phase 1I (#74): per-tenant quotas.
--
-- Three knobs are stored. The hot ones (msg/sec, integration count) are
-- checked in the request path; the slow one (storage bytes) is updated by
-- an hourly background job that flips storage_exceeded so the upload
-- endpoints can short-circuit without doing a fresh count on every call.

CREATE TABLE tenant_quotas (
    tenant_id          UUID         PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    plan_name          VARCHAR(32)  NOT NULL DEFAULT 'free',
    max_msg_per_sec    INT          NOT NULL DEFAULT 50,
    max_integrations   INT          NOT NULL DEFAULT 10,
    max_storage_bytes  BIGINT       NOT NULL DEFAULT 1073741824,     -- 1 GiB
    storage_exceeded   BOOLEAN      NOT NULL DEFAULT FALSE,
    storage_bytes      BIGINT       NOT NULL DEFAULT 0,              -- last observed usage
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_tenant_quotas_plan ON tenant_quotas(plan_name);

COMMENT ON TABLE  tenant_quotas IS 'Per-tenant resource ceilings (#74). msg/sec and integrations are enforced in-line; storage via the hourly job.';
COMMENT ON COLUMN tenant_quotas.storage_exceeded IS 'Set true by the storage job once storage_bytes > max_storage_bytes. Cleared when usage drops back below the limit.';

-- Backfill: every existing tenant gets a free-plan quota row.
INSERT INTO tenant_quotas (tenant_id, plan_name)
SELECT id, 'free' FROM tenants
ON CONFLICT (tenant_id) DO NOTHING;
