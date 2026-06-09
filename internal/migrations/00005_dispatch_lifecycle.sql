-- +goose Up
-- +goose StatementBegin
-- The TASKING axis, orthogonal to verification: "verified" is the START of the
-- task lifecycle, not the end. Plus life-safety severity (fast lane) and cluster
-- routing. Modelled on 911/CAD dispatch + INSARAG worksite tasking.

ALTER TABLE reports ADD COLUMN task_status text NOT NULL DEFAULT 'new'
    CHECK (task_status IN ('new','triaged','assigned','in_progress','resolved','closed'));
-- terminal disposition (only set when closed) — includes negative/no-action outcomes
ALTER TABLE reports ADD COLUMN disposition text
    CHECK (disposition IN ('resolved','cleared_nothing_found','no_action_needed','gone_on_arrival','unfounded','duplicate','referred'));
ALTER TABLE reports ADD COLUMN assignee text;      -- owner / responder team
ALTER TABLE reports ADD COLUMN task_ref text;      -- stable human-readable task id (cf. INSARAG worksite A-1)
ALTER TABLE reports ADD COLUMN severity text NOT NULL DEFAULT 'routine'
    CHECK (severity IN ('routine','elevated','life_safety'));
ALTER TABLE reports ADD COLUMN life_safety boolean NOT NULL DEFAULT false; -- people at risk (intake question)
ALTER TABLE reports ADD COLUMN clusters text[] NOT NULL DEFAULT '{}';      -- sector routing (configurable multi-select)

CREATE INDEX idx_reports_task_status ON reports (crisis_id, task_status);
CREATE INDEX idx_reports_severity    ON reports (crisis_id, severity);
CREATE INDEX idx_reports_assignee    ON reports (assignee);
CREATE INDEX idx_reports_clusters    ON reports USING gin (clusters);
-- fast lane: open life-safety tasks first
CREATE INDEX idx_reports_lifesafety  ON reports (crisis_id, life_safety, task_status) WHERE life_safety;

-- audit trail for task transitions (mirrors report_verification_audit)
CREATE TABLE report_task_audit (
    id          bigserial PRIMARY KEY,
    report_id   text NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    field       text NOT NULL,        -- task_status | assignee | severity | disposition | clusters
    from_value  text,
    to_value    text,
    actor       text NOT NULL,
    note        text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_task_audit_report ON report_task_audit (report_id, created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS report_task_audit;
ALTER TABLE reports DROP COLUMN clusters;
ALTER TABLE reports DROP COLUMN life_safety;
ALTER TABLE reports DROP COLUMN severity;
ALTER TABLE reports DROP COLUMN task_ref;
ALTER TABLE reports DROP COLUMN assignee;
ALTER TABLE reports DROP COLUMN disposition;
ALTER TABLE reports DROP COLUMN task_status;
-- +goose StatementEnd
