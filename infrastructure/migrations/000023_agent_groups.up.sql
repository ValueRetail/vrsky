-- Agent groups: one output node that sends to every till in a group.
--
-- A group is just a name an agent carries (all-tills, store-oslo); an agent
-- can be in several. No groups table: nothing to keep in sync, and "the
-- members of a group" is one indexed query on agents, which is already
-- tenant-scoped. A registration token can carry the groups a new agent joins,
-- so an installer one-liner minted for "all-tills" needs no later step.
ALTER TABLE agents ADD COLUMN groups TEXT[] NOT NULL DEFAULT '{}';
CREATE INDEX idx_agents_groups ON agents USING GIN (groups);

ALTER TABLE agent_registration_tokens ADD COLUMN suggested_groups TEXT[] NOT NULL DEFAULT '{}';
