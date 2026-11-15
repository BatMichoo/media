// Package library files finished downloads into the Jellyfin library
// with hard links (never copies), and undoes exactly what it made.
//
// All paths are relative to the data folder: downloads/ (Transmission's)
// and library/Movies, library/Shows (Jellyfin's).
package library

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/BatMichoo/media/internal/transmission"
)

// ErrNoVideo means a torrent has no video files to file.
var ErrNoVideo = errors.New("no video files")

// Link is one hard link to make: From (under downloads/) to To (under
// library/), both relative to the data folder.
type Link struct{ From, To string }

var (
	videoExt    = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".webm": true, ".ts": true}
	subtitleExt = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".vtt": true, ".sub": true, ".idx": true}
	partMarker  = regexp.MustCompile(`(?i)[ ._-](cd|part|pt|disc|disk)[ ._-]?(\d+)`)
)

const sampleMax = 200 << 20

// CleanTitle makes a title safe as a folder name: no / \ : * ? " < > |
// or control characters, spaces collapsed, no spaces or dots at the ends.
// Letters of any script stay.
func CleanTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.Trim(strings.Join(strings.Fields(s), " "), " .")
}

// folderName is "<Title> (<Year>)", or the title alone without a year.
func folderName(title string, year int) string {
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

type file struct {
	rel  string // as Transmission gives it
	from string // downloads/<rel>
	base string // file name
	stem string // file name without extension
	ext  string
	size int64
}

func newFile(tf transmission.File) (file, error) {
	from := path.Join("downloads", tf.Name)
	if !strings.HasPrefix(from, "downloads/") || strings.HasPrefix(path.Clean(tf.Name), "../") || path.IsAbs(tf.Name) {
		return file{}, fmt.Errorf("file %q is outside the download folder", tf.Name)
	}
	base := path.Base(tf.Name)
	ext := path.Ext(base)
	return file{rel: tf.Name, from: from, base: base, stem: strings.TrimSuffix(base, ext), ext: ext, size: tf.Length}, nil
}

func (f file) isVideo() bool    { return videoExt[strings.ToLower(f.ext)] }
func (f file) isSubtitle() bool { return subtitleExt[strings.ToLower(f.ext)] }
func (f file) isSample() bool {
	// "sample" anywhere in the path: Sample/x.mkv counts too.
	return strings.Contains(strings.ToLower(f.rel), "sample") && f.size < sampleMax
}

// Plan says where each file of a finished torrent goes. kind is "movie"
// or "series"; year 0 means none.
func Plan(kind, title string, year int, files []transmission.File) ([]Link, error) {
	name := CleanTitle(title)
	if name == "" {
		return nil, errors.New("the title is empty")
	}
	var videos, subs []file
	for _, tf := range files {
		f, err := newFile(tf)
		if err != nil {
			return nil, err
		}
		switch {
		case f.isVideo() && !f.isSample():
			videos = append(videos, f)
		case f.isSubtitle():
			subs = append(subs, f)
		}
	}
	if len(videos) == 0 {
		return nil, ErrNoVideo
	}
	folder := folderName(name, year)
	switch kind {
	case "movie":
		return planMovie(path.Join("library/Movies", folder), folder, videos, subs), nil
	case "series":
		return planSeries(path.Join("library/Shows", folder), videos, subs, files), nil
	}
	return nil, fmt.Errorf("unknown kind %q", kind)
}

// placed is a video and where it went (directory and name without
// extension), for matching its subtitles.
type placed struct {
	f       file
	dir     string
	newStem string
}

func planMovie(dir, folder string, videos, subs []file) []Link {
	var out []placed
	if parts, ok := numberedParts(videos); ok {
		for i, f := range parts {
			out = append(out, placed{f, dir, fmt.Sprintf("%s - part%d", folder, i+1)})
		}
	} else {
		main := 0
		for i, f := range videos {
			if f.size > videos[main].size {
				main = i
			}
		}
		out = append(out, placed{videos[main], dir, folder})
		for i, f := range videos {
			if i != main {
				out = append(out, placed{f, path.Join(dir, "extras"), f.stem})
			}
		}
	}
	var links []Link
	for _, p := range out {
		links = append(links, Link{p.f.from, path.Join(p.dir, p.newStem+p.f.ext)})
	}
	for _, s := range subs {
		if p, suffix, ok := matchSubtitle(s, out); ok {
			links = append(links, Link{s.from, path.Join(p.dir, p.newStem+suffix+s.ext)})
			continue
		}
		links = append(links, Link{s.from, path.Join(dir, s.base)})
	}
	return links
}

// numberedParts orders the videos by their part marker (cd1, part2,
// disc3, ...) when every one of them has one.
func numberedParts(videos []file) ([]file, bool) {
	if len(videos) < 2 {
		return nil, false
	}
	type numbered struct {
		f file
		n int
	}
	var ns []numbered
	for _, f := range videos {
		m := partMarker.FindStringSubmatch(f.stem)
		if m == nil {
			return nil, false
		}
		n, _ := strconv.Atoi(m[2])
		ns = append(ns, numbered{f, n})
	}
	sort.SliceStable(ns, func(i, j int) bool { return ns[i].n < ns[j].n })
	out := make([]file, len(ns))
	for i, n := range ns {
		out[i] = n.f
	}
	return out, true
}

// matchSubtitle finds the video a subtitle belongs to: the one whose name
// (without extension) starts the subtitle's name, the longest such. The
// rest of the subtitle's name (e.g. ".en") is kept.
func matchSubtitle(s file, videos []placed) (placed, string, bool) {
	best, found := placed{}, false
	for _, v := range videos {
		vs := v.f.stem
		ss := s.stem
		if len(ss) < len(vs) || !strings.EqualFold(ss[:len(vs)], vs) {
			continue
		}
		if rest := ss[len(vs):]; rest != "" && rest[0] != '.' {
			continue
		}
		if !found || len(vs) > len(best.f.stem) {
			best, found = v, true
		}
	}
	if !found {
		return placed{}, "", false
	}
	return best, s.stem[len(best.f.stem):], true
}

// planSeries links every video and subtitle into the show's folder,
// keeping the torrent's own paths; Jellyfin reads S01E02 from the names.
func planSeries(dir string, videos, subs []file, all []transmission.File) []Link {
	keep := map[string]bool{}
	for _, f := range append(append([]file{}, videos...), subs...) {
		keep[f.rel] = true
	}
	var links []Link
	for _, tf := range all { // the torrent's order
		if keep[tf.Name] {
			links = append(links, Link{path.Join("downloads", tf.Name), path.Join(dir, tf.Name)})
		}
	}
	return links
}
