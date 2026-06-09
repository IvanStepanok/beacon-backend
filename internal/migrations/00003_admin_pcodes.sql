-- +goose Up
-- +goose StatementBegin
-- Admin-boundary reference (OCHA COD-AB style): nested administrative areas keyed
-- by P-code. Reports are reverse-geocoded (GPS → ST_Contains) and stamped with the
-- P-code chain — the join/routing key for the whole humanitarian data system
-- (population, 3W/4W, HNO, PDNA all key on P-codes; names are ambiguous).
CREATE TABLE admin_areas (
    pcode        text PRIMARY KEY,
    level        smallint NOT NULL CHECK (level BETWEEN 0 AND 3),
    name         text NOT NULL,
    parent_pcode text REFERENCES admin_areas(pcode),
    geom         geometry(Geometry,4326)   -- polygon for resolvable levels; NULL for upper refs
);
CREATE INDEX idx_admin_areas_geom   ON admin_areas USING gist (geom);
CREATE INDEX idx_admin_areas_parent ON admin_areas (parent_pcode);

-- Store the resolved chain as jsonb {"adm0":{"pcode","name"},...,"adm3":{...}}.
-- Indexable P-code columns are GENERATED from it (one source of truth, no drift,
-- only one extra insert param).
ALTER TABLE reports ADD COLUMN admin jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE reports ADD COLUMN adm1_pcode text GENERATED ALWAYS AS (admin->'adm1'->>'pcode') STORED;
ALTER TABLE reports ADD COLUMN adm2_pcode text GENERATED ALWAYS AS (admin->'adm2'->>'pcode') STORED;
ALTER TABLE reports ADD COLUMN adm3_pcode text GENERATED ALWAYS AS (admin->'adm3'->>'pcode') STORED;
CREATE INDEX idx_reports_adm1 ON reports (crisis_id, adm1_pcode);
CREATE INDEX idx_reports_adm2 ON reports (crisis_id, adm2_pcode);
CREATE INDEX idx_reports_adm3 ON reports (crisis_id, adm3_pcode);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE reports DROP COLUMN adm3_pcode;
ALTER TABLE reports DROP COLUMN adm2_pcode;
ALTER TABLE reports DROP COLUMN adm1_pcode;
ALTER TABLE reports DROP COLUMN admin;
DROP TABLE IF EXISTS admin_areas;
-- +goose StatementEnd
