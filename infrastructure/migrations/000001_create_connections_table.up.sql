-- Create connections table for Management API
CREATE TABLE IF NOT EXISTS connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id VARCHAR(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    description TEXT,
    
    -- Configuration stored as JSONB
    source_config JSONB NOT NULL,
    converter_config JSONB NOT NULL,
    filter_config JSONB NOT NULL,
    destination_config JSONB NOT NULL,
    
    -- Status: stopped, running, error
    status VARCHAR(50) NOT NULL DEFAULT 'stopped',
    
    -- Timestamps
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP WITH TIME ZONE,
    stopped_at TIMESTAMP WITH TIME ZONE,
    
    -- Last error message
    last_error TEXT,
    
    -- Unique constraint: tenant + name
    CONSTRAINT unique_tenant_connection_name UNIQUE(tenant_id, name)
);

-- Create indexes for common queries
CREATE INDEX idx_connections_tenant_id ON connections(tenant_id);
CREATE INDEX idx_connections_status ON connections(status);
CREATE INDEX idx_connections_created_at ON connections(created_at);
CREATE INDEX idx_connections_tenant_status ON connections(tenant_id, status);
