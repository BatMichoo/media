package downloads

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

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
func prepareAll(kind string, ups []Upload) ([]prepared, error) {
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

// storeUploads keeps uploads for a download (checked first, all or
// nothing). They're linked into the library when it's filed.
func (s *Service) storeUploads(ctx context.Context, hash, kind string, ups []Upload) error {
	ps, err := prepareAll(kind, ups)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if _, err := s.keep(ctx, kind, db.Subtitle{Hash: hash, Lang: p.up.Lang, Ext: p.ext, Name: p.up.Name}, p.data); err != nil {
			return err
		}
	}
	return nil
}

// keep writes an upload's file under subtitles/<hash>/ and records it,
// first dropping an earlier upload for the same video and language.
func (s *Service) keep(ctx context.Context, kind string, row db.Subtitle, data []byte) (db.Subtitle, error) {
	have, err := s.store.Subtitles(ctx, row.Hash)
	if err != nil {
		return row, err
	}
	for _, old := range have {
		if old.Lang == row.Lang && sameVideo(kind, old, row) {
			if err := s.forget(ctx, old); err != nil {
				return row, err
			}
		}
	}
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
func sameVideo(kind string, a, b db.Subtitle) bool {
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
func (s *Service) LibrarySubtitle(ctx context.Context, video, kind string, up Upload) error {
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
	if err := os.Remove(dstFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
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
func standIn(kind, video string) string {
	base := path.Base(video)
	if kind == "series" {
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
	return library.Subtitle{ID: s.ID, Lang: s.Lang, Ext: s.Ext, Name: s.Name, Video: s.Video}
}

func librarySubs(subs []db.Subtitle) []library.Subtitle {
	out := make([]library.Subtitle, len(subs))
	for i, s := range subs {
		out[i] = librarySub(s)
	}
	return out
}

func unplacedText(n int) string {
	if n == 1 {
		return "1 subtitle didn't match a video"
	}
	return fmt.Sprintf("%d subtitles didn't match a video", n)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
