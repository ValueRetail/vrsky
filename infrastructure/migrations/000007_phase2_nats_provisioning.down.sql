-- Rollback Phase 2: Async Per-Tenant NATS Provisioning
DROP TABLE IF EXISTS provisioning_jobs;
DROP TABLE IF EXISTS tenant_api_keys;
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS chk_tenants_status;
ALTER TABLE tenants DROP COLUMN IF EXISTS nats_slug;
ALTER TABLE tenants DROP COLUMN IF EXISTS status;
