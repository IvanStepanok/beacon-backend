-- +goose Up
-- +goose StatementBegin
-- Server-side photo: once a reporter uploads the captured image (multipart), the file is stored
-- on the API's photo volume and this column holds the URL the dashboard/app can GET.
ALTER TABLE reports ADD COLUMN IF NOT EXISTS photo_url text;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE reports DROP COLUMN IF EXISTS photo_url;
-- +goose StatementEnd
