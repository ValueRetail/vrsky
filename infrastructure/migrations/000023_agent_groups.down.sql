ALTER TABLE agent_registration_tokens DROP COLUMN IF EXISTS suggested_groups;
DROP INDEX IF EXISTS idx_agents_groups;
ALTER TABLE agents DROP COLUMN IF EXISTS groups;
