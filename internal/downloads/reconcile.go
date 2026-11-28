package downloads

// The background rounds: picking files, filing finished downloads,
// stopping seeding, and the reasons written along the way.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BatMichoo/media/internal/db"
	"github.com/BatMichoo/media/internal/library"
	"github.com/BatMichoo/media/internal/model"
	"github.com/BatMichoo/media/internal/transmission"
)

// The reasons the app gives itself, as message keys (Download.ReasonKey).
const (
	ReasonRemoved      = "reason.removed"            // Transmission no longer has it
	ReasonInLibrary    = "reason.already_in_library" // every file was there already
	ReasonNoVideo      = "reason.no_video"           // nothing to file
	ReasonSubsUnplaced = "reason.subs_unplaced"      // ReasonN subtitles matched no video
)

// setText sets r's reason to text from elsewhere ("" for none).
func setText(r *db.Download, text string) { r.Reason, r.ReasonKey, r.ReasonN = text, "", 0 }

// setKey sets r's reason to one of the app's own, with its count.
func setKey(r *db.Download, key string, n int) { r.Reason, r.ReasonKey, r.ReasonN = "", key, n }

// selectFiles marks t's wanted files (library.Wanted) and unmarks the
// rest, changing only what differs.
func (s *Service) selectFiles(ctx context.Context, t transmission.Torrent) error {
	var want, skip []int
	for i, f := range t.Files {
		switch w := library.Wanted(libraryFile(f)); {
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

// seedEnd is when a finished torrent stops seeding: the end chosen when
// seeding was started by hand, else SeedFor after it finished. noLimit is
// true when it was started by hand with no end.
func (s *Service) seedEnd(r db.Download, t transmission.Torrent) (end time.Time, noLimit bool) {
	switch {
	case r.SeedNoLimit:
		return time.Time{}, true
	case !r.SeedUntil.IsZero():
		return r.SeedUntil, false
	}
	return t.DoneDate.Add(s.SeedFor), false
}

// seedingOver is true once a finished torrent has seeded long enough.
func (s *Service) seedingOver(r db.Download, t transmission.Torrent) bool {
	if t.DoneDate.IsZero() {
		return false
	}
	end, noLimit := s.seedEnd(r, t)
	return !noLimit && s.Now().After(end)
}

// roundTimeout bounds one background round. A round isn't cut short
// when ctx ends (shutdown): filing makes links on disk and then records
// them, and stopping between the two would leave links nobody can undo.
const roundTimeout = 20 * time.Second

// Run polls every interval until ctx ends, and checks new magnet links
// for their file list every couple of seconds. A round under way when
// ctx ends is finished first.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	fast := time.NewTicker(2 * time.Second)
	defer fast.Stop()
	round := func(f func(context.Context) error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), roundTimeout)
		defer cancel()
		if err := f(rctx); err != nil {
			s.Log.Printf("downloads: %v", err)
		}
	}
	for {
		round(s.Poll)
		for waiting := true; waiting; {
			select {
			case <-ctx.Done():
				return
			case <-fast.C:
				round(s.CheckPending)
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
	noPeers, err := s.poll(ctx)
	if noPeers {
		// Outside mu: the test can take a while (Transmission asks an
		// outside site), and nothing else should wait for it.
		s.testPort(ctx)
	}
	return err
}

// poll is Poll's round, under mu. noPeers is true when some download is
// stuck without peers, so the port test is worth running.
func (s *Service) poll(ctx context.Context) (noPeers bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.store.ListDownloads(ctx)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	hashes := make([]string, 0, len(rows))
	for _, r := range rows {
		hashes = append(hashes, r.Hash)
	}
	live, err := s.t.Get(ctx, hashes)
	if err != nil {
		return false, fmt.Errorf("transmission: %w", err)
	}
	var errs []error
	for _, r := range rows {
		t, ok := live[r.Hash]
		if r.State == model.StateDownloading {
			if err := s.check(ctx, r, t, ok); err != nil {
				errs = append(errs, err)
			}
		}
		if ok && r.State == model.StateDownloading && len(t.Files) > 0 && t.PercentDone < 1 {
			if err := s.selectFiles(ctx, t); err != nil {
				errs = append(errs, fmt.Errorf("pick files %s: %w", r.Title, err))
			}
			delete(s.pending, r.Hash)
		}
		if ok && s.stuck(r.Hash, stuckReason(r.State, r.Paused, t)) == StuckNoPeers {
			noPeers = true
		}
		// Transmission follows the saved order, either way (a change it
		// missed, or one it lost on restart, is put right here).
		if ok && t.PercentDone < 1 && t.Sequential != r.InOrder {
			if err := s.t.SetSequential(ctx, r.Hash, r.InOrder); err != nil {
				errs = append(errs, fmt.Errorf("sequential %s: %w", r.Title, err))
			}
		}
		if ok && !t.Stopped && s.seedingOver(r, t) {
			if err := s.t.Stop(ctx, r.Hash); err != nil {
				errs = append(errs, fmt.Errorf("stop seeding %s: %w", r.Title, err))
			}
		}
	}
	return noPeers, errors.Join(errs...)
}

// check moves one downloading row on: failed if Transmission lost it,
// the error shown if Transmission reports one, filed when finished.
func (s *Service) check(ctx context.Context, r db.Download, t transmission.Torrent, known bool) error {
	switch {
	case !known:
		r.State = model.StateFailed
		setKey(&r, ReasonRemoved, 0)
		return s.store.UpdateDownload(ctx, r)
	case t.LocalError || t.PercentDone < 1:
		// A tracker warning shows but doesn't stop a finished download
		// from being filed; a problem with the data does.
		if r.Reason == t.Error && r.ReasonKey == "" {
			return nil
		}
		setText(&r, t.Error)
		return s.store.UpdateDownload(ctx, r)
	}
	return s.file(ctx, r, t)
}

// file links a finished torrent into the library and records the links.
func (s *Service) file(ctx context.Context, r db.Download, t transmission.Torrent) error {
	links, err := library.Plan(r.Kind, r.Title, r.Year, r.Hash, libraryFiles(t.Files))
	if err != nil {
		r.State = model.StateFailed
		if errors.Is(err, library.ErrNoVideo) {
			setKey(&r, ReasonNoVideo, 0)
		} else {
			setText(&r, err.Error())
		}
		return s.store.UpdateDownload(ctx, r)
	}
	subs, err := s.store.Subtitles(ctx, r.Hash)
	if err != nil {
		return err
	}
	links, unplaced := library.PlaceSubtitles(r.Kind, r.Hash, links, librarySubs(subs))
	made, linkErr := library.Apply(s.dataDir, links)
	switch {
	case linkErr != nil:
		r.State = model.StateFailed
		setText(&r, linkErr.Error())
	case len(made) == 0:
		// Every file was there already (another copy of the same thing).
		r.State = model.StateFiled
		setKey(&r, ReasonInLibrary, 0)
	default:
		r.State = model.StateFiled
		setText(&r, "")
	}
	if linkErr == nil && unplaced > 0 {
		setKey(&r, ReasonSubsUnplaced, unplaced)
	}
	// The links and the state are saved together: links recorded for a
	// download still shown as downloading would be filed again.
	if err := s.store.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.AddLinks(ctx, r.Hash, made); err != nil {
			return err
		}
		return s.store.UpdateDownload(ctx, r)
	}); err != nil {
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

func libraryFile(f transmission.File) library.File {
	return library.File{Name: f.Name, Length: f.Length}
}

func libraryFiles(fs []transmission.File) []library.File {
	out := make([]library.File, len(fs))
	for i, f := range fs {
		out[i] = libraryFile(f)
	}
	return out
}
