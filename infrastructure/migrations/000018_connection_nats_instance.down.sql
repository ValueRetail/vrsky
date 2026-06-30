DROP INDEX IF EXISTS idx_connections_nats_instance;
ALTER TABLE connections DROP COLUMN IF EXISTS nats_instance_id;
DROP TABLE IF EXISTS nats_instances;
