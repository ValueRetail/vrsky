-- pkg/checkpoint has queried connection_node_checkpoints by tenant_id since it
-- was written, but 000003 never created the column and the unique constraint
-- it upserts against — (tenant_id, connection_id, node_id) — does not exist
-- either. Every PostgresStore call therefore fails at runtime. Nothing wrote
-- to the table, so nothing noticed until the Business Central consumer became
-- its first user.

ALTER TABLE connection_node_checkpoints
    ADD COLUMN IF NOT EXISTS tenant_id VARCHAR(255);

-- Backfill from the owning connection. The table is empty in every known
-- deployment; this is here so the migration is still correct where it is not.
UPDATE connection_node_checkpoints cp
SET tenant_id = c.tenant_id
FROM connections c
WHERE cp.connection_id = c.id
  AND cp.tenant_id IS NULL;

-- A row whose connection has already been deleted has no tenant to attribute
-- it to, and a checkpoint without a pipeline is not worth keeping.
DELETE FROM connection_node_checkpoints WHERE tenant_id IS NULL;

ALTER TABLE connection_node_checkpoints
    ALTER COLUMN tenant_id SET NOT NULL;

-- ON CONFLICT binds to a constraint, so the upsert's target columns must have
-- one. The old (connection_id, node_id) key is subsumed by the new one.
ALTER TABLE connection_node_checkpoints
    DROP CONSTRAINT IF EXISTS connection_node_checkpoints_connection_id_node_id_key;

ALTER TABLE connection_node_checkpoints
    ADD CONSTRAINT connection_node_checkpoints_tenant_connection_node_key
    UNIQUE (tenant_id, connection_id, node_id);

CREATE INDEX IF NOT EXISTS idx_node_checkpoints_tenant
    ON connection_node_checkpoints(tenant_id, connection_id, node_id);

COMMENT ON COLUMN connection_node_checkpoints.last_processed_message_id IS 'Where this node got to: a message id for pipeline components, or for a polling connector the watermark it resumes from (the Business Central consumer stores the newest lastModifiedDateTime it has published).';
