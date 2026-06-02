-- Phase 2A (#75): OAuth 2.0 framework.
--
-- Two tables:
--   oauth_providers — per-tenant OAuth 2.0 provider configuration
--                     (client_id + reference to encrypted client_secret).
--   oauth_grants    — issued grants per connection (access + refresh tokens,
--                     both referenced as encrypted secrets).
--
-- Tokens are NEVER stored in plaintext. The *_secret_id columns reference rows
-- in the secrets table (#66), which holds aes256:base64(...) ciphertext.

CREATE TABLE oauth_providers (
    id                  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name                TEXT         NOT NULL,                          -- human label, e.g. "Acme Salesforce Prod"
    provider_type       TEXT         NOT NULL,                          -- salesforce | microsoft365 | google | hubspot | shopify | custom
    client_id           TEXT         NOT NULL,
    client_secret_id    UUID         NOT NULL,                          -- -> secrets.id (not FK; secrets has many referrers)
    auth_url            TEXT         NOT NULL,                          -- populated from profile; overridable for custom
    token_url           TEXT         NOT NULL,
    revoke_url          TEXT         NULL,                              -- optional; used by Revoke when known
    scopes              TEXT[]       NOT NULL DEFAULT ARRAY[]::TEXT[],  -- admin-editable, profile-seeded
    redirect_url        TEXT         NOT NULL,                          -- our /oauth/callback URL
    extra_params        JSONB        NOT NULL DEFAULT '{}'::JSONB,      -- provider-specific extras (access_type, prompt, ...)
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX idx_oauth_providers_tenant ON oauth_providers(tenant_id);

CREATE TABLE oauth_grants (
    id                       UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,

    -- Nullable + ON DELETE SET NULL: once all of a provider's grants are
    -- revoked, an admin can delete the provider config. Revoked grant rows
    -- survive (their provider_id goes NULL) so the audit trail is preserved;
    -- the provider_type / provider_name snapshot below keeps them readable.
    provider_id              UUID         REFERENCES oauth_providers(id) ON DELETE SET NULL,

    -- Snapshot of provider identity so audit/list views survive the provider
    -- being deleted-after-revoke (provider_id goes NULL, these remain).
    provider_type            TEXT         NOT NULL,
    provider_name            TEXT         NOT NULL,

    -- Optional link to the connection that owns this grant. NULL during the
    -- brief window between callback and the user attaching it to a node, and
    -- for grants intentionally shared across connections.
    connection_id            UUID         NULL,

    user_identifier          TEXT         NULL,                              -- e.g. "alice@acme.com" or provider sub; for UI display
    scopes_granted           TEXT[]       NOT NULL DEFAULT ARRAY[]::TEXT[],  -- what the provider actually issued

    access_token_secret_id   UUID         NOT NULL,                          -- -> secrets.id (not FK; see oauth_providers)
    refresh_token_secret_id  UUID         NULL,                              -- NULL for providers that don't refresh (Shopify)

    expires_at               TIMESTAMPTZ  NULL,                              -- access-token expiry; NULL = unknown/never
    last_refreshed_at        TIMESTAMPTZ  NULL,
    refresh_failed_at        TIMESTAMPTZ  NULL,                              -- last failure; cleared on success
    refresh_failure_reason   TEXT         NULL,

    revoked_at               TIMESTAMPTZ  NULL,
    created_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_oauth_grants_tenant     ON oauth_grants(tenant_id);
CREATE INDEX idx_oauth_grants_provider   ON oauth_grants(provider_id);
CREATE INDEX idx_oauth_grants_connection ON oauth_grants(connection_id) WHERE connection_id IS NOT NULL;

-- Hot path: the refresher scans for grants expiring within its horizon
-- (default 5 minutes). The partial index keeps that scan an index-only
-- lookup over the small set of live, refreshable, near-expiry grants.
CREATE INDEX idx_oauth_grants_expiring
    ON oauth_grants(expires_at)
    WHERE revoked_at IS NULL
      AND expires_at IS NOT NULL
      AND refresh_token_secret_id IS NOT NULL;

COMMENT ON TABLE oauth_providers IS 'Per-tenant OAuth 2.0 provider configuration (#75). Client secret stored encrypted in secrets table.';
COMMENT ON TABLE oauth_grants    IS 'Issued OAuth 2.0 grants (#75). Access + refresh tokens stored encrypted in secrets table.';

COMMENT ON COLUMN oauth_grants.refresh_token_secret_id IS 'NULL when the provider does not support refresh (Shopify). Refresher loop skips these.';
COMMENT ON COLUMN oauth_grants.refresh_failed_at       IS 'Set when refresh fails. UI surfaces as "Reconnect required". Cleared on next successful refresh.';
