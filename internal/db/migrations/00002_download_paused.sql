-- +goose Up
-- Paused by someone in the app (downloading or seeding), so a paused
-- torrent isn't taken for one whose seeding is over.
ALTER TABLE downloads ADD COLUMN paused BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE downloads DROP COLUMN paused;
