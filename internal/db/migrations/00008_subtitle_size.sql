-- +goose Up
-- How big a person's subtitles show, in percent of the player's default.
ALTER TABLE preferences ADD COLUMN subtitle_size INTEGER NOT NULL DEFAULT 100;

-- +goose Down
ALTER TABLE preferences DROP COLUMN subtitle_size;
