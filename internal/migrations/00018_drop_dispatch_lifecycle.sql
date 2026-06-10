-- The emergency-DISPATCH lifecycle (task tracking, responder assignment, severity
-- fast-lane, terminal dispositions, INSARAG-style worksite refs) is removed. Per the
-- challenge Q&A (Q23), Beacon produces a structured situational-awareness DATASET to
-- guide field assessment — it is NOT a responder dispatch / CAD system, and shipping
-- one overstates the tool's role and the responsibility it can carry. We KEEP:
--   - verification (verify / pending / flag) + its audit trail, and
--   - the affected-sector CLUSTER tags (an OCHA-aligned data dimension).
-- Drop the tasking axis columns, their indexes, and the task-audit table.
-- +goose Up
DROP INDEX IF EXISTS idx_reports_task_status;
DROP INDEX IF EXISTS idx_reports_severity;
DROP INDEX IF EXISTS idx_reports_assignee;
DROP INDEX IF EXISTS idx_reports_lifesafety;
DROP TABLE IF EXISTS report_task_audit;
ALTER TABLE reports
  DROP COLUMN IF EXISTS task_status,
  DROP COLUMN IF EXISTS disposition,
  DROP COLUMN IF EXISTS assignee,
  DROP COLUMN IF EXISTS task_ref,
  DROP COLUMN IF EXISTS severity,
  DROP COLUMN IF EXISTS life_safety;

-- +goose Down
-- No-op: the dispatch lifecycle is a removed capability; recreating empty columns
-- and an empty audit table serves nothing.
SELECT 1;
