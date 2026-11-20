package downloads

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BatMichoo/media/internal/model"

	"github.com/BatMichoo/media/internal/db"
	"github.com/BatMichoo/media/internal/library"
	"github.com/BatMichoo/media/internal/subtitles"
)

// Upload is a subtitle file someone uploaded.
type Upload struct {
	Name, Lang string // Lang: "bg" or "en"
	Data       []byte
}

var subtitleLangs = map[string]bool{"bg": true, "en": true}

// MaxUploads is the most subtitle files taken with one download.
const MaxUploads = 30

// prepared is an upload that passed the checks, as UTF-8.
type prepared struct {
	up   Upload
	ext  string
	data []byte
}

func prepare(up Upload) (prepared, error) {
	if !subtitleLangs[up.Lang] {
		return prepared{}, invalid("error.sub_lang")
	}
	ext, data, err := subtitles.Prepare(up.Name, up.Data)
	switch {
	case errors.Is(err, subtitles.ErrType):
		return prepared{}, &InvalidError{Key: "error.sub_type", Arg: up.Name}
	case errors.Is(err, subtitles.ErrSize):
		return prepared{}, &InvalidError{Key: "error.sub_size", Arg: up.Name}
	case errors.Is(err, subtitles.ErrEmpty), errors.Is(err, subtitles.ErrBinary):
		return prepared{}, &InvalidError{Key: "error.sub_empty", Arg: up.Name}
	case err != nil:
		return prepared{}, err
	}
	return prepared{up, ext, data}, nil
}

// prepareAll checks every upload for a download of this kind before any
// is kept: all or nothing.
func prepareAll(kind model.Kind, ups []Upload) ([]prepared, error) {
	out := make([]prepared, 0, len(ups))
	for _, up := range ups {
		p, err := prepare(up)
		if err != nil {
			return nil, err
		}
		if _, ok := subtitles.Target(kind, up.Name); !ok {
			return nil, &InvalidError{Key: "error.sub_episode", Arg: up.Name}
		}
		out = append(out, p)
	}
	return out, nil
}

