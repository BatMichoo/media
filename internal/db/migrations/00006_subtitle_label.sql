-- +goose Up
-- Tells apart several subtitles uploaded together for the same video and
-- language (e.g. timings for different releases); '' for a lone one. It
-- goes into the library file name: <video>.<label>.<lang><ext>.
ALTER TABLE download_subtitles ADD COLUMN label TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE download_subtitles DROP COLUMN label;
