package library

import (
	"path"
	"regexp"
	"strings"

	"github.com/BatMichoo/media/internal/subtitles"
)

// Subtitle is an uploaded subtitle kept in the app's own folder. Name is
// the upload's name (or a stand-in such as "S01E03" or "part2") that
// says which video it belongs to; Video, when set, pins it to exactly
// that library path instead.
type Subtitle struct{ ID, Lang, Ext, Name, Video string }

// SubtitleFile is where an uploaded subtitle is kept, relative to the
// data folder.
func SubtitleFile(hash string, s Subtitle) string {
	return path.Join("subtitles", hash, s.ID+s.Ext)
}

var partSuffix = regexp.MustCompile(` - part(\d+)$`)

// PlaceSubtitles adds a link for each uploaded subtitle next to the video
// it belongs to, named after that video plus its language
// ("Dune (2021).bg.srt"). An upload takes the place of a torrent's own
// subtitle of the same name; of two uploads for the same place the later
// one wins. unplaced counts the uploads no video matched.
func PlaceSubtitles(kind, hash string, links []Link, subs []Subtitle) (out []Link, unplaced int) {
	var videos []Link
	for _, l := range links {
		if videoExt[strings.ToLower(path.Ext(l.To))] {
			videos = append(videos, l)
		}
	}
	placed := map[string]Link{}
	var order []string
	for _, s := range subs {
		v, ok := videoFor(kind, s, videos)
		if !ok {
			unplaced++
			continue
		}
		stem := strings.TrimSuffix(v.To, path.Ext(v.To))
		dst := stem + "." + s.Lang + s.Ext
		if _, seen := placed[dst]; !seen {
			order = append(order, dst)
		}
		placed[dst] = Link{SubtitleFile(hash, s), dst}
	}
	for _, l := range links {
		if _, taken := placed[l.To]; !taken {
			out = append(out, l)
		}
	}
	for _, dst := range order {
		out = append(out, placed[dst])
	}
	return out, unplaced
}

// videoFor finds the video link a subtitle belongs to.
func videoFor(kind string, s Subtitle, videos []Link) (Link, bool) {
	if s.Video != "" {
		for _, v := range videos {
			if v.To == s.Video {
				return v, true
			}
		}
		return Link{}, false
	}
	target, ok := subtitles.Target(kind, s.Name)
	if !ok {
		return Link{}, false
	}
	if kind == "series" {
		best, found := Link{}, false
		for _, v := range videos {
			if code, ok := subtitles.EpisodeCode(path.Base(v.To)); ok && code == target {
				if !found || len(v.To) < len(best.To) {
					best, found = v, true
				}
			}
		}
		return best, found
	}
	want := strings.TrimPrefix(target, "part")
	if target == "" {
		// The main video; for a movie in parts, part 1.
		for _, v := range videos {
			if path.Base(path.Dir(v.To)) != "extras" && partSuffix.FindString(stemOf(v.To)) == "" {
				return v, true
			}
		}
		want = "1"
	}
	for _, v := range videos {
		if m := partSuffix.FindStringSubmatch(stemOf(v.To)); m != nil && m[1] == want {
			return v, true
		}
	}
	return Link{}, false
}

func stemOf(p string) string { return strings.TrimSuffix(path.Base(p), path.Ext(p)) }
