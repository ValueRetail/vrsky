DROP INDEX IF EXISTS idx_users_oidc_identity;
ALTER TABLE users DROP COLUMN IF EXISTS oidc_subject;
ALTER TABLE users DROP COLUMN IF EXISTS oidc_provider;
DROP TABLE IF EXISTS oidc_config;
