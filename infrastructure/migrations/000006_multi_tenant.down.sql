-- Rollback: Drop multi-tenant tables
DROP TABLE IF EXISTS user_tenant_roles;
DROP TABLE IF EXISTS tenants;
