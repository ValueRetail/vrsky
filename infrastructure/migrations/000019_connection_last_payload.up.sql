-- The sample-data / "show data structure" feature (filter + converter previews)
-- reads a connections.last_payload column, and every consumer writes it after
-- publishing — but the column was never defined in any migration, so the
-- feature silently failed on any DB built purely from migrations. Add it.
ALTER TABLE connections ADD COLUMN IF NOT EXISTS last_payload JSONB;
