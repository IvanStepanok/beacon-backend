-- +goose Up
-- +goose StatementBegin
-- ANONYMIZATION HONESTY: face/plate blurring was NEVER implemented (no blur code
-- exists anywhere in the stack), yet stored rows and the column default claimed
-- facesBlurred=true / platesBlurred=true. The API must never advertise a privacy
-- guarantee it does not deliver. EXIF stripping IS real on the mobile client, so
-- exifStripped stays true.
--
-- 1. backfill every existing row to the honest values (faces/plates = false).
UPDATE reports
SET anonymization = jsonb_set(
        jsonb_set(anonymization, '{facesBlurred}', 'false'::jsonb, true),
        '{platesBlurred}', 'false'::jsonb, true);

-- 2. fix the column DEFAULT so new rows inserted without an explicit anonymization
--    object are honest too (the app also forces these false on write + read).
ALTER TABLE reports ALTER COLUMN anonymization SET DEFAULT
    '{"anonymous":true,"exifStripped":true,"facesBlurred":false,"platesBlurred":false}'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Restore the previous (dishonest) default + values. Provided only for reversibility;
-- there is no real-world reason to re-enable a guarantee that is not implemented.
ALTER TABLE reports ALTER COLUMN anonymization SET DEFAULT
    '{"anonymous":true,"exifStripped":true,"facesBlurred":true,"platesBlurred":true}'::jsonb;
UPDATE reports
SET anonymization = jsonb_set(
        jsonb_set(anonymization, '{facesBlurred}', 'true'::jsonb, true),
        '{platesBlurred}', 'true'::jsonb, true);
-- +goose StatementEnd
