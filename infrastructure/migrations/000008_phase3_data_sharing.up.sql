-- Phase 3: Tenant-to-Tenant Data Sharing
-- Connection requests, active data connections, and audit logging

-- Connection requests: tenant A asks to connect to tenant B
CREATE TABLE tenant_connection_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    requester_tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    target_tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    permission_type     VARCHAR(10) NOT NULL CHECK (permission_type IN ('send','receive','both')),
    status              VARCHAR(20) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','approved','denied','revoked')),
    message             TEXT,
    allowed_fields      JSONB,
    denied_fields       JSONB,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    responded_at        TIMESTAMPTZ
);
CREATE INDEX idx_tcr_target    ON tenant_connection_requests(target_tenant_id, status);
CREATE INDEX idx_tcr_requester ON tenant_connection_requests(requester_tenant_id, status);

-- Active data connections: created on approval
CREATE TABLE tenant_data_connections (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id          UUID NOT NULL REFERENCES tenant_connection_requests(id),
    requester_tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    target_tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    permission_type     VARCHAR(10) NOT NULL CHECK (permission_type IN ('send','receive','both')),
    allowed_fields      JSONB,
    denied_fields       JSONB,
    rate_limit_per_hour INT NOT NULL DEFAULT 1000,
    status              VARCHAR(20) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','paused','revoked')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at          TIMESTAMPTZ
);
CREATE INDEX idx_tdc_requester ON tenant_data_connections(requester_tenant_id, status);
CREATE INDEX idx_tdc_target    ON tenant_data_connections(target_tenant_id, status);

-- Audit log: every data POST is recorded
CREATE TABLE tenant_data_access_log (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id       UUID NOT NULL REFERENCES tenant_data_connections(id),
    requester_tenant_id UUID NOT NULL REFERENCES tenants(id),
    target_tenant_id    UUID NOT NULL REFERENCES tenants(id),
    request_time        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    fields_accessed     JSONB,
    bytes_received      INT,
    status_code         INT NOT NULL,
    ip_address          INET
);
CREATE INDEX idx_tdal_target     ON tenant_data_access_log(target_tenant_id, request_time DESC);
CREATE INDEX idx_tdal_connection ON tenant_data_access_log(connection_id, request_time DESC);
