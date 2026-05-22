-- Phase 1G (#72): general-purpose audit log.
--
-- Every state-changing API operation writes one row. Read-only operations
-- (GET, health checks, /metrics) are explicitly skipped.
--
-- The table is append-only: a trigger blocks UPDATE and DELETE so that even
-- a compromised application connection cannot tamper with the trail.
-- Retention is enforced out-of-band (cron job, documented separately).

CREATE TABLE audit_log (
    id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID         NOT NULL,
    -- actor: who triggered the action
    user_id       UUID         NULL,                                -- NULL for service-account / unauthenticated paths
    actor_kind    VARCHAR(32)  NOT NULL DEFAULT 'user',             -- user | api_key | service | system
    actor_label   TEXT         NULL,                                -- email | api key name | service name (denormalised for read perf)
    -- the operation
    action        VARCHAR(64)  NOT NULL,                            -- e.g. "connection.create", "connection.deploy", "secret.read"
    resource_type VARCHAR(64)  NOT NULL,                            -- e.g. "connection", "secret", "tenant"
    resource_id   TEXT         NULL,                                -- UUID or other identifier; nullable for create
    -- request context
    method        VARCHAR(8)   NOT NULL,
    path          TEXT         NOT NULL,
    status_code   INT          NOT NULL,
    request_id    TEXT         NULL,
    ip_address    INET         NULL,
    user_agent    TEXT         NULL,
    -- arbitrary structured extras populated by handlers (e.g. {name, before, after})
    details       JSONB        NOT NULL DEFAULT '{}'::jsonb,
    occurred_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- Read patterns:
--   1. UI: tenant timeline ordered by recency.
--   2. UI: history for a specific resource.
--   3. Filter by action.
CREATE INDEX idx_audit_log_tenant_time   ON audit_log (tenant_id, occurred_at DESC);
CREATE INDEX idx_audit_log_resource      ON audit_log (tenant_id, resource_type, resource_id);
CREATE INDEX idx_audit_log_action        ON audit_log (tenant_id, action);
CREATE INDEX idx_audit_log_user          ON audit_log (tenant_id, user_id) WHERE user_id IS NOT NULL;

-- Append-only: refuse any mutation on existing rows. INSERT is unrestricted.
CREATE OR REPLACE FUNCTION audit_log_block_mutations() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only (operation % rejected)', TG_OP USING ERRCODE = 'check_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_block_mutations();

CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_block_mutations();

COMMENT ON TABLE  audit_log IS 'Immutable per-tenant audit trail (#72). Retention enforced by external job.';
COMMENT ON COLUMN audit_log.action IS 'Dotted verb identifier, e.g. connection.create, secret.read, auth.login. See AUDIT_ACTIONS in src/pkg/managementapi/audit.go.';
