-- +goose Up
-- +goose StatementBegin
-- Composite index for count(DISTINCT submitter_id) per crisis. crisisSelect now embeds
-- a `(SELECT count(DISTINCT r.submitter_id) FROM reports r WHERE r.crisis_id = crises.id)`
-- correlated subquery (the corroboration signal on every /crises, /crises/near,
-- /crises/active response). The existing idx_reports_crisis_captured (crisis_id,
-- captured_at) cannot serve a DISTINCT on submitter_id, and idx_reports_submitter leads
-- with the wrong column. This (crisis_id, submitter_id) index lets the distinct count be
-- answered from the index for a given crisis instead of heap-reading every report row —
-- the difference between cheap and a full per-crisis scan at the 500k-report target.
CREATE INDEX IF NOT EXISTS idx_reports_crisis_submitter ON reports (crisis_id, submitter_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_reports_crisis_submitter;
-- +goose StatementEnd
