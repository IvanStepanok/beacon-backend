-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS pgcrypto;   -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS pg_trgm;    -- q free-text search

-- enum-like columns use TEXT + CHECK (not native ENUM) so UNDP can extend
-- crisis-nature / modular question sets without blocking ALTER TYPE migrations.

CREATE TABLE crises (
    id            text PRIMARY KEY,
    title         text        NOT NULL,
    area          text        NOT NULL,
    nature        text        NOT NULL DEFAULT 'earthquake',
    geom          geometry(Point,4326) NOT NULL,
    center_lat    double precision NOT NULL,
    center_lng    double precision NOT NULL,
    source        text        NOT NULL DEFAULT 'UNDP RAPIDA',
    started_at    timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE submitters (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    anonymous_id   text NOT NULL UNIQUE,
    alias          text,
    report_count   integer NOT NULL DEFAULT 0,
    building_count integer NOT NULL DEFAULT 0,
    points         integer NOT NULL DEFAULT 0,
    badges         jsonb   NOT NULL DEFAULT '[]'::jsonb,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE buildings (
    id             text PRIMARY KEY,
    crisis_id      text NOT NULL REFERENCES crises(id),
    geom           geometry(Point,4326),
    lat            double precision,
    lng            double precision,
    current_damage text CHECK (current_damage IN ('minimal','partial','complete')),
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- reports.id is the CLIENT-SUPPLIED string id (mobile UUID string OR seed numeric string),
-- stored as text so POST is an idempotent UPSERT on the PK and the original id round-trips
-- to both clients verbatim. supersedes_report_id is a self-FK on the same text id.
CREATE TABLE reports (
    id                    text PRIMARY KEY,                       -- CLIENT-SUPPLIED => POST UPSERT = idempotency
    idempotency_key       text NOT NULL UNIQUE,
    crisis_id             text NOT NULL DEFAULT 'crisis-antakya' REFERENCES crises(id),
    submitter_id          uuid REFERENCES submitters(id),
    damage                text NOT NULL CHECK (damage IN ('minimal','partial','complete')),
    verification          text NOT NULL DEFAULT 'pending' CHECK (verification IN ('pending','verified','flagged')),
    debris                text NOT NULL DEFAULT 'unsure' CHECK (debris IN ('yes','no','unsure')),
    infra_types           text[] NOT NULL DEFAULT '{}',
    infra_other_detail    text,
    crisis_nature         text[] NOT NULL DEFAULT '{earthquake}',
    geom                  geometry(Point,4326) NOT NULL,
    lat                   double precision NOT NULL,
    lng                   double precision NOT NULL,
    gps_accuracy_m        double precision,
    building_id           text REFERENCES buildings(id),
    version               integer NOT NULL DEFAULT 1,
    supersedes_report_id  text REFERENCES reports(id),
    what3words            text,
    plus_code             text,
    landmark              text,
    place                 text NOT NULL DEFAULT '',
    desc_original         text,
    desc_original_lang    text,
    desc_translated       text,
    desc_translated_lang  text,
    ai_level              text CHECK (ai_level IN ('minimal','partial','complete')),
    ai_confidence         smallint CHECK (ai_confidence BETWEEN 0 AND 100),
    photos                jsonb NOT NULL DEFAULT '[]'::jsonb,
    size_bytes            bigint NOT NULL DEFAULT 0,
    modular               jsonb,
    anonymization         jsonb NOT NULL DEFAULT
        '{"anonymous":true,"exifStripped":true,"facesBlurred":true,"platesBlurred":true}'::jsonb,
    is_mine               boolean NOT NULL DEFAULT false,
    synced                boolean NOT NULL DEFAULT true,
    sync_state            jsonb NOT NULL DEFAULT '{"type":"Synced"}'::jsonb,
    captured_at           timestamptz NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_infra_types CHECK (
        infra_types <@ ARRAY['residential','commercial','government','utility','transport','community','public','other']::text[]),
    CONSTRAINT chk_crisis_nature CHECK (
        crisis_nature <@ ARRAY['earthquake','flood','tsunami','hurricane','wildfire','explosion','chemical','conflict','civil_unrest']::text[])
);

CREATE TABLE danger_zones (
    id         text PRIMARY KEY,
    crisis_id  text NOT NULL REFERENCES crises(id),
    name       text NOT NULL,
    note       text NOT NULL,
    severity   text NOT NULL CHECK (severity IN ('caution','warning','critical')),
    geom       geometry(Geometry,4326)
);

CREATE TABLE report_verification_audit (
    id          bigserial PRIMARY KEY,
    report_id   text NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    from_status text NOT NULL,
    to_status   text NOT NULL,
    actor       text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- spatial indexes (map bbox + clustering + danger overlay)
CREATE INDEX idx_reports_geom        ON reports      USING gist (geom);
CREATE INDEX idx_buildings_geom      ON buildings    USING gist (geom);
CREATE INDEX idx_danger_zones_geom   ON danger_zones USING gist (geom);
-- dashboard filter / sort / pagination btrees (lead with crisis_id for tenant isolation)
CREATE INDEX idx_reports_crisis_captured ON reports (crisis_id, captured_at DESC);
CREATE INDEX idx_reports_damage          ON reports (crisis_id, damage);
CREATE INDEX idx_reports_verification    ON reports (crisis_id, verification);
CREATE INDEX idx_reports_building        ON reports (building_id, captured_at DESC);
CREATE INDEX idx_reports_place           ON reports (crisis_id, place);
CREATE INDEX idx_reports_submitter       ON reports (submitter_id);
-- free-text q search on place
CREATE INDEX idx_reports_place_trgm      ON reports USING gin (place gin_trgm_ops);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS report_verification_audit;
DROP TABLE IF EXISTS danger_zones;
DROP TABLE IF EXISTS reports;
DROP TABLE IF EXISTS buildings;
DROP TABLE IF EXISTS submitters;
DROP TABLE IF EXISTS crises;
DROP EXTENSION IF EXISTS pg_trgm;
DROP EXTENSION IF EXISTS pgcrypto;
DROP EXTENSION IF EXISTS postgis;
-- +goose StatementEnd