// keepAll keeps checked uploads with a download; one that's already in
// the library gets them linked in right away.
func (s *Service) keepAll(ctx context.Context, d db.Download, ps []prepared) error {
	if len(ps) == 0 {
		return nil
	}
	var rows []db.Subtitle
	for _, p := range ps {
		row, err := s.keep(ctx, d.Kind, db.Subtitle{Hash: d.Hash, Lang: p.up.Lang, Ext: p.ext, Name: p.up.Name}, p.data)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	if d.State != model.StateFiled {
		return nil
	}
	unplaced, err := s.placeNow(ctx, d, rows)
	if err != nil || unplaced == 0 {
		return err
	}
	setKey(&d, ReasonSubsUnplaced, unplaced)
	return s.store.UpdateDownload(ctx, d)
}

// placeNow links kept uploads next to a filed download's videos (found
// among the links it already made), replacing what's at those places.
func (s *Service) placeNow(ctx context.Context, d db.Download, rows []db.Subtitle) (int, error) {
	have, err := s.store.Links(ctx, d.Hash)
	if err != nil {
		return 0, err
	}
	links := make([]library.Link, len(have))
	for i, p := range have {
		links[i] = library.Link{To: p}
	}
	out, unplaced := library.PlaceSubtitles(d.Kind, d.Hash, links, librarySubs(rows))
	var made []string
	for _, l := range out {
		if !strings.HasPrefix(l.From, "subtitles/") {
			continue // a link the download made earlier
		}
		if err := s.clearPlace(ctx, d.Hash, l.To); err != nil {
			return 0, err
		}
		m, err := library.Apply(s.dataDir, []library.Link{l})
		made = append(made, m...)
		if err != nil {
			_ = s.store.AddLinks(ctx, d.Hash, made)
			return 0, err
		}
	}
	if err := s.store.AddLinks(ctx, d.Hash, made); err != nil {
		return 0, err
	}
	if len(made) > 0 {
		s.refresh(ctx)
	}
	return unplaced, nil
}

// clearPlace makes room for a subtitle at dst (library path): it removes
// what's there and the same language in the other formats (so a .ass
// replacing a .srt doesn't leave two), and forgets those links when a
// download (hash, "" for none) made them.
func (s *Service) clearPlace(ctx context.Context, hash, dst string) error {
	stem := strings.TrimSuffix(dst, path.Ext(dst))
	for ext := range subtitles.Exts {
		p := stem + ext
		if err := os.Remove(filepath.Join(s.dataDir, filepath.FromSlash(p))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if hash != "" && p != dst {
			if err := s.store.DeleteLink(ctx, hash, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// keep writes an upload's file under subtitles/<hash>/ and records it,
// first dropping an earlier upload for the same video and language.
func (s *Service) keep(ctx context.Context, kind model.Kind, row db.Subtitle, data []byte) (db.Subtitle, error) {
	have, err := s.store.Subtitles(ctx, row.Hash)
	if err != nil {
		return row, err
	}
	// The new one is saved before the one it replaces is dropped, so a
	// failure keeps the earlier upload.
	row.ID = newID()
	file := filepath.Join(s.dataDir, filepath.FromSlash(library.SubtitleFile(row.Hash, librarySub(row))))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return row, err
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		return row, err
	}
	if err := s.store.AddSubtitle(ctx, row); err != nil {
		os.Remove(file)
		return row, err
	}
	for _, old := range have {
		if old.Lang == row.Lang && sameVideo(kind, old, row) {
			if err := s.forget(ctx, old); err != nil {
				return row, err
			}
		}
	}
	return row, nil
}

// forget drops a kept upload: its record and its file (a library link to
// it stays until replaced or undone).
func (s *Service) forget(ctx context.Context, sub db.Subtitle) error {
	file := filepath.Join(s.dataDir, filepath.FromSlash(library.SubtitleFile(sub.Hash, librarySub(sub))))
	if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return s.store.DeleteSubtitle(ctx, sub.Hash, sub.ID)
}

// sameVideo is whether two uploads are for the same video.
func sameVideo(kind model.Kind, a, b db.Subtitle) bool {
	if a.Video != "" || b.Video != "" {
		return a.Video == b.Video
	}
	ta, oka := subtitles.Target(kind, a.Name)
	tb, okb := subtitles.Target(kind, b.Name)
	return oka && okb && ta == tb
}

// LibrarySubtitle puts an uploaded subtitle next to a video already in
// the library (video: its path relative to the data folder), named after
// it. When a download made that video the upload is kept with it, so it
// survives re-filing; a video put there by hand just gets the file.
func (s *Service) LibrarySubtitle(ctx context.Context, video string, kind model.Kind, up Upload) error {
	p, err := prepare(up)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dst := strings.TrimSuffix(video, path.Ext(video)) + "." + up.Lang + p.ext
	dstFile := filepath.Join(s.dataDir, filepath.FromSlash(dst))
	hash, err := s.store.LinkOwner(ctx, video)
	if errors.Is(err, db.ErrNotFound) {
		if err := s.clearPlace(ctx, "", dst); err != nil {
			return err
		}
		return writeFile(dstFile, p.data)
	}
	if err != nil {
		return err
	}
	row := db.Subtitle{Hash: hash, Lang: up.Lang, Ext: p.ext, Name: standIn(kind, video)}
	if row.Name == "" {
		row.Name, row.Video = path.Base(video), video
	}
	row, err = s.keep(ctx, kind, row, p.data)
	if err != nil {
		return err
	}
	// Uploads win over whatever is there (an earlier upload, the
	// torrent's own subtitle).
	if err := s.clearPlace(ctx, hash, dst); err != nil {
		return err
	}
	if _, err := library.Apply(s.dataDir, []library.Link{{From: library.SubtitleFile(hash, librarySub(row)), To: dst}}); err != nil {
		return err
	}
	return s.store.AddLinks(ctx, hash, []string{dst})
}

// standIn is the name that says which video a library upload is for, so
// re-filing finds it again: an episode code, "movie.partN" or "movie";
// "" for an episode whose file name has no code (it's pinned instead).
func standIn(kind model.Kind, video string) string {
	base := path.Base(video)
	if kind == model.KindSeries {
		code, _ := subtitles.EpisodeCode(base)
		return code
	}
	if n, ok := subtitles.PartNumber(strings.TrimSuffix(base, path.Ext(base))); ok {
		return "movie.part" + strconv.Itoa(n)
	}
	return "movie"
}

// writeFile writes data to a file through a temporary file, replacing it.
func writeFile(file string, data []byte) error {
	tmp := file + ".upload-" + newID()
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func librarySub(s db.Subtitle) library.Subtitle {
	return library.Subtitle{ID: s.ID, Lang: s.Lang, Ext: s.Ext, Name: s.Name, Video: s.Video, Label: s.Label}
}

func librarySubs(subs []db.Subtitle) []library.Subtitle {
	out := make([]library.Subtitle, len(subs))
	for i, s := range subs {
		out[i] = librarySub(s)
	}
	return out
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
