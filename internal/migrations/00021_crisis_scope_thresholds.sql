-- +goose Up
-- +goose StatementBegin
-- Admin-area scope for a crisis: the COD-AB / geoBoundaries area a crisis is bounded
-- to (joins to admin_areas, the same table ResolveAdmin reverse-geocodes against).
-- NULL = legacy center+radius circle scope (the fallback when a point falls outside
-- all known boundaries). This lets an EMERGENT cluster be constrained to ONE admin
-- area instead of a blind 2 km circle that can straddle two districts, and lets the
-- dashboard/exports label a crisis by its official P-code.
ALTER TABLE crises ADD COLUMN admin_pcode text REFERENCES admin_areas(pcode);

-- Per-crisis emergent-clustering thresholds. NULL = use the deployment-global
-- defaults (config.Config / env: BEACON_EMERGENT_*). On an EMERGENT crisis these are
-- stamped with the EFFECTIVE values that formed it (provenance an analyst can read on
-- the review card, and a hook for future per-crisis tuning); on a declared
-- (feed/analyst) crisis they stay NULL unless explicitly configured.
ALTER TABLE crises ADD COLUMN emergent_radius_km   double precision;
ALTER TABLE crises ADD COLUMN emergent_window_hrs  integer;
ALTER TABLE crises ADD COLUMN emergent_min_reports integer;

CREATE INDEX IF NOT EXISTS idx_crises_admin_pcode ON crises (admin_pcode);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_crises_admin_pcode;
ALTER TABLE crises DROP COLUMN IF EXISTS emergent_min_reports;
ALTER TABLE crises DROP COLUMN IF EXISTS emergent_window_hrs;
ALTER TABLE crises DROP COLUMN IF EXISTS emergent_radius_km;
ALTER TABLE crises DROP COLUMN IF EXISTS admin_pcode;
-- +goose StatementEnd
