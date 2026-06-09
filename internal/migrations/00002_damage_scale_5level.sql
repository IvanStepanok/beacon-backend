-- +goose Up
-- +goose StatementBegin
-- Migrate the damage scale from 3-level (minimal/partial/complete) to the 5-level
-- ordinal grade aligned to EMS-98 / Copernicus EMS / UNOSAT
-- (none < slight < moderate < severe < destroyed), plus a "possibly damaged"
-- confidence flag (the satellite class Beacon's ground reports resolve).

-- 1. migrate existing data to the new vocabulary before tightening constraints
UPDATE reports SET damage = CASE damage
    WHEN 'minimal'  THEN 'slight'
    WHEN 'partial'  THEN 'severe'
    WHEN 'complete' THEN 'destroyed'
    ELSE damage END;
UPDATE reports SET ai_level = CASE ai_level
    WHEN 'minimal'  THEN 'slight'
    WHEN 'partial'  THEN 'severe'
    WHEN 'complete' THEN 'destroyed'
    ELSE ai_level END
    WHERE ai_level IS NOT NULL;
UPDATE buildings SET current_damage = CASE current_damage
    WHEN 'minimal'  THEN 'slight'
    WHEN 'partial'  THEN 'severe'
    WHEN 'complete' THEN 'destroyed'
    ELSE current_damage END
    WHERE current_damage IS NOT NULL;

-- 2. swap the CHECK constraints for the 5-level vocabulary
ALTER TABLE reports DROP CONSTRAINT reports_damage_check;
ALTER TABLE reports ADD CONSTRAINT reports_damage_check
    CHECK (damage IN ('none','slight','moderate','severe','destroyed'));
ALTER TABLE reports DROP CONSTRAINT reports_ai_level_check;
ALTER TABLE reports ADD CONSTRAINT reports_ai_level_check
    CHECK (ai_level IN ('none','slight','moderate','severe','destroyed'));
ALTER TABLE buildings DROP CONSTRAINT buildings_current_damage_check;
ALTER TABLE buildings ADD CONSTRAINT buildings_current_damage_check
    CHECK (current_damage IN ('none','slight','moderate','severe','destroyed'));

-- 3. "possibly damaged" confidence flag — reporter unsure / resolves the
--    satellite "possibly damaged" class. Orthogonal to the ordinal grade.
ALTER TABLE reports ADD COLUMN possibly_damaged boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE reports DROP COLUMN possibly_damaged;
UPDATE reports SET damage = CASE damage
    WHEN 'none' THEN 'minimal' WHEN 'slight' THEN 'minimal'
    WHEN 'moderate' THEN 'partial' WHEN 'severe' THEN 'partial'
    WHEN 'destroyed' THEN 'complete' ELSE damage END;
UPDATE reports SET ai_level = CASE ai_level
    WHEN 'none' THEN 'minimal' WHEN 'slight' THEN 'minimal'
    WHEN 'moderate' THEN 'partial' WHEN 'severe' THEN 'partial'
    WHEN 'destroyed' THEN 'complete' ELSE ai_level END WHERE ai_level IS NOT NULL;
UPDATE buildings SET current_damage = CASE current_damage
    WHEN 'none' THEN 'minimal' WHEN 'slight' THEN 'minimal'
    WHEN 'moderate' THEN 'partial' WHEN 'severe' THEN 'partial'
    WHEN 'destroyed' THEN 'complete' ELSE current_damage END WHERE current_damage IS NOT NULL;
ALTER TABLE reports DROP CONSTRAINT reports_damage_check;
ALTER TABLE reports ADD CONSTRAINT reports_damage_check CHECK (damage IN ('minimal','partial','complete'));
ALTER TABLE reports DROP CONSTRAINT reports_ai_level_check;
ALTER TABLE reports ADD CONSTRAINT reports_ai_level_check CHECK (ai_level IN ('minimal','partial','complete'));
ALTER TABLE buildings DROP CONSTRAINT buildings_current_damage_check;
ALTER TABLE buildings ADD CONSTRAINT buildings_current_damage_check CHECK (current_damage IN ('minimal','partial','complete'));
-- +goose StatementEnd
