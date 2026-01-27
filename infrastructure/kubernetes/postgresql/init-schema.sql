-- VRSky Platform Database Schema
-- PostgreSQL 18+

-- Enable UUID extension
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ============================================================================
-- TENANTS TABLE
-- ============================================================================
CREATE TABLE IF NOT EXISTS tenants (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR(255) NOT NULL,
    slug VARCHAR(100) NOT NULL UNIQUE,
    status VARCHAR(50) NOT NULL DEFAULT 'active',
    plan VARCHAR(50) NOT NULL DEFAULT 'free',
    
    -- Contact information
    contact_email VARCHAR(255) NOT NULL,
    contact_name VARCHAR(255),
    
    -- Limits and quotas
    max_integrations INTEGER DEFAULT 10,
    max_messages_per_month BIGINT DEFAULT 100000,
    
    -- Timestamps
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    deleted_at TIMESTAMP WITH TIME ZONE,
    
    -- Indexes
    CHECK (status IN ('active', 'suspended', 'deleted'))
);

CREATE INDEX idx_tenants_slug ON tenants(slug);
CREATE INDEX idx_tenants_status ON tenants(status);
CREATE INDEX idx_tenants_created_at ON tenants(created_at);

-- ============================================================================
-- NATS INSTANCES TABLE (Tenant NATS tracking)
-- ============================================================================
CREATE TABLE IF NOT EXISTS nats_instances (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    instance_number INTEGER NOT NULL,
    dns_name VARCHAR(255) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'provisioning',
    
    -- Capacity metrics
    integration_count INTEGER DEFAULT 0,
    message_rate_avg BIGINT DEFAULT 0,
    connection_count INTEGER DEFAULT 0,
    memory_usage_mb INTEGER DEFAULT 0,
    
    -- Timestamps
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    deleted_at TIMESTAMP WITH TIME ZONE,
    
    -- Constraints
    UNIQUE(tenant_id, instance_number),
    CHECK (status IN ('provisioning', 'active', 'scaling', 'terminating', 'terminated'))
);

CREATE INDEX idx_nats_instances_tenant ON nats_instances(tenant_id);
CREATE INDEX idx_nats_instances_status ON nats_instances(status);
CREATE INDEX idx_nats_instances_dns ON nats_instances(dns_name);

-- ============================================================================
-- CONNECTORS TABLE (Available connector types)
-- ============================================================================
CREATE TABLE IF NOT EXISTS connectors (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR(100) NOT NULL UNIQUE,
    type VARCHAR(50) NOT NULL,
    description TEXT,
    version VARCHAR(50) NOT NULL,
    
    -- Connector metadata
    category VARCHAR(50) NOT NULL,
    icon_url VARCHAR(500),
    documentation_url VARCHAR(500),
    
    -- Configuration schema (JSON)
    config_schema JSONB NOT NULL,
    
    -- Availability
    is_enabled BOOLEAN DEFAULT true,
    is_beta BOOLEAN DEFAULT false,
    
    -- Timestamps
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    
    CHECK (type IN ('consumer', 'producer', 'converter', 'filter'))
);

CREATE INDEX idx_connectors_type ON connectors(type);
CREATE INDEX idx_connectors_category ON connectors(category);
CREATE INDEX idx_connectors_enabled ON connectors(is_enabled);

-- ============================================================================
-- INTEGRATIONS TABLE
-- ============================================================================
CREATE TABLE IF NOT EXISTS integrations (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    description TEXT,
    status VARCHAR(50) NOT NULL DEFAULT 'draft',
    
    -- NATS assignment
    nats_instance_id UUID REFERENCES nats_instances(id),
    
    -- Integration flow components (JSON arrays of component IDs)
    consumer_id UUID REFERENCES connectors(id),
    consumer_config JSONB,
    
    converters JSONB,  -- Array of {connector_id, config}
    filters JSONB,     -- Array of {connector_id, config}
    
    producer_id UUID REFERENCES connectors(id),
    producer_config JSONB,
    
    -- Execution settings
    retry_max_attempts INTEGER DEFAULT 3,
    retry_backoff_seconds INTEGER DEFAULT 2,
    timeout_seconds INTEGER DEFAULT 300,
    
    -- Statistics
    message_count BIGINT DEFAULT 0,
    error_count BIGINT DEFAULT 0,
    last_run_at TIMESTAMP WITH TIME ZONE,
    
    -- Timestamps
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    deleted_at TIMESTAMP WITH TIME ZONE,
    
    CHECK (status IN ('draft', 'active', 'paused', 'error', 'deleted'))
);

CREATE INDEX idx_integrations_tenant ON integrations(tenant_id);
CREATE INDEX idx_integrations_status ON integrations(status);
CREATE INDEX idx_integrations_nats_instance ON integrations(nats_instance_id);
CREATE INDEX idx_integrations_created_at ON integrations(created_at);

