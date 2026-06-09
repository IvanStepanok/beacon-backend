-- +goose Up
-- +goose StatementBegin
-- The challenge's REQUIRED core indicator is a 3-level damage classification
-- (minimal / partial / complete). Beacon also captures the richer 5-level EMS-98
-- grade (none/slight/moderate/severe/destroyed) when enabled, and ALWAYS derives
-- the required 3-tier rollup. So: the damage column accepts EITHER vocabulary, a
-- generated `damage_tier` column normalizes both to the 3 tiers, and an analyst can
-- flip the global capture scale (tier3 ⇄ ems98) via app_settings — applied to all
-- clients at once.

-- 1. widen the damage / ai_level / current_damage CHECKs to accept both vocabularies
ALTER TABLE reports DROP CONSTRAINT reports_damage_check;
ALTER TABLE reports ADD CONSTRAINT reports_damage_check
    CHECK (damage IN ('none','slight','moderate','severe','destroyed','minimal','partial','complete'));
ALTER TABLE reports DROP CONSTRAINT reports_ai_level_check;
ALTER TABLE reports ADD CONSTRAINT reports_ai_level_check
    CHECK (ai_level IN ('none','slight','moderate','severe','destroyed','minimal','partial','complete'));
ALTER TABLE buildings DROP CONSTRAINT buildings_current_damage_check;
ALTER TABLE buildings ADD CONSTRAINT buildings_current_damage_check
    CHECK (current_damage IN ('none','slight','moderate','severe','destroyed','minimal','partial','complete'));

-- 2. generated 3-tier rollup — always present, regardless of which vocabulary was stored
ALTER TABLE reports ADD COLUMN damage_tier text GENERATED ALWAYS AS (
    CASE damage
        WHEN 'none'      THEN 'minimal'  WHEN 'slight'   THEN 'minimal'  WHEN 'minimal'  THEN 'minimal'
        WHEN 'moderate'  THEN 'partial'  WHEN 'severe'   THEN 'partial'  WHEN 'partial'  THEN 'partial'
        WHEN 'destroyed' THEN 'complete' WHEN 'complete' THEN 'complete'
        ELSE 'minimal' END
) STORED;
CREATE INDEX idx_reports_damage_tier ON reports (crisis_id, damage_tier);

-- 3. global key/value settings; damage_scale drives the client capture UI (tier3|ems98)
CREATE TABLE app_settings (
    key        text PRIMARY KEY,
    value      text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO app_settings (key, value) VALUES ('damage_scale','tier3') ON CONFLICT (key) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS app_settings;
DROP INDEX IF EXISTS idx_reports_damage_tier;
ALTER TABLE reports DROP COLUMN damage_tier;
ALTER TABLE reports DROP CONSTRAINT reports_damage_check;
ALTER TABLE reports ADD CONSTRAINT reports_damage_check CHECK (damage IN ('none','slight','moderate','severe','destroyed'));
ALTER TABLE reports DROP CONSTRAINT reports_ai_level_check;
ALTER TABLE reports ADD CONSTRAINT reports_ai_level_check CHECK (ai_level IN ('none','slight','moderate','severe','destroyed'));
ALTER TABLE buildings DROP CONSTRAINT buildings_current_damage_check;
ALTER TABLE buildings ADD CONSTRAINT buildings_current_damage_check CHECK (current_damage IN ('none','slight','moderate','severe','destroyed'));
-- +goose StatementEnd
