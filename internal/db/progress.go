package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"
)

// Progress is where a person is in one item.
type Progress struct {
	ItemID, SeriesID string
	Part             int     // 0-based part of a multi-part movie
	Position         float64 // seconds within that part
	Duration         float64 // seconds, the whole item
	Watched          float64 // seconds watched: earlier parts plus Position
	Finished         bool
	UpdatedAt        time.Time
}

const progressCols = `item_id, COALESCE(series_id, ''), part, position_seconds, duration_seconds, watched_seconds, finished, updated_at`

func scanProgress(row interface{ Scan(...any) error }) (Progress, error) {
	var p Progress
	err := row.Scan(&p.ItemID, &p.SeriesID, &p.Part, &p.Position, &p.Duration, &p.Watched, &p.Finished, &p.UpdatedAt)
	return p, err
}

// SaveProgress stores a person's progress in an item.
func (d *DB) SaveProgress(ctx context.Context, userID string, p Progress) error {
	var series any
	if p.SeriesID != "" {
		series = p.SeriesID
	}
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO progress
		(user_id, item_id, series_id, part, position_seconds, duration_seconds, watched_seconds, finished, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (user_id, item_id) DO UPDATE SET series_id = EXCLUDED.series_id, part = EXCLUDED.part,
			position_seconds = EXCLUDED.position_seconds, duration_seconds = EXCLUDED.duration_seconds,
			watched_seconds = EXCLUDED.watched_seconds,
			finished = EXCLUDED.finished, updated_at = EXCLUDED.updated_at`,
		userID, p.ItemID, series, p.Part, p.Position, p.Duration, p.Watched, p.Finished, p.UpdatedAt)
	return err
}

// GetProgress is a person's progress in an item; ok is false if none.
func (d *DB) GetProgress(ctx context.Context, userID, itemID string) (Progress, bool, error) {
	p, err := scanProgress(d.q(ctx).QueryRowContext(ctx,
		`SELECT `+progressCols+` FROM progress WHERE user_id = $1 AND item_id = $2`, userID, itemID))
	if errors.Is(err, sql.ErrNoRows) {
		return Progress{}, false, nil
	}
	return p, err == nil, err
}

func (d *DB) listProgress(ctx context.Context, q string, args ...any) ([]Progress, error) {
	rows, err := d.q(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Progress
	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecentProgress is a person's latest progress rows, newest first.
func (d *DB) RecentProgress(ctx context.Context, userID string, limit int) ([]Progress, error) {
	return d.listProgress(ctx, `SELECT `+progressCols+` FROM progress WHERE user_id = $1
		ORDER BY updated_at DESC, item_id LIMIT $2`, userID, limit)
}

// ProgressFor is a person's progress in these items (only those with any).
func (d *DB) ProgressFor(ctx context.Context, userID string, itemIDs []string) ([]Progress, error) {
	return d.listProgress(ctx, `SELECT `+progressCols+` FROM progress WHERE user_id = $1 AND item_id = ANY($2)`,
		userID, pq.Array(itemIDs))
}

// Prefs is a person's chosen subtitle and audio languages, and subtitle
// size in percent (0 when nothing is saved).
type Prefs struct {
	SubtitleLang, AudioLang string
	SubtitleSize            int
}

// GetPrefs is a person's preferences (empty if none saved).
func (d *DB) GetPrefs(ctx context.Context, userID string) (Prefs, error) {
	var p Prefs
	err := d.q(ctx).QueryRowContext(ctx, `SELECT subtitle_lang, audio_lang, subtitle_size FROM preferences WHERE user_id = $1`, userID).
		Scan(&p.SubtitleLang, &p.AudioLang, &p.SubtitleSize)
	if errors.Is(err, sql.ErrNoRows) {
		return Prefs{}, nil
	}
	return p, err
}

// SetPrefs saves the preferences that aren't nil, keeping the others.
func (d *DB) SetPrefs(ctx context.Context, userID string, subtitleLang, audioLang *string, subtitleSize *int) error {
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO preferences (user_id, subtitle_lang, audio_lang, subtitle_size)
		VALUES ($1, COALESCE($2, ''), COALESCE($3, ''), COALESCE($4, 100))
		ON CONFLICT (user_id) DO UPDATE SET
			subtitle_lang = COALESCE($2, preferences.subtitle_lang),
			audio_lang = COALESCE($3, preferences.audio_lang),
			subtitle_size = COALESCE($4, preferences.subtitle_size)`, userID, subtitleLang, audioLang, subtitleSize)
	return err
}
