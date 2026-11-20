-- +goose Up
-- Downloads its pieces in order, so it can be watched while downloading.
-- Turned off from the app when in-order downloading stalls.
ALTER TABLE downloads ADD COLUMN in_order BOOLEAN NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE downloads DROP COLUMN in_order;
