-- +goose Up
-- +goose StatementBegin
-- 1. NEVER fabricate a hazard: an empty crisisNature stays empty. The old
--    '{earthquake}' default invented data the reporter never asserted (and the
--    service used to fill the same default on submit — also removed).
ALTER TABLE reports ALTER COLUMN crisis_nature SET DEFAULT '{}';

-- 2. Named infrastructure ("Cumhuriyet Primary School") for ANY infra type, and
--    footprint provenance: building_source = 'footprint' ONLY when a real
--    footprint polygon was tapped on the map (GPS-grid "b-" ids are NOT footprints).
ALTER TABLE reports ADD COLUMN infra_name      text;
ALTER TABLE reports ADD COLUMN building_source text;

-- 3. plus_code consolidation: the legacy what3words column was a MISNAMED plus-code
--    carrier (current clients always sent a plus code in it). plus_code already
--    exists (00001), so instead of a rename: merge the legacy values into plus_code
--    and drop the column. The API keeps accepting the `what3words` submit key and
--    keeps emitting it as an alias of plusCode (same value) for existing mobile builds.
--    Merge ONLY values shaped like a Plus Code (4–8 chars of the base-20 alphabet,
--    '+', 2–3 more — covers short and full codes like "8G7F6526+VC"): early rows may
--    hold a REAL what3words phrase ("garden.tribe.sparkle"), which is NOT a Plus Code
--    and would corrupt the column. Non-matching legacy values are DROPPED with the
--    column — a w3w phrase cannot be converted to a plus code offline, and the
--    report's authoritative location (lat/lng or landmark) is unaffected.
UPDATE reports SET plus_code = what3words
 WHERE plus_code IS NULL AND what3words IS NOT NULL
   AND what3words ~ '^[23456789CFGHJMPQRVWX]{4,8}\+[23456789CFGHJMPQRVWX]{2,3}$';
ALTER TABLE reports DROP COLUMN what3words;

-- 4. Verification audit: record the analyst's free-text note and whether the
--    photo gate was overridden (force) alongside the existing from/to/actor row.
ALTER TABLE report_verification_audit ADD COLUMN note   text;
ALTER TABLE report_verification_audit ADD COLUMN forced boolean NOT NULL DEFAULT false;

-- 5. place backfill: "Your location" is a client UI placeholder, not a real place
--    name. The submit path now stores '' instead (service.normalize), but that
--    sanitizer only covers NEW submits — clean the rows that predate it. Idempotent
--    ('' is this schema's "no place"; AreaGroups already treats '' as absent).
UPDATE reports SET place = '' WHERE place = 'Your location';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE report_verification_audit DROP COLUMN forced;
ALTER TABLE report_verification_audit DROP COLUMN note;
-- Best-effort: the pre-merge what3words values are not recoverable after Up
-- (they were folded into plus_code), so restore the column as a copy.
ALTER TABLE reports ADD COLUMN what3words text;
UPDATE reports SET what3words = plus_code;
ALTER TABLE reports DROP COLUMN building_source;
ALTER TABLE reports DROP COLUMN infra_name;
ALTER TABLE reports ALTER COLUMN crisis_nature SET DEFAULT '{earthquake}';
-- +goose StatementEnd
