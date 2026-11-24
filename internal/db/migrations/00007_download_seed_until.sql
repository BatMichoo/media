-- +goose Up
-- Seeding started again by hand: until seed_until, or with no limit.
-- Without either, a finished torrent seeds for the app's default time.
ALTER TABLE downloads ADD COLUMN seed_until TIMESTAMPTZ;
ALTER TABLE downloads ADD COLUMN seed_no_limit BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE downloads DROP COLUMN seed_no_limit;
ALTER TABLE downloads DROP COLUMN seed_until;
