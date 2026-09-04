-- Drop the four columns of the retired flat ("linear") connection model.
--
-- These held source_config / converter_config / filter_config /
-- destination_config for the pre-graph pipeline shape. Nothing reads them:
-- every standing connector service loads its per-connection settings from the
-- `nodes` array, the UI has only ever written nodes/edges, and the validators
-- that guarded these columns validated a shape no runtime consumed (#212).
-- The one writer left was POST /api/v1/api-consumers, which produced
-- connections with no nodes at all — unrunnable by construction; it is removed
-- in the same change.
--
-- SAFETY: any row still carrying data here describes a pipeline that cannot
-- run, so nothing operable is lost. To see whether any such rows exist before
-- applying (expected: 0):
--
--   SELECT count(*) FROM connections
--    WHERE jsonb_typeof(source_config) = 'object'      AND source_config      <> '{}'::jsonb
--       OR jsonb_typeof(converter_config) = 'object'   AND converter_config   <> '{}'::jsonb
--       OR jsonb_typeof(filter_config) = 'object'      AND filter_config      <> '{}'::jsonb
--       OR jsonb_typeof(destination_config) = 'object' AND destination_config <> '{}'::jsonb;
--
-- The down migration restores the columns but NOT their contents: this is a
-- one-way data drop, and that is deliberate — the data drives nothing.
ALTER TABLE connections DROP COLUMN IF EXISTS source_config;
ALTER TABLE connections DROP COLUMN IF EXISTS converter_config;
ALTER TABLE connections DROP COLUMN IF EXISTS filter_config;
ALTER TABLE connections DROP COLUMN IF EXISTS destination_config;
