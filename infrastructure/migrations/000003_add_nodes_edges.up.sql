-- Phase 1: Add graph-based pipeline model (Nodes/Edges) to connections table
-- This migration adds support for flexible DAG-based pipelines while maintaining
-- backward compatibility with the existing linear pipeline model.

-- Add nodes and edges columns to connections table
-- Using JSONB for efficient querying and indexing
ALTER TABLE connections
ADD COLUMN IF NOT EXISTS nodes JSONB DEFAULT '[]'::jsonb NOT NULL,
ADD COLUMN IF NOT EXISTS edges JSONB DEFAULT '[]'::jsonb NOT NULL;

-- Create GIN indexes for efficient JSONB querying
-- These indexes support containment queries (@>) and existence checks (?)
CREATE INDEX IF NOT EXISTS idx_connections_nodes ON connections USING GIN (nodes);
CREATE INDEX IF NOT EXISTS idx_connections_edges ON connections USING GIN (edges);

-- Create checkpoint table for tracking component state
-- This enables resumable processing and exactly-once semantics
CREATE TABLE IF NOT EXISTS connection_node_checkpoints (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id UUID NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    node_id VARCHAR(255) NOT NULL,
    last_processed_message_id VARCHAR(255),
    last_processed_at TIMESTAMP WITH TIME ZONE,
    message_count BIGINT DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(connection_id, node_id)
);

-- Create indexes for checkpoint queries
CREATE INDEX IF NOT EXISTS idx_node_checkpoints_connection ON connection_node_checkpoints(connection_id);
CREATE INDEX IF NOT EXISTS idx_node_checkpoints_node ON connection_node_checkpoints(connection_id, node_id);

-- Add comment documenting the migration
COMMENT ON COLUMN connections.nodes IS 'JSON array of pipeline nodes (consumers, filters, converters, producers)';
COMMENT ON COLUMN connections.edges IS 'JSON array of edges connecting nodes in the pipeline DAG';
COMMENT ON TABLE connection_node_checkpoints IS 'Tracks processing state per component for resumable pipelines';
