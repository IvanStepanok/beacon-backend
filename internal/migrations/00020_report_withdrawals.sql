-- +goose Up
-- +goose StatementBegin
-- Reporter-initiated takedown (data-subject erasure right). When a reporter withdraws
-- their OWN report, the report row is ERASED (true erasure, not hiding — it disappears
-- from every read path) and only this NON-PII record-of-erasure is retained for
-- accountability. Deliberately NOT a foreign key to reports: the report no longer exists.
CREATE TABLE report_withdrawals (
    id           bigserial PRIMARY KEY,
    report_id    text NOT NULL,
    submitter_id uuid,
    crisis_id    text,
    withdrawn_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_report_withdrawals_at ON report_withdrawals (withdrawn_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS report_withdrawals;
-- +goose StatementEnd
