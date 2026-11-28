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

// Torrents is Transmission.
type Torrents interface {
	Add(ctx context.Context, magnet string, torrentFile []byte, downloadDir string) (hash, name string, duplicate bool, err error)
	Get(ctx context.Context, hashes []string) (map[string]transmission.Torrent, error)
	Remove(ctx context.Context, hash string, deleteData bool) error
	Stop(ctx context.Context, hash string) error
	Start(ctx context.Context, hash string) error
	SetSequential(ctx context.Context, hash string, on bool) error
	StartNow(ctx context.Context, hash string) error
	PortOpen(ctx context.Context) (open bool, port int, err error)
	SetWanted(ctx context.Context, hash string, wanted, unwanted []int) error
	Place(ctx context.Context, hash, dir string) error
}

// Library is Jellyfin's library.
type Library interface {
	Refresh(ctx context.Context) error
}

// Store is where downloads and their links are recorded.
type Store interface {
	// InTx runs f in one transaction: the store calls made with the ctx
	// f gets are saved together or not at all.
	InTx(ctx context.Context, f func(ctx context.Context) error) error
	DownloadStore
	LinkStore
	SubtitleStore
}

// DownloadStore is the downloads' own rows.
type DownloadStore interface {
	InsertDownload(ctx context.Context, d db.Download) error
	GetDownload(ctx context.Context, hash string) (db.Download, error)
	ListDownloads(ctx context.Context) ([]db.Download, error)
	UpdateDownload(ctx context.Context, d db.Download) error
	DeleteDownload(ctx context.Context, hash string) error
}

// LinkStore is the library links each download made, so they can be
// undone exactly.
type LinkStore interface {
	AddLinks(ctx context.Context, hash string, paths []string) error
	Links(ctx context.Context, hash string) ([]string, error)
	DeleteLinks(ctx context.Context, hash string) error
	DeleteLink(ctx context.Context, hash, path string) error
	LinkOwner(ctx context.Context, path string) (string, error)
}

// SubtitleStore is the subtitles uploaded for downloads.
type SubtitleStore interface {
	AddSubtitle(ctx context.Context, s db.Subtitle) error
	Subtitles(ctx context.Context, hash string) ([]db.Subtitle, error)
	DeleteSubtitle(ctx context.Context, hash, id string) error
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

	PortEvery  time.Duration // how often the port test may run
	StallAfter time.Duration // how long a download must be stalled to show as stuck

	live liveState // stalls and the port test (health.go)
}

// New makes the service; seeding lasts 72 hours.
func New(store Store, t Torrents, lib Library, dataDir string) *Service {
	return &Service{store: store, t: t, lib: lib, dataDir: dataDir, pending: map[string]bool{},
		SeedFor: 72 * time.Hour, Now: time.Now, Log: log.Default(),
		PortEvery: 10 * time.Minute, StallAfter: time.Minute, live: liveState{stalledSince: map[string]time.Time{}}}
}

const maxTitleBytes = 150

