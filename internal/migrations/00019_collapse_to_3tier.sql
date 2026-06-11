-- +goose Up
-- +goose StatementBegin
-- Collapse the optional 5-level EMS-98 capture vocabulary down to the challenge's
-- MANDATED 3-tier classification (minimal / partial / complete) — the only damage
-- scale the product now uses end-to-end. Existing 5-level grades are re-mapped to
-- their tier (none/slight→minimal, moderate/severe→partial, destroyed→complete),
-- the damage / ai_level / current_damage CHECKs are narrowed to the 3 tiers, and the
-- global damage_scale toggle is retired. The generated damage_tier column is KEPT
-- (now a pass-through) so every export / stat / tile that joins on it is untouched.

-- 1. re-map any stored 5-level grades to their 3-tier rollup. MUST run before the
--    narrowed CHECK; the generated damage_tier column recomputes automatically.
UPDATE reports SET damage = CASE damage
        WHEN 'none' THEN 'minimal' WHEN 'slight' THEN 'minimal'
        WHEN 'moderate' THEN 'partial' WHEN 'severe' THEN 'partial'
        WHEN 'destroyed' THEN 'complete' ELSE damage END
    WHERE damage IN ('none','slight','moderate','severe','destroyed');
UPDATE reports SET ai_level = CASE ai_level
        WHEN 'none' THEN 'minimal' WHEN 'slight' THEN 'minimal'
        WHEN 'moderate' THEN 'partial' WHEN 'severe' THEN 'partial'
        WHEN 'destroyed' THEN 'complete' ELSE ai_level END
    WHERE ai_level IN ('none','slight','moderate','severe','destroyed');
UPDATE buildings SET current_damage = CASE current_damage
        WHEN 'none' THEN 'minimal' WHEN 'slight' THEN 'minimal'
        WHEN 'moderate' THEN 'partial' WHEN 'severe' THEN 'partial'
        WHEN 'destroyed' THEN 'complete' ELSE current_damage END
    WHERE current_damage IN ('none','slight','moderate','severe','destroyed');

-- 2. narrow the CHECKs to the 3 mandated tiers only
ALTER TABLE reports DROP CONSTRAINT reports_damage_check;
ALTER TABLE reports ADD CONSTRAINT reports_damage_check
    CHECK (damage IN ('minimal','partial','complete'));
ALTER TABLE reports DROP CONSTRAINT reports_ai_level_check;
ALTER TABLE reports ADD CONSTRAINT reports_ai_level_check
    CHECK (ai_level IN ('minimal','partial','complete'));
ALTER TABLE buildings DROP CONSTRAINT buildings_current_damage_check;
ALTER TABLE buildings ADD CONSTRAINT buildings_current_damage_check
    CHECK (current_damage IN ('minimal','partial','complete'));

-- 3. retire the global capture-scale toggle (the 5-level UI is gone from all clients)
DELETE FROM app_settings WHERE key = 'damage_scale';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Re-widen the CHECKs to accept both vocabularies again. The forward data re-map is
-- lossy (5-level detail is gone), so Down only restores the constraint envelope.
ALTER TABLE reports DROP CONSTRAINT reports_damage_check;
ALTER TABLE reports ADD CONSTRAINT reports_damage_check
    CHECK (damage IN ('none','slight','moderate','severe','destroyed','minimal','partial','complete'));
ALTER TABLE reports DROP CONSTRAINT reports_ai_level_check;
ALTER TABLE reports ADD CONSTRAINT reports_ai_level_check
    CHECK (ai_level IN ('none','slight','moderate','severe','destroyed','minimal','partial','complete'));
ALTER TABLE buildings DROP CONSTRAINT buildings_current_damage_check;
ALTER TABLE buildings ADD CONSTRAINT buildings_current_damage_check
    CHECK (current_damage IN ('none','slight','moderate','severe','destroyed','minimal','partial','complete'));
INSERT INTO app_settings (key, value) VALUES ('damage_scale','tier3') ON CONFLICT (key) DO NOTHING;
-- +goose StatementEnd
