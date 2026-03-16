-- Migration: Add API Consumer State table
-- This table stores persistent state for API consumers to survive restarts
-- State includes: cursor positions, offsets, timestamps, and pagination info

CREATE TABLE IF NOT EXISTS api_consumer_state (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    
    -- Consumer identification
    consumer_id VARCHAR(255) NOT NULL UNIQUE,
    tenant_id UUID,  -- Optional: for multi-tenant isolation
    
    -- State data (JSONB for flexibility)
    -- Contains: endpoint cursors, offsets, last poll timestamps, pagination state
    state_data JSONB NOT NULL DEFAULT '{}'::jsonb,
    
    -- Metadata
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    
    -- Statistics (optional, for observability)
    total_polls BIGINT DEFAULT 0,
    total_records_fetched BIGINT DEFAULT 0,
    last_error TEXT,
    last_error_at TIMESTAMP WITH TIME ZONE
);

-- Create indexes for efficient querying
CREATE INDEX IF NOT EXISTS idx_api_consumer_state_consumer_id ON api_consumer_state(consumer_id);
CREATE INDEX IF NOT EXISTS idx_api_consumer_state_tenant_id ON api_consumer_state(tenant_id) WHERE tenant_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_api_consumer_state_updated_at ON api_consumer_state(updated_at);

-- Add comments for documentation
COMMENT ON TABLE api_consumer_state IS 'Persistent state storage for API consumers (polling external REST APIs)';
COMMENT ON COLUMN api_consumer_state.consumer_id IS 'Unique identifier for the API consumer instance';
COMMENT ON COLUMN api_consumer_state.tenant_id IS 'Optional tenant ID for multi-tenant deployments';
COMMENT ON COLUMN api_consumer_state.state_data IS 'JSONB containing cursor positions, offsets, timestamps, and pagination state per endpoint';
COMMENT ON COLUMN api_consumer_state.total_polls IS 'Running count of successful poll operations';
COMMENT ON COLUMN api_consumer_state.total_records_fetched IS 'Running count of total records ingested';
COMMENT ON COLUMN api_consumer_state.last_error IS 'Most recent error message (for debugging)';
COMMENT ON COLUMN api_consumer_state.last_error_at IS 'Timestamp of most recent error';

-- Create trigger function for auto-updating updated_at
CREATE OR REPLACE FUNCTION update_api_consumer_state_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Attach trigger to table
DROP TRIGGER IF EXISTS trigger_update_api_consumer_state_updated_at ON api_consumer_state;
CREATE TRIGGER trigger_update_api_consumer_state_updated_at
    BEFORE UPDATE ON api_consumer_state
    FOR EACH ROW
    EXECUTE FUNCTION update_api_consumer_state_updated_at();
