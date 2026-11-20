-- +goose Up
-- Reasons the app writes itself are kept as a message key (and a count)
-- and translated when shown; reason keeps only text from elsewhere
-- (Transmission's errors, the file system's).
ALTER TABLE downloads ADD COLUMN reason_key TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN reason_n INT NOT NULL DEFAULT 0;
UPDATE downloads SET reason_key = 'reason.removed', reason = '' WHERE reason = 'removed from Transmission';
UPDATE downloads SET reason_key = 'reason.already_in_library', reason = '' WHERE reason = 'already in the library';
UPDATE downloads SET reason_key = 'reason.no_video', reason = '' WHERE reason = 'no video files';
UPDATE downloads SET reason_key = 'reason.subs_unplaced', reason_n = substring(reason FROM '^(\d+) ')::int, reason = ''
  WHERE reason ~ '^\d+ subtitles? didn''t match a video$';

-- +goose Down
UPDATE downloads SET reason = CASE reason_key
    WHEN 'reason.removed' THEN 'removed from Transmission'
    WHEN 'reason.already_in_library' THEN 'already in the library'
    WHEN 'reason.no_video' THEN 'no video files'
    WHEN 'reason.subs_unplaced' THEN reason_n || CASE WHEN reason_n = 1 THEN ' subtitle' ELSE ' subtitles' END || ' didn''t match a video'
    ELSE reason END
  WHERE reason_key <> '';
ALTER TABLE downloads DROP COLUMN reason_n;
ALTER TABLE downloads DROP COLUMN reason_key;