// validate checks kind, title and year, and returns the cleaned title.
func validate(kind model.Kind, title string, year int) (string, error) {
	if !kind.Valid() {
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
	if kind == model.KindMovie && year == 0 {
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
		AddedBy: user.ID, AddedByName: user.Name, State: model.StateDownloading, InOrder: true}
	if err := s.store.InsertDownload(ctx, row); err != nil {
		return Download{}, false, err
	}
	if err := s.keepAll(ctx, row, subs); err != nil {
		return Download{}, false, err
	}
	row.AddedAt = s.Now()
	return fromRow(row), false, nil
}

// Get is one download as recorded, without Transmission's live progress;
// ErrNotFound if there's none.
func (s *Service) Get(ctx context.Context, hash string) (Download, error) {
	r, err := s.row(ctx, hash)
	if err != nil {
		return Download{}, err
	}
	return fromRow(r), nil
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
		out[i].Live = true
		out[i].RateUpload, out[i].Uploaded, out[i].Ratio = t.RateUpload, t.Uploaded, t.Ratio
		out[i].Peers, out[i].PeersSending, out[i].Seeders = t.Peers, t.PeersSending, t.Seeders
		out[i].QueuePos, out[i].TrackerError = queueRank(live, t), t.TrackerError
		out[i].Stuck = s.stuck(out[i].Hash, stuckReason(out[i].State, out[i].Paused, t))
		if out[i].Stuck == StuckNoPeers {
			out[i].PortClosed, out[i].Port = s.portClosed()
		}
		if !t.DoneDate.IsZero() {
			out[i].SeedingUntil, out[i].SeedingNoLimit = s.seedEnd(rows[i], t)
			// Paused past its seeding time is simply done.
			if s.seedingOver(rows[i], t) {
				out[i].Paused = false
			}
			out[i].SeedingDone = t.Stopped && !out[i].Paused
		}
	}
	return out, nil
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
	if t, ok := live[hash]; ok && !s.seedingOver(r, t) {
		if err := s.t.Start(ctx, hash); err != nil {
			return err
		}
	}
	r.Paused = false
	return s.store.UpdateDownload(ctx, r)
}

// Seed starts seeding a finished torrent again, for limit from now, or
// with no end when limit is 0. It replaces the seeding time it had.
func (s *Service) Seed(ctx context.Context, hash string, limit time.Duration) error {
	if limit < 0 {
		return ErrInvalid
	}
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
	if t, ok := live[hash]; !ok || t.DoneDate.IsZero() || t.PercentDone < 1 {
		return ErrInvalid
	}
	if err := s.t.Start(ctx, hash); err != nil {
		return err
	}
	r.SeedUntil, r.SeedNoLimit = time.Time{}, limit == 0
	if limit > 0 {
		r.SeedUntil = s.Now().Add(limit)
	}
	r.Paused = false
	return s.store.UpdateDownload(ctx, r)
}

// StartNow starts a queued download straight away.
func (s *Service) StartNow(ctx context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.row(ctx, hash); err != nil {
		return err
	}
	return s.t.StartNow(ctx, hash)
}

// AnyOrder lets a download fetch its pieces in any order, which helps when
// in-order downloading stalls; it can't be watched until it's finished.
func (s *Service) AnyOrder(ctx context.Context, hash string) error {
	return s.setOrder(ctx, hash, false)
}

// InOrder makes a download fetch its pieces in order again, so it can be
// watched while it downloads.
func (s *Service) InOrder(ctx context.Context, hash string) error {
	return s.setOrder(ctx, hash, true)
}

func (s *Service) setOrder(ctx context.Context, hash string, inOrder bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.row(ctx, hash)
	if err != nil {
		return err
	}
	// Saved first: the poll makes Transmission follow the saved choice, so
	// if Transmission fails now it is put right on the next round.
	r.InOrder = inOrder
	if err := s.store.UpdateDownload(ctx, r); err != nil {
		return err
	}
	return s.t.SetSequential(ctx, hash, inOrder)
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
func (s *Service) Edit(ctx context.Context, hash string, kind model.Kind, title string, year int) error {
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
	refile := r.State == model.StateFiled || r.State == model.StateFailed
	if refile {
		links, err := s.store.Links(ctx, hash)
		if err != nil {
			return err
		}
		if err := library.Undo(s.dataDir, links); err != nil {
			return err
		}
		r.State = model.StateDownloading
		setText(&r, "")
	}
	r.Kind, r.Title, r.Year = kind, clean, year
	// The links (undone above) are forgotten with the new details saved,
	// so a failure never leaves a filed download without its links.
	if err := s.store.InTx(ctx, func(ctx context.Context) error {
		if refile {
			if err := s.store.DeleteLinks(ctx, hash); err != nil {
				return err
			}
		}
		return s.store.UpdateDownload(ctx, r)
	}); err != nil {
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
