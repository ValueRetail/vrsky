-- Phase 1 Refactor: Multi-Tenant Foundation
-- Creates tenants and user_tenant_roles tables

CREATE TABLE IF NOT EXISTS tenants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    slug VARCHAR(100) NOT NULL UNIQUE,
    owner_id UUID NOT NULL REFERENCES users(id),
    subscription_plan VARCHAR(50) NOT NULL DEFAULT 'free',
    is_verified BOOLEAN NOT NULL DEFAULT false,
    max_integrations INTEGER NOT NULL DEFAULT 10,
    max_messages_per_month BIGINT NOT NULL DEFAULT 100000,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_tenants_owner ON tenants(owner_id);
CREATE INDEX IF NOT EXISTS idx_tenants_slug ON tenants(slug);

CREATE TABLE IF NOT EXISTS user_tenant_roles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    role VARCHAR(50) NOT NULL CHECK (role IN ('owner', 'admin', 'editor', 'viewer')),
    invited_by UUID REFERENCES users(id),
    invited_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    joined_at TIMESTAMPTZ,
    UNIQUE(user_id, tenant_id)
);

CREATE INDEX IF NOT EXISTS idx_utr_user ON user_tenant_roles(user_id);
CREATE INDEX IF NOT EXISTS idx_utr_tenant ON user_tenant_roles(tenant_id);

-- Migrate existing users: create a default tenant for each user without one
INSERT INTO tenants (name, slug, owner_id)
SELECT
    COALESCE(u.full_name, split_part(u.email, '@', 1)) || '''s Workspace',
    'workspace-' || LEFT(u.id::text, 8),
    u.id
FROM users u
WHERE u.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM tenants t WHERE t.owner_id = u.id);

INSERT INTO user_tenant_roles (user_id, tenant_id, role, joined_at)
SELECT u.id, t.id, 'owner', NOW()
FROM users u
JOIN tenants t ON t.owner_id = u.id
WHERE u.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM user_tenant_roles utr
    WHERE utr.user_id = u.id AND utr.tenant_id = t.id
  );
