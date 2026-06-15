-- +goose Up
-- +goose StatementBegin
-- H3 resolution-8 cell (~0.74 km² hexagon) per report, computed in Go (uber/h3-go)
-- at insert time — deliberately NOT a generated column: the h3-pg Postgres extension
-- is not assumed present on the deployment target, and a single stored text id is
-- portable. Powers hexagonal hotspot aggregation (AreaGroupsH3) and the native h3id
-- export column (RAPIDA / GeoHub interoperability). NULL for location-unresolved
-- (landmark-only) reports, which have no point to index.
ALTER TABLE reports ADD COLUMN h3_r8 text;
-- Partial: the only reader (AreaGroupsH3) filters `h3_r8 IS NOT NULL`, so indexing the
-- location-unresolved (NULL) rows would be dead weight.
CREATE INDEX IF NOT EXISTS idx_reports_h3 ON reports (crisis_id, h3_r8) WHERE h3_r8 IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_reports_h3;
ALTER TABLE reports DROP COLUMN IF EXISTS h3_r8;
-- +goose StatementEnd
