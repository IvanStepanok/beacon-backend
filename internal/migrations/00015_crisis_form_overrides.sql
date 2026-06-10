-- +goose Up
-- +goose StatementBegin
-- Per-crisis modular-form overrides for GET /form-schema: a jsonb document like
-- {"required":["electricity"],"disabled":[]} applied over the built-in
-- Appendix-1 section defaults (sections default to optional — the modular
-- framing; a crisis can require or hide individual sections). NULL = pure
-- defaults, which is what every existing crisis keeps.
ALTER TABLE crises ADD COLUMN form_overrides jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crises DROP COLUMN form_overrides;
-- +goose StatementEnd
