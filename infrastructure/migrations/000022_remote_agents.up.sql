-- Remote agents (#266): a small binary on a customer machine that dials out to
-- VRSky over HTTPS and makes that machine usable as a pipeline input or output.
--
-- tenant_id is UUID with a foreign key, the shape every settings table since
-- multi-tenancy uses (secrets, tenant_invites, oauth_providers). The one place
-- agents are joined to connections — whose tenant_id is the legacy VARCHAR —
-- casts a.tenant_id::text.

CREATE TABLE agents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name            VARCHAR(255) NOT NULL,
    hostname        VARCHAR(255) NOT NULL DEFAULT '',
    os              VARCHAR(32)  NOT NULL DEFAULT '',
    arch            VARCHAR(32)  NOT NULL DEFAULT '',
    agent_version   VARCHAR(64)  NOT NULL DEFAULT '',
    -- hex(sha256(credential)) via pkg/auth.HashToken. The credential is returned
    -- to the agent exactly once, at registration, and never stored.
    credential_hash CHAR(64) NOT NULL UNIQUE,
    -- Reported by the agent: [{"name":"inbox","mode":"read"}, ...]. Names and
    -- modes only. The agent's own config owns the paths, and they never leave
    -- the machine.
    directories     JSONB NOT NULL DEFAULT '[]'::jsonb,
    last_seen_at    TIMESTAMPTZ,
    registered_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Revoke keeps the row: a pipeline that still names the agent can then say
    -- why it is broken, rather than pointing at nothing.
    revoked_at      TIMESTAMPTZ,
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX idx_agents_tenant ON agents(tenant_id);

-- A revoked agent releases its name, so the machine can be re-registered under
-- the same one.
CREATE UNIQUE INDEX idx_agents_tenant_name_live
    ON agents(tenant_id, lower(name)) WHERE revoked_at IS NULL;

CREATE TABLE agent_registration_tokens (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- hex(sha256(token)). UNIQUE is also the lookup index — tenant_api_keys has
    -- no index on its hash column and scans on every authentication.
    token_hash     CHAR(64) NOT NULL UNIQUE,
    suggested_name VARCHAR(255),
    created_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at     TIMESTAMPTZ NOT NULL,
    -- Consumed by a single UPDATE ... WHERE used_at IS NULL AND expires_at > NOW(),
    -- so two registrations racing on one token cannot both succeed.
    used_at        TIMESTAMPTZ,
    used_by_agent  UUID REFERENCES agents(id) ON DELETE SET NULL
);

CREATE INDEX idx_agent_reg_tokens_tenant ON agent_registration_tokens(tenant_id);
