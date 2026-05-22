-- Phase 1A (#66): per-tenant encrypted secrets store.
--
-- Each row holds a single piece of sensitive material (DB password, bearer
-- token, OAuth client secret, HMAC signing secret, ...). The plaintext never
-- enters the database; only AES-256-GCM ciphertext in the format
-- "aes256:<base64(nonce||ciphertext)>". Lookup and access are gated by
-- tenant_id at both the SQL and HTTP-handler layers.

CREATE TABLE secrets (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT         NOT NULL,
    ciphertext  TEXT         NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    rotated_at  TIMESTAMPTZ,

    -- Two secrets within a tenant cannot share a name. Names are for humans
    -- only — references in connector configs use the UUID.
    UNIQUE(tenant_id, name)
);

CREATE INDEX idx_secrets_tenant ON secrets(tenant_id);
