-- +goose Up
-- +goose StatementBegin
-- Analyst MFA (TOTP, RFC 6238). mfa_secret holds the per-user TOTP secret ENCRYPTED
-- at rest (AES-256-GCM via DATA_ENCRYPTION_KEY, stored as base64 text); it is written
-- at enrollment and mfa_enabled only flips true once the analyst verifies a first code.
-- Reporters are anonymous (X-Device-Id) and never have accounts, so this is analyst-only.
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_secret  text;
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_enabled boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN IF EXISTS mfa_enabled;
ALTER TABLE users DROP COLUMN IF EXISTS mfa_secret;
-- +goose StatementEnd
