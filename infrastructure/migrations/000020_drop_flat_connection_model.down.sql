-- Restores the columns' existence, not their contents (see the up migration):
-- the data they held drove no runtime, so it is not preserved anywhere to
-- restore from.
--
-- NOTE these differ from the original definitions in 000001, which were
-- NOT NULL with no default. A default is required here: without one, adding a
-- NOT NULL column to a table that already has rows fails. '{}' is the value
-- every row would have carried anyway once the flat model fell out of use.
ALTER TABLE connections ADD COLUMN IF NOT EXISTS source_config      JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE connections ADD COLUMN IF NOT EXISTS converter_config   JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE connections ADD COLUMN IF NOT EXISTS filter_config      JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE connections ADD COLUMN IF NOT EXISTS destination_config JSONB NOT NULL DEFAULT '{}'::jsonb;
