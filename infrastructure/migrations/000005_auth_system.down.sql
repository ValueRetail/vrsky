-- Rollback: Drop all auth system tables
-- Run in reverse order to respect foreign key constraints

-- Drop auth audit log
DROP TABLE IF EXISTS auth_audit_log;

-- Drop sessions
DROP TABLE IF EXISTS sessions;

-- Drop password reset tokens
DROP TABLE IF EXISTS password_reset_tokens;

-- Drop email verification tokens
DROP TABLE IF EXISTS email_verification_tokens;

-- Drop trigger and function
DROP TRIGGER IF EXISTS trigger_users_updated_at ON users;
DROP FUNCTION IF EXISTS update_users_updated_at();

-- Drop users table
DROP TABLE IF EXISTS users;
