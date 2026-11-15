// Package watching keeps each person's place in every video, works out
// Continue watching (with the next episode of a series), and remembers
// subtitle and audio languages.
package watching

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BatMichoo/media/internal/db"
	"github.com/BatMichoo/media/internal/jellyfin"
)

// Progress is where a person is in one item.
type Progress = db.Progress

// Prefs is a person's subtitle and audio language ("" none yet; for
// subtitles "off" means off).
type Prefs = db.Prefs

// FinishedAt is the share of an item after which it counts as watched.
const FinishedAt = 0.9

// Catalog is the library (Jellyfin).
type Catalog interface {
	Item(ctx context.Context, id string) (jellyfin.Item, error)
	Episodes(ctx context.Context, seriesID string) ([]jellyfin.Item, error)
}

// Store is where progress and preferences are kept.
type Store interface {
	SaveProgress(ctx context.Context, userID string, p db.Progress) error
	GetProgress(ctx context.Context, userID, itemID string) (db.Progress, bool, error)
	RecentProgress(ctx context.Context, userID string, limit int) ([]db.Progress, error)
	ProgressFor(ctx context.Context, userID string, itemIDs []string) ([]db.Progress, error)
	GetPrefs(ctx context.Context, userID string) (db.Prefs, error)
	SetPrefs(ctx context.Context, userID string, subtitleLang, audioLang *string) error
}

// Service is the watching logic.
type Service struct {
	store Store
	cat   Catalog
	Now   func() time.Time
}

// New makes the service.
func New(store Store, cat Catalog) *Service {
	return &Service{store: store, cat: cat, Now: time.Now}
}

// SaveInput is one progress report from the player.
type SaveInput struct {
	ItemID, SeriesID string
	Part             int
	Position         float64 // seconds within the part
	PartDurations    []float64
}

func sum(xs []float64) float64 {
	t := 0.0
	for _, x := range xs {
		t += x
	}
	return t
}

// Save stores a person's place. Finished is worked out again every time,
// so watching again from the start un-finishes it.
func (s *Service) Save(ctx context.Context, userID string, in SaveInput) (Progress, error) {
	if len(in.PartDurations) == 0 || in.Part < 0 || in.Part >= len(in.PartDurations) {
		return Progress{}, fmt.Errorf("part %d of %d", in.Part, len(in.PartDurations))
	}
	if in.Position < 0 {
		return Progress{}, errors.New("negative position")
	}
	total := sum(in.PartDurations)
	watched := sum(in.PartDurations[:in.Part]) + in.Position
	p := Progress{
		ItemID: in.ItemID, SeriesID: in.SeriesID, Part: in.Part, Position: in.Position, Duration: total, Watched: watched,
		Finished: total > 0 && watched >= FinishedAt*total, UpdatedAt: s.Now(),
	}
	return p, s.store.SaveProgress(ctx, userID, p)
}

// Get is a person's place in an item; ok is false if none.
func (s *Service) Get(ctx context.Context, userID, itemID string) (Progress, bool, error) {
	return s.store.GetProgress(ctx, userID, itemID)
}

// Many is a person's places in these items, by item id.
func (s *Service) Many(ctx context.Context, userID string, itemIDs []string) (map[string]Progress, error) {
	rows, err := s.store.ProgressFor(ctx, userID, itemIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Progress, len(rows))
	for _, p := range rows {
		out[p.ItemID] = p
	}
	return out, nil
}

// Fraction is how much of the item is watched, 0..1, for progress bars.
func Fraction(p Progress) float64 {
	if p.Duration <= 0 {
		return 0
	}
	return min(max(p.Watched/p.Duration, 0), 1)
}

// Entry is one card in Continue watching. Progress is nil for a next
// episode not started yet.
type Entry struct {
	Item     jellyfin.Item
	Progress *Progress
}

// Continue is a person's Continue watching row, newest first: unfinished
// movies and episodes, and after a finished episode the next one, one
// card per series. Items Jellyfin no longer has are skipped.
func (s *Service) Continue(ctx context.Context, userID string, limit int) ([]Entry, error) {
	rows, err := s.store.RecentProgress(ctx, userID, 50)
	if err != nil {
		return nil, err
	}
	var out []Entry
	seenSeries := map[string]bool{}
	for _, r := range rows {
		if len(out) >= limit {
			break
		}
		r := r
		if r.SeriesID != "" {
			if seenSeries[r.SeriesID] {
				continue
			}
			seenSeries[r.SeriesID] = true
		}
		var e *Entry
		switch {
		case !r.Finished:
			it, err := s.cat.Item(ctx, r.ItemID)
			if errors.Is(err, jellyfin.ErrNotFound) {
				continue
			} else if err != nil {
				return nil, err
			}
			e = &Entry{Item: it, Progress: &r}
		case r.SeriesID != "":
			if e, err = s.next(ctx, userID, r); err != nil {
				return nil, err
			}
		}
		if e != nil {
			out = append(out, *e)
		}
	}
	return out, nil
}

// next is the episode after a finished one, unless it's finished too or
// there's none.
func (s *Service) next(ctx context.Context, userID string, r Progress) (*Entry, error) {
	eps, err := s.cat.Episodes(ctx, r.SeriesID)
	if errors.Is(err, jellyfin.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	for i, ep := range eps {
		if ep.ID != r.ItemID || i+1 >= len(eps) {
			continue
		}
		n := eps[i+1]
		p, ok, err := s.store.GetProgress(ctx, userID, n.ID)
		if err != nil {
			return nil, err
		}
		switch {
		case !ok:
			return &Entry{Item: n}, nil
		case !p.Finished:
			return &Entry{Item: n, Progress: &p}, nil
		}
		return nil, nil
	}
	return nil, nil
}

// Prefs is a person's languages.
func (s *Service) Prefs(ctx context.Context, userID string) (Prefs, error) {
	return s.store.GetPrefs(ctx, userID)
}

// SetPrefs saves the languages that aren't nil.
func (s *Service) SetPrefs(ctx context.Context, userID string, subtitleLang, audioLang *string) error {
	return s.store.SetPrefs(ctx, userID, subtitleLang, audioLang)
}

// PickTracks chooses tracks by a person's languages: a subtitle track in
// their language (none when "off" or not set), and an audio track in
// theirs (nil means Jellyfin's default).
func PickTracks(p Prefs, subs, audio []jellyfin.Track) (sub *int, aud *int) {
	if p.SubtitleLang != "" && p.SubtitleLang != "off" {
		for _, t := range subs {
			if t.Lang == p.SubtitleLang {
				i := t.Index
				sub = &i
				break
			}
		}
	}
	if p.AudioLang != "" {
		for _, t := range audio {
			if t.Lang == p.AudioLang {
				i := t.Index
				aud = &i
				break
			}
		}
	}
	return sub, aud
}
