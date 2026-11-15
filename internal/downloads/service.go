// Package downloads adds torrents to Transmission, files them into the
// library when they finish, stops seeding after a while, and removes or
// re-files them on request.
package downloads

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BatMichoo/media/internal/db"
	"github.com/BatMichoo/media/internal/library"
	"github.com/BatMichoo/media/internal/model"
	"github.com/BatMichoo/media/internal/transmission"
)

// ErrInvalid means the input was refused; the error is an *InvalidError.
var ErrInvalid = errors.New("invalid input")

// ErrNotFound means there's no such download.
var ErrNotFound = errors.New("no such download")

// InvalidError says what was wrong, as a message key the pages translate.
type InvalidError struct{ Key string }

func (e *InvalidError) Error() string        { return "invalid input: " + e.Key }
func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(key string) error { return &InvalidError{Key: key} }

// Torrents is Transmission.
type Torrents interface {
	Add(ctx context.Context, magnet string, torrentFile []byte, downloadDir string) (hash, name string, duplicate bool, err error)
	Get(ctx context.Context, hashes []string) (map[string]transmission.Torrent, error)
	Remove(ctx context.Context, hash string, deleteData bool) error
	Stop(ctx context.Context, hash string) error
	Place(ctx context.Context, hash, dir string) error
}

// Library is Jellyfin's library.
type Library interface {
	Refresh(ctx context.Context) error
}

// Store is where downloads and their links are recorded.
type Store interface {
	InsertDownload(ctx context.Context, d db.Download) error
	GetDownload(ctx context.Context, hash string) (db.Download, error)
	ListDownloads(ctx context.Context) ([]db.Download, error)
	ListDownloadsByState(ctx context.Context, state string) ([]db.Download, error)
	UpdateDownload(ctx context.Context, d db.Download) error
	DeleteDownload(ctx context.Context, hash string) error
	AddLinks(ctx context.Context, hash string, paths []string) error
	Links(ctx context.Context, hash string) ([]string, error)
	DeleteLinks(ctx context.Context, hash string) error
}

// Download is a download as the pages show it.
type Download struct {
	Hash, Kind, Title string
	Year              int
	Name, AddedByName string
	AddedAt           time.Time
	State, Reason     string
	Percent           float64       // 0..1
	RateDownload      int64         // bytes per second
	Size, Done        int64         // bytes in all and so far (Size 0 = not known yet)
	ETA               time.Duration // time left; 0 = not known
	SeedingUntil      time.Time
	SeedingDone       bool
}

// AddInput is what the Add page sends: a magnet link or a torrent file.
type AddInput struct {
	Magnet      string
	File        []byte
	Kind, Title string
	Year        int // 0 = none (series only)
}

// Service is the downloads logic. Poll, Add, Edit and Remove take turns,
// so a poll round filing a download never undoes an edit made meanwhile.
type Service struct {
	mu      sync.Mutex
	store   Store
	t       Torrents
	lib     Library
	dataDir string

	SeedFor time.Duration    // how long a finished torrent seeds
	Now     func() time.Time // the clock
	Log     *log.Logger
}

// New makes the service; seeding lasts 72 hours.
func New(store Store, t Torrents, lib Library, dataDir string) *Service {
	return &Service{store: store, t: t, lib: lib, dataDir: dataDir,
		SeedFor: 72 * time.Hour, Now: time.Now, Log: log.Default()}
}

const maxTitleBytes = 150

// validate checks kind, title and year, and returns the cleaned title.
func validate(kind, title string, year int) (string, error) {
	if kind != "movie" && kind != "series" {
		return "", invalid("error.kind")
	}
	clean := library.CleanTitle(title)
	if clean == "" {
		return "", invalid("error.title")
	}
	// Folder and file names are at most 255 bytes, and a name gets the
	// year, " - partN" and an extension added.
	if len(clean) > maxTitleBytes {
		return "", invalid("error.title_long")
	}
	if kind == "movie" && year == 0 {
		return "", invalid("error.year_needed")
	}
	if year != 0 && (year < 1888 || year > 2100) {
		return "", invalid("error.year")
	}
	return clean, nil
}

