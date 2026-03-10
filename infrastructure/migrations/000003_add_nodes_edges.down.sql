-- Rollback Phase 1: Remove graph-based pipeline model
-- This removes the nodes/edges columns and checkpoint table

-- Drop checkpoint table first (due to foreign key)
DROP TABLE IF EXISTS connection_node_checkpoints;

-- Drop indexes
DROP INDEX IF EXISTS idx_connections_edges;
DROP INDEX IF EXISTS idx_connections_nodes;

-- Remove columns from connections table
ALTER TABLE connections
DROP COLUMN IF EXISTS nodes,
DROP COLUMN IF EXISTS edges;
