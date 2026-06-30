-- Pending member invitations (#130). The "add member by email" path only works
-- for users who already have an account; this table backs inviting an email
-- that has not registered yet — a pending invite that can be listed, resent,
-- and revoked, and accepted once the invitee signs up.

CREATE TABLE IF NOT EXISTS tenant_invites (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email       VARCHAR(255) NOT NULL,
    role        VARCHAR(50)  NOT NULL,
    token       VARCHAR(128) NOT NULL UNIQUE,
    status      VARCHAR(20)  NOT NULL DEFAULT 'pending', -- pending | accepted | revoked
    invited_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMP WITH TIME ZONE NOT NULL,
    accepted_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX IF NOT EXISTS idx_tenant_invites_tenant ON tenant_invites(tenant_id);
CREATE INDEX IF NOT EXISTS idx_tenant_invites_token ON tenant_invites(token);

-- At most one OUTSTANDING (pending) invite per (tenant, email). Accepted/revoked
-- rows are kept for history and don't block a fresh invite.
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_invites_pending_email
    ON tenant_invites(tenant_id, lower(email))
    WHERE status = 'pending';
