// Package downloads adds torrents to Transmission, files them into the
// library when they finish, stops seeding after a while, and removes or
// re-files them on request.
package downloads

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
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
type InvalidError struct {
	Key string
	Arg string // filled into the message (e.g. a file name), or ""
}

func (e *InvalidError) Error() string        { return "invalid input: " + e.Key }
func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(key string) error { return &InvalidError{Key: key} }

// Torrents is Transmission.
type Torrents interface {
	Add(ctx context.Context, magnet string, torrentFile []byte, downloadDir string) (hash, name string, duplicate bool, err error)
	Get(ctx context.Context, hashes []string) (map[string]transmission.Torrent, error)
	Remove(ctx context.Context, hash string, deleteData bool) error
	Stop(ctx context.Context, hash string) error
	Start(ctx context.Context, hash string) error
	SetSequential(ctx context.Context, hash string, on bool) error
	SetWanted(ctx context.Context, hash string, wanted, unwanted []int) error
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
	DeleteLink(ctx context.Context, hash, path string) error
	LinkOwner(ctx context.Context, path string) (string, error)
	AddSubtitle(ctx context.Context, s db.Subtitle) error
	Subtitles(ctx context.Context, hash string) ([]db.Subtitle, error)
	DeleteSubtitle(ctx context.Context, hash, id string) error
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
	Paused            bool // paused in the app, downloading or seeding
}

// CanPause is true while it downloads or seeds.
func (d Download) CanPause() bool {
	if d.Paused {
		return false
	}
	return d.State == "downloading" || (d.State == "filed" && !d.SeedingUntil.IsZero() && !d.SeedingDone)
}

// AddInput is what the Add page sends: a magnet link or a torrent file.
type AddInput struct {
	Magnet      string
	File        []byte
	Kind, Title string
	Year        int      // 0 = none (series only)
	Subtitles   []Upload // kept with the download, linked in when it's filed
}

// Service is the downloads logic. Poll, Add, Edit and Remove take turns,
// so a poll round filing a download never undoes an edit made meanwhile.
type Service struct {
	mu      sync.Mutex
	store   Store
	t       Torrents
	lib     Library
	dataDir string
	// Magnet links whose file list hasn't arrived yet: watched closely so
	// unwanted files are left out as soon as it does.
	pending map[string]bool

	SeedFor time.Duration    // how long a finished torrent seeds
	Now     func() time.Time // the clock
	Log     *log.Logger
}

// New makes the service; seeding lasts 72 hours.
func New(store Store, t Torrents, lib Library, dataDir string) *Service {
	return &Service{store: store, t: t, lib: lib, dataDir: dataDir, pending: map[string]bool{},
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
	if len(in.Subtitles) > MaxUploads {
		return Download{}, false, invalid("error.sub_count")
	}
	subs, err := prepareAll(in.Kind, in.Subtitles)
	if err != nil {
		return Download{}, false, err
	}
	hash, name, duplicate, err := s.t.Add(ctx, magnet, in.File, filepath.Join(s.dataDir, "downloads"))
	if err != nil {
		return Download{}, false, err
	}
	// Only videos, subtitles and .nfo files download. A .torrent file's
	// list is known now, before anything starts; a magnet link's arrives
	// once it runs, and CheckPending catches it then.
	if !duplicate {
		live, err := s.t.Get(ctx, []string{hash})
		if err != nil {
			return Download{}, false, err
		}
		if t := live[hash]; len(t.Files) > 0 {
			if err := s.selectFiles(ctx, t); err != nil {
				return Download{}, false, err
			}
		} else {
			s.pending[hash] = true
		}
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
		// Already here: the uploads belong to it as it is.
		if have.Kind != in.Kind {
			if subs, err = prepareAll(have.Kind, in.Subtitles); err != nil {
				return Download{}, false, err
			}
		}
		if err := s.keepAll(ctx, have, subs); err != nil {
			return Download{}, false, err
		}
		return fromRow(have), true, nil
	} else if !errors.Is(err, db.ErrNotFound) {
		return Download{}, false, err
	}
	row := db.Download{Hash: hash, Kind: in.Kind, Title: title, Year: in.Year, Name: name,
		AddedBy: user.ID, AddedByName: user.Name, State: "downloading"}
	if err := s.store.InsertDownload(ctx, row); err != nil {
		return Download{}, false, err
	}
	if err := s.keepAll(ctx, row, subs); err != nil {
		return Download{}, false, err
	}
	row.AddedAt = s.Now()
	return fromRow(row), false, nil
}

func fromRow(r db.Download) Download {
	return Download{Hash: r.Hash, Kind: r.Kind, Title: r.Title, Year: r.Year, Name: r.Name,
		AddedByName: r.AddedByName, AddedAt: r.AddedAt, State: r.State, Reason: r.Reason, Paused: r.Paused}
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
			// Paused past its seeding time is simply done.
			if s.seedingOver(t) {
				out[i].Paused = false
			}
			out[i].SeedingDone = t.Stopped && !out[i].Paused
		}
	}
	return out, nil
}

