-- Danger zones (a hazard-avoidance / "areas to avoid" overlay) are out of scope for
-- this challenge, which is about crowdsourced damage REPORTING — not early-warning or
-- safety navigation. Removing the feature (table + index + API + UI) sharpens the
-- product around its mandated purpose. Forward-only drop (never edit applied 00001).
-- +goose Up
DROP TABLE IF EXISTS danger_zones;

-- +goose Down
-- No-op: danger zones are a removed feature; recreating the empty table serves nothing.
SELECT 1;
