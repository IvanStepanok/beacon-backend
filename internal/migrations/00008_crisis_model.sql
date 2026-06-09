-- +goose Up
-- +goose StatementBegin
-- A "crisis" is a DISCRETE EVENT (assessment unit), not a permanent state. This
-- migration gives it spatial + temporal extent and a lifecycle so we can:
--   * serve location-first clients ("is there an active crisis near me?"),
--   * assign reports to a crisis SERVER-SIDE by space+time (no more one hard-coded
--     crisis), leaving reports that match nothing as pending (crisis_id NULL),
--   * support EMERGENT crises — a cluster of citizen reports proposes a new event
--     before any feed/analyst declares it (status='proposed' → analyst confirms).

-- Long-running umbrella (e.g. "Ukraine humanitarian response"). Hundreds of
-- discrete event-crises per year can roll up to a handful of responses.
CREATE TABLE responses (
    id          text PRIMARY KEY,
    title       text NOT NULL,
    nature      text NOT NULL DEFAULT 'conflict',
    started_at  timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE crises ADD COLUMN radius_km    double precision NOT NULL DEFAULT 40;  -- coverage radius around the center point (km)
ALTER TABLE crises ADD COLUMN ended_at     timestamptz;                           -- NULL = still ongoing
ALTER TABLE crises ADD COLUMN status       text NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','proposed','closed','dismissed'));
ALTER TABLE crises ADD COLUMN response_id  text REFERENCES responses(id);         -- optional umbrella
ALTER TABLE crises ADD COLUMN report_count integer NOT NULL DEFAULT 0;            -- denormalized cluster size (emergent + display)
-- crises.source stays free text; convention: 'UNDP RAPIDA' | 'analyst' |
-- 'feed:GDACS' | 'feed:USGS' | 'feed:Copernicus' | 'emergent' (new feeds need no migration).

-- Index the center so "crises near me" + report assignment are index-backed.
CREATE INDEX idx_crises_geom           ON crises USING gist (geom);
CREATE INDEX idx_crises_status_started ON crises (status, started_at DESC);

-- Reports can exist BEFORE any crisis is declared (citizen reports a strike; the
-- crisis is assigned/created later). crisis_id becomes NULLABLE = "pending".
ALTER TABLE reports   ALTER COLUMN crisis_id DROP DEFAULT;
ALTER TABLE reports   ALTER COLUMN crisis_id DROP NOT NULL;
ALTER TABLE buildings ALTER COLUMN crisis_id DROP NOT NULL;

-- Partial index: the emergent-cluster scan + dashboard "unassigned" view.
CREATE INDEX idx_reports_pending ON reports (captured_at DESC) WHERE crisis_id IS NULL;

-- Give the already-seeded Antakya crisis a real coverage radius + explicit status
-- (no-op on a fresh DB, where the seeder inserts the row after migrations run).
UPDATE crises SET radius_km = 25, status = 'active' WHERE id = 'crisis-antakya';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_reports_pending;
ALTER TABLE reports ALTER COLUMN crisis_id SET DEFAULT 'crisis-antakya';
DROP INDEX IF EXISTS idx_crises_status_started;
DROP INDEX IF EXISTS idx_crises_geom;
ALTER TABLE crises DROP COLUMN report_count;
ALTER TABLE crises DROP COLUMN response_id;
ALTER TABLE crises DROP COLUMN status;
ALTER TABLE crises DROP COLUMN ended_at;
ALTER TABLE crises DROP COLUMN radius_km;
DROP TABLE IF EXISTS responses;
-- crisis_id left NULLABLE on down: restoring NOT NULL would fail if pending rows exist.
-- +goose StatementEnd
