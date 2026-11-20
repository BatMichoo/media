package db

import (
	"context"
	"database/sql"
	"errors"
	"github.com/BatMichoo/media/internal/model"
	"time"

	"github.com/lib/pq"
)

// ErrNotFound means there's no such row.
var ErrNotFound = errors.New("not found")

// Download is a row of downloads: a torrent added through the app.
type Download struct {
	Hash                 string
	Kind                 model.Kind
	Title                string
	Year                 int // 0 = none
	Name                 string
	AddedBy, AddedByName string
	AddedAt              time.Time
	State                model.State
	Reason               string // text from elsewhere (Transmission, the file system), or ""
	ReasonKey            string // or a message key the app wrote, translated when shown
	ReasonN              int    // the count filled into ReasonKey's message, 0 = none
	Paused               bool   // paused in the app
	InOrder              bool   // downloads its pieces in order (new rows: true)
}

const downloadCols = `hash, kind, title, COALESCE(year, 0), name, added_by, added_by_name, added_at, state, reason, reason_key, reason_n, paused, in_order`

func scanDownload(row interface{ Scan(...any) error }) (Download, error) {
	var d Download
	err := row.Scan(&d.Hash, &d.Kind, &d.Title, &d.Year, &d.Name, &d.AddedBy, &d.AddedByName, &d.AddedAt, &d.State, &d.Reason, &d.ReasonKey, &d.ReasonN, &d.Paused, &d.InOrder)
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
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO downloads (hash, kind, title, year, name, added_by, added_by_name, state, reason, reason_key, reason_n, paused, in_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		dl.Hash, dl.Kind, dl.Title, nullYear(dl.Year), dl.Name, dl.AddedBy, dl.AddedByName, dl.State, dl.Reason, dl.ReasonKey, dl.ReasonN, dl.Paused, dl.InOrder)
	return err
}

// GetDownload is one download; ErrNotFound if there's none.
func (d *DB) GetDownload(ctx context.Context, hash string) (Download, error) {
	dl, err := scanDownload(d.q(ctx).QueryRowContext(ctx, `SELECT `+downloadCols+` FROM downloads WHERE hash = $1`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return Download{}, ErrNotFound
	}
	return dl, err
}

func (d *DB) listDownloads(ctx context.Context, where string, args ...any) ([]Download, error) {
	rows, err := d.q(ctx).QueryContext(ctx, `SELECT `+downloadCols+` FROM downloads `+where+` ORDER BY added_at DESC, hash`, args...)
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

// UpdateDownload saves kind, title, year, name, state, the reason, paused
// and in_order.
func (d *DB) UpdateDownload(ctx context.Context, dl Download) error {
	res, err := d.q(ctx).ExecContext(ctx, `UPDATE downloads SET kind = $2, title = $3, year = $4, name = $5, state = $6, reason = $7,
		reason_key = $8, reason_n = $9, paused = $10, in_order = $11 WHERE hash = $1`,
		dl.Hash, dl.Kind, dl.Title, nullYear(dl.Year), dl.Name, dl.State, dl.Reason, dl.ReasonKey, dl.ReasonN, dl.Paused, dl.InOrder)
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
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM downloads WHERE hash = $1`, hash)
	return err
}

// AddLinks records links made for a download, all or none (repeats are
// ignored).
func (d *DB) AddLinks(ctx context.Context, hash string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO download_links (hash, path)
		SELECT $1, p FROM unnest($2::text[]) AS p ON CONFLICT DO NOTHING`, hash, pq.Array(paths))
	return err
}

// Links is every link recorded for a download.
func (d *DB) Links(ctx context.Context, hash string) ([]string, error) {
	rows, err := d.q(ctx).QueryContext(ctx, `SELECT path FROM download_links WHERE hash = $1 ORDER BY path`, hash)
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
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM download_links WHERE hash = $1`, hash)
	return err
}