// Add sends a torrent to Transmission and records it. existing is true
// when the app already had this torrent; then nothing changes.
func (s *Service) Add(ctx context.Context, user model.User, in AddInput) (Download, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	magnet := strings.TrimSpace(in.Magnet)
	if (magnet == "") == (len(in.File) == 0) {
		return Download{}, false, invalid("error.source")
	}
	if magnet != "" && !strings.HasPrefix(magnet, "magnet:?") {
		return Download{}, false, invalid("error.magnet")
	}
	title, err := validate(in.Kind, in.Title, in.Year)
	if err != nil {
		return Download{}, false, err
	}
	hash, name, duplicate, err := s.t.Add(ctx, magnet, in.File, filepath.Join(s.dataDir, "downloads"))
	if err != nil {
		return Download{}, false, err
	}
	// Each download gets its own folder, so two torrents of the same
	// release never share files (and removing one never takes the
	// other's). One Transmission already had stays where it is.
	if !duplicate {
		if err := s.t.Place(ctx, hash, filepath.Join(s.dataDir, "downloads", hash)); err != nil {
			return Download{}, false, err
		}
	}
	if have, err := s.store.GetDownload(ctx, hash); err == nil {
		return fromRow(have), true, nil
	} else if !errors.Is(err, db.ErrNotFound) {
		return Download{}, false, err
	}
	row := db.Download{Hash: hash, Kind: in.Kind, Title: title, Year: in.Year, Name: name,
		AddedBy: user.ID, AddedByName: user.Name, State: "downloading"}
	if err := s.store.InsertDownload(ctx, row); err != nil {
		return Download{}, false, err
	}
	row.AddedAt = s.Now()
	return fromRow(row), false, nil
}

func fromRow(r db.Download) Download {
	return Download{Hash: r.Hash, Kind: r.Kind, Title: r.Title, Year: r.Year, Name: r.Name,
		AddedByName: r.AddedByName, AddedAt: r.AddedAt, State: r.State, Reason: r.Reason}
}

// List is every download, newest first, with live progress from
// Transmission. When Transmission can't be reached it still lists them
// (without progress) and returns that error too.
func (s *Service) List(ctx context.Context) ([]Download, error) {
	rows, err := s.store.ListDownloads(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Download, 0, len(rows))
	hashes := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
		hashes = append(hashes, r.Hash)
	}
	live, err := s.t.Get(ctx, hashes)
	if err != nil {
		return out, fmt.Errorf("transmission: %w", err)
	}
	for i := range out {
		t, ok := live[out[i].Hash]
		if !ok {
			continue
		}
		out[i].Percent = t.PercentDone
		out[i].RateDownload = t.RateDownload
		out[i].Size, out[i].Done, out[i].ETA = t.Size, t.Size-t.Left, t.ETA
		if !t.DoneDate.IsZero() {
			out[i].SeedingUntil = t.DoneDate.Add(s.SeedFor)
			out[i].SeedingDone = t.Stopped
		}
	}
	return out, nil
}

