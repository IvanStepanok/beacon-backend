-- The automated USGS/GDACS disaster-feed ingest is removed: Beacon is a community
-- damage-REPORTING tool, not a global disaster tracker, and the auto-ingested feed
-- events polluted the crisis registry (the "newest active crisis" scoping default in
-- particular). Remove leftover feed-sourced crises that never received a community
-- report. Analyst-declared and community-EMERGENT crises (source='emergent') are
-- untouched — those are in-scope and report-backed. Pure code now creates crises only
-- from analyst declaration or community ground-truth clustering.
-- +goose Up
DELETE FROM crises c
WHERE c.source LIKE 'feed:%'
  AND NOT EXISTS (SELECT 1 FROM reports r WHERE r.crisis_id = c.id);

-- +goose Down
-- No-op: feed-sourced crises are a removed capability; re-creating them serves nothing.
SELECT 1;