// selectFiles marks t's wanted files (library.Wanted) and unmarks the
// rest, changing only what differs.
func (s *Service) selectFiles(ctx context.Context, t transmission.Torrent) error {
	var want, skip []int
	for i, f := range t.Files {
		switch w := library.Wanted(f); {
		case w && !f.Wanted:
			want = append(want, i)
		case !w && f.Wanted:
			skip = append(skip, i)
		}
	}
	return s.t.SetWanted(ctx, t.Hash, want, skip)
}

// CheckPending picks the files of magnet links whose file list has
// arrived since they were added.
func (s *Service) CheckPending(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(s.pending))
	for h := range s.pending {
		hashes = append(hashes, h)
	}
	live, err := s.t.Get(ctx, hashes)
	if err != nil {
		return fmt.Errorf("transmission: %w", err)
	}
	var errs []error
	for _, h := range hashes {
		t, ok := live[h]
		switch {
		case !ok:
			delete(s.pending, h)
		case len(t.Files) > 0:
			if err := s.selectFiles(ctx, t); err != nil {
				errs = append(errs, fmt.Errorf("pick files %s: %w", h, err))
				continue
			}
			delete(s.pending, h)
		}
	}
	return errors.Join(errs...)
}

// seedingOver is true once a finished torrent has seeded long enough.
func (s *Service) seedingOver(t transmission.Torrent) bool {
	return !t.DoneDate.IsZero() && s.Now().Sub(t.DoneDate) > s.SeedFor
}

// Pause stops a download, or a finished one's seeding, until Resume.
func (s *Service) Pause(ctx context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.row(ctx, hash)
	if err != nil {
		return err
	}
	if err := s.t.Stop(ctx, hash); err != nil {
		return err
	}
	r.Paused = true
	return s.store.UpdateDownload(ctx, r)
}

// Resume starts a paused download again. One whose seeding time ended
// while paused stays stopped.
func (s *Service) Resume(ctx context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.row(ctx, hash)
	if err != nil {
		return err
	}
	live, err := s.t.Get(ctx, []string{hash})
	if err != nil {
		return fmt.Errorf("transmission: %w", err)
	}
	if t, ok := live[hash]; ok && !s.seedingOver(t) {
		if err := s.t.Start(ctx, hash); err != nil {
			return err
		}
	}
	r.Paused = false
	return s.store.UpdateDownload(ctx, r)
}

// Run polls every interval until ctx ends, and checks new magnet links
// for their file list every couple of seconds.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	fast := time.NewTicker(2 * time.Second)
	defer fast.Stop()
	for {
		if err := s.Poll(ctx); err != nil && ctx.Err() == nil {
			s.Log.Printf("downloads: %v", err)
		}
		for waiting := true; waiting; {
			select {
			case <-ctx.Done():
				return
			case <-fast.C:
				if err := s.CheckPending(ctx); err != nil && ctx.Err() == nil {
					s.Log.Printf("downloads: %v", err)
				}
			case <-t.C:
				waiting = false
			}
		}
	}
}

// Poll is one round: file what finished, mark what vanished, stop
// seeding what has seeded long enough, and make every unfinished torrent
// download its pieces in order (one added before, or as a duplicate).
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
		if ok && r.State == "downloading" && len(t.Files) > 0 && t.PercentDone < 1 {
			if err := s.selectFiles(ctx, t); err != nil {
				errs = append(errs, fmt.Errorf("pick files %s: %w", r.Title, err))
			}
			delete(s.pending, r.Hash)
		}
		if ok && t.PercentDone < 1 && !t.Sequential {
			if err := s.t.SetSequential(ctx, r.Hash, true); err != nil {
				errs = append(errs, fmt.Errorf("sequential %s: %w", r.Title, err))
			}
		}
		if ok && !t.Stopped && s.seedingOver(t) {
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
	subs, err := s.store.Subtitles(ctx, r.Hash)
	if err != nil {
		return err
	}
	links, unplaced := library.PlaceSubtitles(r.Kind, r.Hash, links, librarySubs(subs))
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
	if linkErr == nil && unplaced > 0 {
		r.Reason = unplacedText(unplaced)
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
	// The app's own copies of uploaded subtitles go either way; ones in
	// the library are hard links and keep their content.
	if err := os.RemoveAll(filepath.Join(s.dataDir, "subtitles", hash)); err != nil {
		return err
	}
	delete(s.pending, hash)
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
