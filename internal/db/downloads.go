package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound means there's no such row.
var ErrNotFound = errors.New("not found")

// Download is a row of downloads: a torrent added through the app.
type Download struct {
	Hash, Kind, Title    string
	Year                 int // 0 = none
	Name                 string
	AddedBy, AddedByName string
	AddedAt              time.Time
	State, Reason        string
	Paused               bool // paused in the app
	InOrder              bool // downloads its pieces in order (new rows: true)
}

const downloadCols = `hash, kind, title, COALESCE(year, 0), name, added_by, added_by_name, added_at, state, reason, paused, in_order`

func scanDownload(row interface{ Scan(...any) error }) (Download, error) {
	var d Download
	err := row.Scan(&d.Hash, &d.Kind, &d.Title, &d.Year, &d.Name, &d.AddedBy, &d.AddedByName, &d.AddedAt, &d.State, &d.Reason, &d.Paused, &d.InOrder)
	return d, err
}

func nullYear(y int) any {
	if y == 0 {
		return nil
	}
	return y
}

// InsertDownload records a new download.
func (d *DB) InsertDownload(ctx context.Context, dl Download) error {
	_, err := d.SQL.ExecContext(ctx, `INSERT INTO downloads (hash, kind, title, year, name, added_by, added_by_name, state, reason, paused)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		dl.Hash, dl.Kind, dl.Title, nullYear(dl.Year), dl.Name, dl.AddedBy, dl.AddedByName, dl.State, dl.Reason, dl.Paused)
	return err
}

// GetDownload is one download; ErrNotFound if there's none.
func (d *DB) GetDownload(ctx context.Context, hash string) (Download, error) {
	dl, err := scanDownload(d.SQL.QueryRowContext(ctx, `SELECT `+downloadCols+` FROM downloads WHERE hash = $1`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return Download{}, ErrNotFound
	}
	return dl, err
}

func (d *DB) listDownloads(ctx context.Context, where string, args ...any) ([]Download, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT `+downloadCols+` FROM downloads `+where+` ORDER BY added_at DESC, hash`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Download
	for rows.Next() {
		dl, err := scanDownload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// ListDownloads is every download, newest first.
func (d *DB) ListDownloads(ctx context.Context) ([]Download, error) {
	return d.listDownloads(ctx, "")
}

// ListDownloadsByState is the downloads in one state, newest first.
func (d *DB) ListDownloadsByState(ctx context.Context, state string) ([]Download, error) {
	return d.listDownloads(ctx, "WHERE state = $1", state)
}

// UpdateDownload saves kind, title, year, name, state, reason, paused and
// in_order.
func (d *DB) UpdateDownload(ctx context.Context, dl Download) error {
	res, err := d.SQL.ExecContext(ctx, `UPDATE downloads SET kind = $2, title = $3, year = $4, name = $5, state = $6, reason = $7,
		paused = $8, in_order = $9 WHERE hash = $1`, dl.Hash, dl.Kind, dl.Title, nullYear(dl.Year), dl.Name, dl.State, dl.Reason, dl.Paused, dl.InOrder)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteDownload removes a download and its recorded links.
func (d *DB) DeleteDownload(ctx context.Context, hash string) error {
	_, err := d.SQL.ExecContext(ctx, `DELETE FROM downloads WHERE hash = $1`, hash)
	return err
}

// AddLinks records links made for a download (repeats are ignored).
func (d *DB) AddLinks(ctx context.Context, hash string, paths []string) error {
	for _, p := range paths {
		if _, err := d.SQL.ExecContext(ctx, `INSERT INTO download_links (hash, path) VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, hash, p); err != nil {
			return err
		}
	}
	return nil
}

// Links is every link recorded for a download.
func (d *DB) Links(ctx context.Context, hash string) ([]string, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT path FROM download_links WHERE hash = $1 ORDER BY path`, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteLinks forgets a download's links.
func (d *DB) DeleteLinks(ctx context.Context, hash string) error {
	_, err := d.SQL.ExecContext(ctx, `DELETE FROM download_links WHERE hash = $1`, hash)
	return err
}
