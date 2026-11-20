package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Subtitle is a subtitle someone uploaded for a download.
type Subtitle struct {
	Hash, ID, Lang, Ext, Name string
	Video                     string // pinned library video path, or ""
	AddedAt                   time.Time
}

// AddSubtitle records an uploaded subtitle.
func (d *DB) AddSubtitle(ctx context.Context, s Subtitle) error {
	_, err := d.SQL.ExecContext(ctx, `INSERT INTO download_subtitles (hash, id, lang, ext, name, video)
		VALUES ($1, $2, $3, $4, $5, $6)`, s.Hash, s.ID, s.Lang, s.Ext, s.Name, s.Video)
	return err
}

// Subtitles is a download's uploaded subtitles, oldest first.
func (d *DB) Subtitles(ctx context.Context, hash string) ([]Subtitle, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT hash, id, lang, ext, name, video, added_at
		FROM download_subtitles WHERE hash = $1 ORDER BY added_at, id`, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subtitle
	for rows.Next() {
		var s Subtitle
		if err := rows.Scan(&s.Hash, &s.ID, &s.Lang, &s.Ext, &s.Name, &s.Video, &s.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteSubtitle forgets one uploaded subtitle.
func (d *DB) DeleteSubtitle(ctx context.Context, hash, id string) error {
	_, err := d.SQL.ExecContext(ctx, `DELETE FROM download_subtitles WHERE hash = $1 AND id = $2`, hash, id)
	return err
}

// LinkOwner is the download that made this library link; ErrNotFound
// when none did (a file put there by hand).
func (d *DB) LinkOwner(ctx context.Context, path string) (string, error) {
	var hash string
	err := d.SQL.QueryRowContext(ctx, `SELECT hash FROM download_links WHERE path = $1 ORDER BY hash LIMIT 1`, path).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

// DeleteLink forgets one library link of a download.
func (d *DB) DeleteLink(ctx context.Context, hash, path string) error {
	_, err := d.SQL.ExecContext(ctx, `DELETE FROM download_links WHERE hash = $1 AND path = $2`, hash, path)
	return err
}
