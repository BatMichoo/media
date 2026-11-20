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
// the library gets them linked in right away. Several for the same video
// and language are all kept, each with a label from its name.
func (s *Service) keepAll(ctx context.Context, d db.Download, ps []prepared) error {
	if len(ps) == 0 {
		return nil
	}
	rows := make([]db.Subtitle, len(ps))
	data := make([][]byte, len(ps))
	for i, p := range ps {
		rows[i] = db.Subtitle{Hash: d.Hash, Lang: p.up.Lang, Ext: p.ext, Name: p.up.Name}
		data[i] = p.data
	}
	labelRows(d.Kind, rows)
	rows, err := s.keepBatch(ctx, d.Kind, d.Hash, rows, data)
	if err != nil {
		return err
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

// groupKey is the video and language a kept upload is for: uploads with
// the same key are variants of one track.
func groupKey(kind model.Kind, r db.Subtitle) string {
	if r.Video != "" {
		return "video\x00" + r.Video + "\x00" + r.Lang
	}
	target, _ := subtitles.Target(kind, r.Name)
	return "target\x00" + target + "\x00" + r.Lang
}

// labelRows gives each upload that shares its video and language with
// another one a label from its name (subtitles.Labels).
func labelRows(kind model.Kind, rows []db.Subtitle) {
	groups := map[string][]int{}
	var order []string
	for i, r := range rows {
		k := groupKey(kind, r)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], i)
	}
	for _, k := range order {
		idx := groups[k]
		names := make([]string, len(idx))
		for j, i := range idx {
			names[j] = rows[i].Name
		}
		for j, label := range subtitles.Labels(names) {
			rows[idx[j]].Label = label
		}
	}
}

// placeNow links kept uploads next to a filed download's videos (found
// among the links it already made), replacing what's there for those
// videos and languages: earlier uploads' links and the plain
// <video>.<lang> files.
func (s *Service) placeNow(ctx context.Context, d db.Download, rows []db.Subtitle) (int, error) {
	have, err := s.store.Links(ctx, d.Hash)
	if err != nil {
		return 0, err
	}
	links := make([]library.Link, len(have))
	for i, p := range have {
		links[i] = library.Link{To: p}
	}
	byFile := map[string]library.Subtitle{}
	for _, r := range rows {
		ls := librarySub(r)
		byFile[library.SubtitleFile(d.Hash, ls)] = ls
	}
	out, unplaced := library.PlaceSubtitles(d.Kind, d.Hash, links, librarySubs(rows))
	var add []library.Link
	cleared := map[string]bool{}
	for _, l := range out {
		sub, ok := byFile[l.From]
		if !ok {
			continue // a link the download made earlier
		}
		stem := videoStem(l.To, sub)
		if k := stem + "\x00" + sub.Lang; !cleared[k] {
			cleared[k] = true
			if err := s.clearGroup(ctx, d.Hash, stem, sub.Lang, have); err != nil {
				return 0, err
			}
		}
		add = append(add, l)
	}
	made, linkErr := library.Apply(s.dataDir, add)
	if err := s.store.AddLinks(ctx, d.Hash, made); err != nil {
		return 0, err
	}
	if linkErr != nil {
		return 0, linkErr
	}
	if len(made) > 0 {
		s.refresh(ctx)
	}
	return unplaced, nil
}

// videoStem is the video's path without its extension, from where a
// subtitle is placed (<stem>[.<label>].<lang><ext>).
func videoStem(dst string, sub library.Subtitle) string {
	stem := strings.TrimSuffix(dst, "."+sub.Lang+sub.Ext)
	if sub.Label != "" {
		stem = strings.TrimSuffix(stem, "."+sub.Label)
	}
	return stem
}

