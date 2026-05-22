-- Phase 1C (#68): OIDC / SSO support.
--
-- Per-tenant OIDC provider configuration plus user → OIDC subject linkage.
-- The client secret never lives in this table; it is stored encrypted via
-- the secrets table (#66) and only referenced by UUID.

CREATE TABLE oidc_config (
    tenant_id           UUID         PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    issuer_url          TEXT         NOT NULL,        -- e.g. https://accounts.google.com
    client_id           TEXT         NOT NULL,
    client_secret_id    UUID         NOT NULL,        -- references secrets.id
    redirect_url        TEXT         NOT NULL,        -- our callback URL exposed to the IdP
    scopes              TEXT[]       NOT NULL DEFAULT ARRAY['openid', 'email', 'profile'],
    allowed_domains     TEXT[]       NULL,            -- NULL = any domain; otherwise whitelist
    default_role        VARCHAR(32)  NOT NULL DEFAULT 'viewer',
    provider_label      TEXT         NULL,            -- shown on the sign-in button ("Sign in with Acme SSO")
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE oidc_config IS 'Per-tenant OIDC provider config. Client secret stored encrypted in secrets table.';

-- Link OIDC identities to existing users. (oidc_provider, oidc_subject) is
-- globally unique — the same person could sign into multiple tenants but
-- through different IdPs each with its own subject namespace.
ALTER TABLE users
    ADD COLUMN oidc_provider VARCHAR(255) NULL,
    ADD COLUMN oidc_subject  TEXT         NULL;

CREATE UNIQUE INDEX idx_users_oidc_identity
    ON users (oidc_provider, oidc_subject)
    WHERE oidc_provider IS NOT NULL AND oidc_subject IS NOT NULL;
