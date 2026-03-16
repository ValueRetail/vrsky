-- Rollback: Remove API Consumer State table and related objects

-- Drop trigger first
DROP TRIGGER IF EXISTS trigger_update_api_consumer_state_updated_at ON api_consumer_state;

-- Drop trigger function
DROP FUNCTION IF EXISTS update_api_consumer_state_updated_at();

-- Drop indexes (automatically dropped with table, but explicit for clarity)
DROP INDEX IF EXISTS idx_api_consumer_state_consumer_id;
DROP INDEX IF EXISTS idx_api_consumer_state_tenant_id;
DROP INDEX IF EXISTS idx_api_consumer_state_updated_at;

-- Drop table
DROP TABLE IF EXISTS api_consumer_state;
