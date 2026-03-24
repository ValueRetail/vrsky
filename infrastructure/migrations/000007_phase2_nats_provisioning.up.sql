-- Phase 2: Async Per-Tenant NATS Provisioning
-- Adds provisioning status, NATS instance reference, API keys, and job tracking

-- Add provisioning status and NATS instance reference to tenants
ALTER TABLE tenants
  ADD COLUMN status VARCHAR(50) NOT NULL DEFAULT 'active',
  ADD COLUMN nats_slug VARCHAR(120);

ALTER TABLE tenants
  ADD CONSTRAINT chk_tenants_status CHECK (status IN ('provisioning','active','failed','terminating'));

-- Tenant API keys (one per tenant, for Phase 3 tenant-to-tenant auth)
CREATE TABLE tenant_api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL UNIQUE REFERENCES tenants(id) ON DELETE CASCADE,
    api_key_hash VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    rotated_at TIMESTAMPTZ,
    is_active BOOLEAN NOT NULL DEFAULT true
);
CREATE INDEX idx_tenant_api_keys_tenant ON tenant_api_keys(tenant_id);

-- Provisioning job tracking
CREATE TABLE provisioning_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    status VARCHAR(50) NOT NULL DEFAULT 'queued',
    progress INT NOT NULL DEFAULT 0,
    current_step VARCHAR(255),
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT chk_provisioning_jobs_status CHECK (status IN ('queued','running','completed','failed'))
);
CREATE INDEX idx_provisioning_jobs_tenant ON provisioning_jobs(tenant_id, created_at DESC);
