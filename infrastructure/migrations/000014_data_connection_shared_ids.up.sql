-- Add shared_connection_ids to tenant_data_connections.
--
-- The repository queries (ListDataConnections / GetDataConnectionByID /
-- GetActiveDataConnection) and the tenant-consumer bridge both SELECT
-- shared_connection_ids, but no migration ever created the column — so the
-- whole /data-connections API 500s. This adds it (JSONB array of connection
-- UUIDs the target tenant has shared with the requester).
ALTER TABLE tenant_data_connections
    ADD COLUMN IF NOT EXISTS shared_connection_ids JSONB NOT NULL DEFAULT '[]'::jsonb;
