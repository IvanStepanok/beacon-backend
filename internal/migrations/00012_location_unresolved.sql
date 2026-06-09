-- +goose Up
-- +goose StatementBegin
-- Location-unresolved reports (C4): a landmark-only report (no GPS fix, no tapped
-- footprint) must NOT be shipped as 0,0 (Null Island). It is stored with NULL
-- geom/lat/lng + location_resolved=false and a non-empty landmark, so the map can
-- skip it and the export emits geometry:null / blank coords instead of [0,0].
--
-- GPS accuracy reuses the EXISTING gps_accuracy_m column (the C4 wire field
-- `accuracyMeters` is an alias coalesced into gps_accuracy_m in the service) — no
-- second accuracy column is added.

ALTER TABLE reports ADD COLUMN IF NOT EXISTS location_resolved boolean NOT NULL DEFAULT true;

-- geom/lat/lng become nullable so an unresolved report can store NULL coordinates.
ALTER TABLE reports ALTER COLUMN geom DROP NOT NULL;
ALTER TABLE reports ALTER COLUMN lat  DROP NOT NULL;
ALTER TABLE reports ALTER COLUMN lng  DROP NOT NULL;

-- Data-integrity guard: a row is valid iff it has a RESOLVED point OR it is
-- explicitly unresolved WITH a non-empty landmark.
ALTER TABLE reports ADD CONSTRAINT chk_location_resolved CHECK (
       (location_resolved = true  AND lat IS NOT NULL AND lng IS NOT NULL)
    OR (location_resolved = false AND landmark IS NOT NULL AND landmark <> ''));

CREATE INDEX IF NOT EXISTS idx_reports_unresolved ON reports (crisis_id, location_resolved);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_reports_unresolved;
ALTER TABLE reports DROP CONSTRAINT IF EXISTS chk_location_resolved;
-- Restore NOT NULL only after nulling any unresolved rows out of existence is the
-- caller's responsibility; we conservatively leave geom/lat/lng nullable on down to
-- avoid failing the migration on existing unresolved rows, and drop the new column.
ALTER TABLE reports DROP COLUMN IF EXISTS location_resolved;
-- +goose StatementEnd