// clearGroup makes room for uploads for one video (stem: its library path
// without extension) and language: it removes <stem>.<lang> in every
// subtitle format, and, of the links the download (hash, "" for none)
// recorded in have, the labelled <stem>.<label>.<lang> ones, forgetting
// the links it removes.
func (s *Service) clearGroup(ctx context.Context, hash, stem, lang string, have []string) error {
	remove := func(p string) error {
		if err := os.Remove(filepath.Join(s.dataDir, filepath.FromSlash(p))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if hash == "" {
			return nil
		}
		return s.store.DeleteLink(ctx, hash, p)
	}
	for ext := range subtitles.Exts {
		if err := remove(stem + "." + lang + ext); err != nil {
			return err
		}
	}
	for _, p := range have {
		ext := path.Ext(p)
		rest, ok := strings.CutPrefix(strings.TrimSuffix(p, "."+lang+ext), stem+".")
		if !subtitles.Exts[ext] || !ok || rest == "" || !strings.HasSuffix(p, "."+lang+ext) {
			continue
		}
		if err := remove(p); err != nil {
			return err
		}
	}
	return nil
}

// keepBatch writes uploads' files under subtitles/<hash>/ and records
// them, then drops the earlier uploads for the same videos and languages
// (never one another). The new ones are saved first, so a failure keeps
// the earlier uploads.
func (s *Service) keepBatch(ctx context.Context, kind model.Kind, hash string, rows []db.Subtitle, data [][]byte) ([]db.Subtitle, error) {
	have, err := s.store.Subtitles(ctx, hash)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Hash, rows[i].ID = hash, newID()
		file := filepath.Join(s.dataDir, filepath.FromSlash(library.SubtitleFile(hash, librarySub(rows[i]))))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(file, data[i], 0o644); err != nil {
			return nil, err
		}
		if err := s.store.AddSubtitle(ctx, rows[i]); err != nil {
			os.Remove(file)
			return nil, err
		}
	}
	for _, old := range have {
		for _, r := range rows {
			if old.Lang == r.Lang && sameVideo(kind, old, r) {
				if err := s.forget(ctx, old); err != nil {
					return nil, err
				}
				break
			}
		}
	}
	return rows, nil
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

// LibrarySubtitle puts uploaded subtitles next to a video already in the
// library (video: its path relative to the data folder), named after it;
// several in one language (from a zip) are all kept, each labelled from
// its name. They replace earlier uploads for that video and language.
// When a download made that video the uploads are kept with it, so they
// survive re-filing; a video put there by hand just gets the files.
func (s *Service) LibrarySubtitle(ctx context.Context, video string, kind model.Kind, ups []Upload) error {
	switch {
	case len(ups) == 0:
		return invalid("error.sub_missing")
	case len(ups) > MaxUploads:
		return invalid("error.sub_count")
	}
	ps := make([]prepared, len(ups))
	for i, up := range ups {
		p, err := prepare(up)
		if err != nil {
			return err
		}
		ps[i] = p
	}
	// Labels per language, from the uploads' own names.
	labels := make([]string, len(ps))
	byLang := map[string][]int{}
	var langs []string
	for i, p := range ps {
		if _, ok := byLang[p.up.Lang]; !ok {
			langs = append(langs, p.up.Lang)
		}
		byLang[p.up.Lang] = append(byLang[p.up.Lang], i)
	}
	for _, l := range langs {
		idx := byLang[l]
		names := make([]string, len(idx))
		for j, i := range idx {
			names[j] = ps[i].up.Name
		}
		for j, label := range subtitles.Labels(names) {
			labels[idx[j]] = label
		}
	}
	stem := strings.TrimSuffix(video, path.Ext(video))
	dst := func(i int) string {
		if labels[i] != "" {
			return stem + "." + labels[i] + "." + ps[i].up.Lang + ps[i].ext
		}
		return stem + "." + ps[i].up.Lang + ps[i].ext
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	hash, err := s.store.LinkOwner(ctx, video)
	if errors.Is(err, db.ErrNotFound) {
		for _, l := range langs {
			if err := s.clearGroup(ctx, "", stem, l, nil); err != nil {
				return err
			}
		}
		for i, p := range ps {
			if err := writeFile(filepath.Join(s.dataDir, filepath.FromSlash(dst(i))), p.data); err != nil {
				return err
			}
		}
		return nil
	}
	if err != nil {
		return err
	}
	name, pinned := standIn(kind, video), ""
	if name == "" {
		name, pinned = path.Base(video), video
	}
	rows := make([]db.Subtitle, len(ps))
	data := make([][]byte, len(ps))
	for i, p := range ps {
		rows[i] = db.Subtitle{Lang: p.up.Lang, Ext: p.ext, Name: name, Video: pinned, Label: labels[i]}
		data[i] = p.data
	}
	if rows, err = s.keepBatch(ctx, kind, hash, rows, data); err != nil {
		return err
	}
	// Uploads win over whatever is there (earlier uploads, the torrent's
	// own subtitle).
	have, err := s.store.Links(ctx, hash)
	if err != nil {
		return err
	}
	for _, l := range langs {
		if err := s.clearGroup(ctx, hash, stem, l, have); err != nil {
			return err
		}
	}
	links := make([]library.Link, len(rows))
	for i, r := range rows {
		links[i] = library.Link{From: library.SubtitleFile(hash, librarySub(r)), To: dst(i)}
	}
	made, linkErr := library.Apply(s.dataDir, links)
	if err := s.store.AddLinks(ctx, hash, made); err != nil {
		return err
	}
	return linkErr
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
