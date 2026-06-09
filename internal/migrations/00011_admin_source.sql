-- +goose Up
-- +goose StatementBegin
-- Provenance + precedence for admin_areas now that boundaries come from MULTIPLE
-- sources (the hand-seeded Antakya grid, a global Natural Earth ADM0 baseline, and
-- lazily-fetched geoBoundaries ADM1 per crisis country). `source` drives the
-- reverse-geocoder's tie-break (authoritative COD/seed wins over geoBoundaries at
-- equal depth); `iso3` gates per-country lazy loading; `source_version` records the
-- boundary vintage so re-tagging is auditable.
ALTER TABLE admin_areas ADD COLUMN IF NOT EXISTS source         text;
ALTER TABLE admin_areas ADD COLUMN IF NOT EXISTS iso3           text;
ALTER TABLE admin_areas ADD COLUMN IF NOT EXISTS source_version text;

-- The existing hand-seeded Antakya rows are the most authoritative demo data.
UPDATE admin_areas SET source = 'seed', iso3 = 'TUR' WHERE source IS NULL;

-- Fast "do we already have ADM1 for this country?" lookups for the lazy loader.
CREATE INDEX IF NOT EXISTS idx_admin_areas_iso3_level ON admin_areas (iso3, level, source);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_admin_areas_iso3_level;
ALTER TABLE admin_areas DROP COLUMN IF EXISTS source_version;
ALTER TABLE admin_areas DROP COLUMN IF EXISTS iso3;
ALTER TABLE admin_areas DROP COLUMN IF EXISTS source;
-- +goose StatementEnd
