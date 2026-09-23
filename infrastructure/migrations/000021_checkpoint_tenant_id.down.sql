DROP INDEX IF EXISTS idx_node_checkpoints_tenant;

ALTER TABLE connection_node_checkpoints
    DROP CONSTRAINT IF EXISTS connection_node_checkpoints_tenant_connection_node_key;

ALTER TABLE connection_node_checkpoints
    DROP COLUMN IF EXISTS tenant_id;

-- Restored last: dropping tenant_id first can leave rows that differ only by
-- tenant, and the old key would then refuse to be created.
ALTER TABLE connection_node_checkpoints
    ADD CONSTRAINT connection_node_checkpoints_connection_id_node_id_key
    UNIQUE (connection_id, node_id);
