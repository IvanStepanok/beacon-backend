-- +goose Up
-- +goose StatementBegin
-- Analyst accounts + RBAC. Reporters stay ANONYMOUS (X-Device-Id → submitters);
-- this table is only for the analyst side (Country Office / Regional Bureau /
-- Crisis Bureau). crisis_scope = the crises a user may see; '{*}' (contains '*')
-- means all. region maps to a UNDP Regional Bureau (e.g. RBEC for Türkiye).
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    name          text NOT NULL,
    role          text NOT NULL CHECK (role IN
        ('field_validator','co_analyst','regional_analyst','crisis_admin','external_viewer')),
    region        text,
    crisis_scope  text[] NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS users;
-- +goose StatementEnd
