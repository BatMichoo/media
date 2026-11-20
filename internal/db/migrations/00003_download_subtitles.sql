-- +goose Up
-- Subtitles people uploaded for a download, kept under
-- DATA_DIR/subtitles/<hash>/<id><ext> and linked next to their videos
-- each time the download is filed.
CREATE TABLE download_subtitles (
  hash     TEXT NOT NULL REFERENCES downloads ON DELETE CASCADE,
  id       TEXT NOT NULL,              -- random; also the kept file's name
  lang     TEXT NOT NULL,              -- "bg" or "en" (file name code)
  ext      TEXT NOT NULL,              -- ".srt", ".ass", ".ssa", ".vtt"
  name     TEXT NOT NULL,              -- upload's name, or "S01E03" / "part2" / "movie"
  video    TEXT NOT NULL DEFAULT '',   -- pinned library video path, or ''
  added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (hash, id)
);

-- +goose Down
DROP TABLE download_subtitles;