// Run polls every interval until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.Poll(ctx); err != nil && ctx.Err() == nil {
			s.Log.Printf("downloads: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Poll is one round: file what finished, mark what vanished, and stop
// seeding what has seeded long enough.
func (s *Service) Poll(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.store.ListDownloads(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(rows))
	for _, r := range rows {
		hashes = append(hashes, r.Hash)
	}
	live, err := s.t.Get(ctx, hashes)
	if err != nil {
		return fmt.Errorf("transmission: %w", err)
	}
	var errs []error
	for _, r := range rows {
		t, ok := live[r.Hash]
		if r.State == "downloading" {
			if err := s.check(ctx, r, t, ok); err != nil {
				errs = append(errs, err)
			}
		}
		if ok && !t.DoneDate.IsZero() && !t.Stopped && s.Now().Sub(t.DoneDate) > s.SeedFor {
			if err := s.t.Stop(ctx, r.Hash); err != nil {
				errs = append(errs, fmt.Errorf("stop seeding %s: %w", r.Title, err))
			}
		}
	}
	return errors.Join(errs...)
}

// check moves one downloading row on: failed if Transmission lost it,
// the error shown if Transmission reports one, filed when finished.
func (s *Service) check(ctx context.Context, r db.Download, t transmission.Torrent, known bool) error {
	switch {
	case !known:
		r.State, r.Reason = "failed", "removed from Transmission"
		return s.store.UpdateDownload(ctx, r)
	case t.LocalError || t.PercentDone < 1:
		// A tracker warning shows but doesn't stop a finished download
		// from being filed; a problem with the data does.
		if r.Reason == t.Error {
			return nil
		}
		r.Reason = t.Error
		return s.store.UpdateDownload(ctx, r)
	}
	return s.file(ctx, r, t)
}

// file links a finished torrent into the library and records the links.
func (s *Service) file(ctx context.Context, r db.Download, t transmission.Torrent) error {
	links, err := library.Plan(r.Kind, r.Title, r.Year, r.Hash, t.Files)
	if err != nil {
		r.State, r.Reason = "failed", err.Error()
		return s.store.UpdateDownload(ctx, r)
	}
	made, linkErr := library.Apply(s.dataDir, links)
	if err := s.store.AddLinks(ctx, r.Hash, made); err != nil {
		return err
	}
	switch {
	case linkErr != nil:
		r.State, r.Reason = "failed", linkErr.Error()
	case len(made) == 0:
		// Every file was there already (another copy of the same thing).
		r.State, r.Reason = "filed", "already in the library"
	default:
		r.State, r.Reason = "filed", ""
	}
	if err := s.store.UpdateDownload(ctx, r); err != nil {
		return err
	}
	if len(made) > 0 {
		s.refresh(ctx)
	}
	return nil
}

// refresh asks Jellyfin to rescan. If it's down, Jellyfin finds the files
// on its next scan anyway, so this only logs.
func (s *Service) refresh(ctx context.Context) {
	if err := s.lib.Refresh(ctx); err != nil {
		s.Log.Printf("downloads: library rescan: %v", err)
	}
}

func (s *Service) row(ctx context.Context, hash string) (db.Download, error) {
	r, err := s.store.GetDownload(ctx, hash)
	if errors.Is(err, db.ErrNotFound) {
		return r, ErrNotFound
	}
	return r, err
}

// Remove stops and forgets a torrent. With deleteFiles, its downloaded
// data and the links the app made in the library go too.
func (s *Service) Remove(ctx context.Context, hash string, deleteFiles bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.row(ctx, hash)
	if err != nil {
		return err
	}
	if err := s.t.Remove(ctx, hash, deleteFiles); err != nil {
		return err
	}
	if deleteFiles {
		links, err := s.store.Links(ctx, hash)
		if err != nil {
			return err
		}
		if err := library.Undo(s.dataDir, links); err != nil {
			return err
		}
	}
	if err := s.store.DeleteDownload(ctx, r.Hash); err != nil {
		return err
	}
	s.refresh(ctx)
	return nil
}

// Edit corrects kind, title and year. A filed (or failed) download is
// filed again under the new details (nothing is downloaded again).
func (s *Service) Edit(ctx context.Context, hash, kind, title string, year int) error {
	clean, err := validate(kind, title, year)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.row(ctx, hash)
	if err != nil {
		return err
	}
	// Filed ones are filed again under the new details; failed ones are
	// tried again (e.g. after the media folder was fixed).
	refile := r.State == "filed" || r.State == "failed"
	if refile {
		links, err := s.store.Links(ctx, hash)
		if err != nil {
			return err
		}
		if err := library.Undo(s.dataDir, links); err != nil {
			return err
		}
		if err := s.store.DeleteLinks(ctx, hash); err != nil {
			return err
		}
		r.State, r.Reason = "downloading", ""
	}
	r.Kind, r.Title, r.Year = kind, clean, year
	if err := s.store.UpdateDownload(ctx, r); err != nil {
		return err
	}
	if !refile {
		return nil
	}
	live, err := s.t.Get(ctx, []string{hash})
	if err != nil {
		return fmt.Errorf("transmission: %w", err)
	}
	t, ok := live[hash]
	return s.check(ctx, r, t, ok)
}
