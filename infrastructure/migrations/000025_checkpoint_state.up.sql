-- Free-form per-node state next to the watermark (plans/bc-picture-deletions.md).
-- First user: the Business Central consumer remembers which records had a
-- picture, so a removed picture can be reported to the till once and a
-- restart does not re-send every picture.
ALTER TABLE connection_node_checkpoints
    ADD COLUMN state JSONB NOT NULL DEFAULT '{}'::jsonb;
