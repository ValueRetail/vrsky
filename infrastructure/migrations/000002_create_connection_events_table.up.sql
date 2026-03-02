-- Create connection_events table for audit trail and metrics storage
CREATE TABLE IF NOT EXISTS connection_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id UUID NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    tenant_id VARCHAR(255) NOT NULL,
    
    -- Event type: started, stopped, error, metrics_snapshot, config_changed
    event_type VARCHAR(50) NOT NULL,
    
    -- Event data stored as JSONB
    event_data JSONB,
    
    -- Timestamp
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Create indexes for common queries
CREATE INDEX idx_connection_events_connection_id ON connection_events(connection_id);
CREATE INDEX idx_connection_events_tenant_id ON connection_events(tenant_id);
CREATE INDEX idx_connection_events_event_type ON connection_events(event_type);
CREATE INDEX idx_connection_events_created_at ON connection_events(created_at);
CREATE INDEX idx_connection_events_tenant_type ON connection_events(tenant_id, event_type);
