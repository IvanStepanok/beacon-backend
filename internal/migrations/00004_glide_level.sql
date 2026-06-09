-- +goose Up
-- +goose StatementBegin
-- GLIDE = the cross-org disaster event key (XX-YYYY-NNNNNN-CCC) linking Beacon
-- data to ReliefWeb / UNDRR / Copernicus activations. response_level = UNDP's
-- corporate crisis Level (1/2/3) so a deployment matches the Country Office posture.
ALTER TABLE crises ADD COLUMN glide          text;
ALTER TABLE crises ADD COLUMN response_level smallint CHECK (response_level BETWEEN 1 AND 3);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crises DROP COLUMN response_level;
ALTER TABLE crises DROP COLUMN glide;
-- +goose StatementEnd
