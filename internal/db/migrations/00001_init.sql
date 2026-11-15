-- +goose Up
-- What was added through the app, and what it became.
CREATE TABLE downloads (
  hash TEXT PRIMARY KEY,                 -- Transmission hashString, lowercase
  kind TEXT NOT NULL CHECK (kind IN ('movie','series')),
  title TEXT NOT NULL,
  year INT,
  name TEXT NOT NULL DEFAULT '',         -- torrent name
  added_by UUID NOT NULL,
  added_by_name TEXT NOT NULL,
  added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  state TEXT NOT NULL DEFAULT 'downloading' CHECK (state IN ('downloading','filed','failed')),
  reason TEXT NOT NULL DEFAULT ''
);

-- Every hard link the app made in library/, so it can be undone exactly.
CREATE TABLE download_links (
  hash TEXT NOT NULL REFERENCES downloads ON DELETE CASCADE,
  path TEXT NOT NULL,                    -- relative to DATA_DIR
  PRIMARY KEY (hash, path)
);

-- Where each person is in each video.
CREATE TABLE progress (
  user_id UUID NOT NULL,
  item_id TEXT NOT NULL,                 -- Jellyfin id (movie or episode)
  series_id TEXT,                        -- set for episodes
  part INT NOT NULL DEFAULT 0,           -- 0-based part of a multi-part movie
  position_seconds DOUBLE PRECISION NOT NULL,  -- within that part
  duration_seconds DOUBLE PRECISION NOT NULL,  -- whole item (all parts)
  finished BOOLEAN NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, item_id)
);
CREATE INDEX progress_recent ON progress (user_id, updated_at DESC);

-- Last chosen subtitle and audio language per person.
CREATE TABLE preferences (
  user_id UUID PRIMARY KEY,
  subtitle_lang TEXT NOT NULL DEFAULT '',  -- '' none yet, 'off', or Jellyfin's language code
  audio_lang TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE preferences;
DROP TABLE progress;
DROP TABLE download_links;
DROP TABLE downloads;
