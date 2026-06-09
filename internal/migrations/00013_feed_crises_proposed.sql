-- Feed-detected crises are detections, not operations: they are born 'proposed'
-- and become 'active' only when a community ground report is assigned to them
-- (store.Crises.ActivateIfProposed) or an analyst activates them. Demote the
-- feed-sourced 'active' rows that never received a single report — they were
-- auto-activated by the old ingest path and were polluting the "newest active
-- crisis" default used by stats/export/map scoping.
-- +goose Up
UPDATE crises
SET status = 'proposed'
WHERE source LIKE 'feed:%'
  AND status = 'active'
  AND report_count = 0;

-- +goose Down
-- No-op: the pre-migration set of auto-activated feed crises is not recorded,
-- and re-activating every feed event would recreate the defect being fixed.
SELECT 1;