-- ============================================================================
-- MESSAGE LOG TABLE (Minimal tracking, TTL-based)
-- ============================================================================
CREATE TABLE IF NOT EXISTS message_log (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    integration_id UUID NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    
    status VARCHAR(50) NOT NULL,
    retry_count INTEGER DEFAULT 0,
    
    -- Payload reference (if stored in MinIO)
    payload_ref VARCHAR(500),
    payload_size BIGINT,
    
    -- Error details (if failed)
    error_message TEXT,
    error_stack TEXT,
    
    -- Timestamps
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    completed_at TIMESTAMP WITH TIME ZONE,
    
    CHECK (status IN ('received', 'processing', 'retry', 'completed', 'dead_letter'))
);

CREATE INDEX idx_message_log_tenant ON message_log(tenant_id);
CREATE INDEX idx_message_log_integration ON message_log(integration_id);
CREATE INDEX idx_message_log_status ON message_log(status);
CREATE INDEX idx_message_log_created_at ON message_log(created_at);

-- TTL cleanup: Delete completed messages older than 24 hours
-- (Run via cron job or pg_cron extension)
-- DELETE FROM message_log WHERE status = 'completed' AND completed_at < NOW() - INTERVAL '24 hours';

-- ============================================================================
-- API KEYS TABLE (Tenant authentication)
-- ============================================================================
CREATE TABLE IF NOT EXISTS api_keys (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    key_hash VARCHAR(255) NOT NULL UNIQUE,
    
    -- Permissions
    scopes JSONB NOT NULL DEFAULT '["read", "write"]',
    
    -- Rate limiting
    rate_limit_per_minute INTEGER DEFAULT 1000,
    
    -- Status
    is_active BOOLEAN DEFAULT true,
    expires_at TIMESTAMP WITH TIME ZONE,
    last_used_at TIMESTAMP WITH TIME ZONE,
    
    -- Timestamps
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_api_keys_tenant ON api_keys(tenant_id);
CREATE INDEX idx_api_keys_hash ON api_keys(key_hash);
CREATE INDEX idx_api_keys_active ON api_keys(is_active);

-- ============================================================================
-- AUDIT LOG TABLE (For POC: keep minimal)
-- ============================================================================
CREATE TABLE IF NOT EXISTS audit_log (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
    
    action VARCHAR(100) NOT NULL,
    resource_type VARCHAR(100) NOT NULL,
    resource_id UUID,
    
    user_id UUID,
    ip_address INET,
    
    metadata JSONB,
    
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_audit_log_tenant ON audit_log(tenant_id);
CREATE INDEX idx_audit_log_created_at ON audit_log(created_at);
CREATE INDEX idx_audit_log_action ON audit_log(action);

-- ============================================================================
-- FUNCTIONS AND TRIGGERS
-- ============================================================================

-- Auto-update updated_at timestamp
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER update_tenants_updated_at BEFORE UPDATE ON tenants
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_integrations_updated_at BEFORE UPDATE ON integrations
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_api_keys_updated_at BEFORE UPDATE ON api_keys
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_nats_instances_updated_at BEFORE UPDATE ON nats_instances
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ============================================================================
-- SEED DATA (For POC)
-- ============================================================================

-- Insert default connectors
INSERT INTO connectors (name, type, description, version, category, config_schema) VALUES
('HTTP Webhook Consumer', 'consumer', 'Receives HTTP POST webhooks', '1.0.0', 'http', 
 '{"type": "object", "properties": {"endpoint": {"type": "string"}, "auth": {"type": "string"}}}'),
 
('PostgreSQL Consumer', 'consumer', 'Reads from PostgreSQL table via CDC', '1.0.0', 'database', 
 '{"type": "object", "properties": {"connection_string": {"type": "string"}, "table": {"type": "string"}}}'),
 
('File Consumer', 'consumer', 'Watches directory for new files', '1.0.0', 'file', 
 '{"type": "object", "properties": {"path": {"type": "string"}, "pattern": {"type": "string"}}}'),

('HTTP REST Producer', 'producer', 'Sends HTTP POST/PUT requests', '1.0.0', 'http', 
 '{"type": "object", "properties": {"url": {"type": "string"}, "method": {"type": "string"}}}'),
 
('PostgreSQL Producer', 'producer', 'Inserts into PostgreSQL table', '1.0.0', 'database', 
 '{"type": "object", "properties": {"connection_string": {"type": "string"}, "table": {"type": "string"}}}'),
 
('JSON Converter', 'converter', 'Converts between JSON formats', '1.0.0', 'transformation', 
 '{"type": "object", "properties": {"mapping": {"type": "object"}}}'),
 
('Field Filter', 'filter', 'Filters messages by field value', '1.0.0', 'filter', 
 '{"type": "object", "properties": {"field": {"type": "string"}, "condition": {"type": "string"}}}')
ON CONFLICT (name) DO NOTHING;

-- Create demo tenant (for POC testing)
INSERT INTO tenants (name, slug, contact_email, contact_name, plan, max_integrations, max_messages_per_month)
VALUES ('Demo Tenant', 'demo', 'demo@vrsky.example.com', 'Demo User', 'free', 10, 100000)
ON CONFLICT (slug) DO NOTHING;
