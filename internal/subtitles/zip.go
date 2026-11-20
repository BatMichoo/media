package subtitles

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode"
)

// MaxEntries is the most subtitle files taken from one zip.
const MaxEntries = 30

var (
	ErrZip         = errors.New("not a zip file that can be opened")
	ErrNoSubtitles = errors.New("no subtitle files in the zip")
	ErrCount       = errors.New("too many subtitle files in the zip")
)

// EntryError is a problem with one file inside a zip.
type EntryError struct {
	Name string // the file's name (without folders)
	Err  error
}

func (e *EntryError) Error() string { return e.Name + ": " + e.Err.Error() }
func (e *EntryError) Unwrap() error { return e.Err }

// Entry is a subtitle file taken out of a zip, named without its folders.
type Entry struct {
	Name string
	Data []byte
}

// Unzip takes the subtitle files (.srt, .ass, .ssa, .vtt) out of a zip, in
// the zip's order. Folders, macOS's __MACOSX copies, hidden files and
// other files are left out. Each file is read through a limit, so one that
// unpacks to more than MaxSize is refused whatever its header says.
func Unzip(data []byte) ([]Entry, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrZip
	}
	var out []Entry
	for _, f := range r.File {
		name := path.Base(strings.ReplaceAll(f.Name, "\\", "/"))
		if f.FileInfo().IsDir() || strings.HasPrefix(name, ".") || !Exts[strings.ToLower(path.Ext(name))] ||
			strings.Contains("/"+f.Name+"/", "/__MACOSX/") {
			continue
		}
		if len(out) == MaxEntries {
			return nil, ErrCount
		}
		rc, err := f.Open()
		if err != nil {
			return nil, &EntryError{Name: name, Err: ErrZip}
		}
		b, err := io.ReadAll(io.LimitReader(rc, MaxSize+1))
		rc.Close()
		switch {
		case err != nil:
			return nil, &EntryError{Name: name, Err: ErrZip}
		case len(b) > MaxSize:
			return nil, &EntryError{Name: name, Err: ErrSize}
		}
		out = append(out, Entry{Name: name, Data: b})
	}
	if len(out) == 0 {
		return nil, ErrNoSubtitles
	}
	return out, nil
}

// maxLabel is the longest label, in characters.
const maxLabel = 40

// Labels tells apart subtitles for the same video by their file names: the
// words (split on dots, underscores and spaces) left once the ones all
// names share at the start and at the end are dropped, e.g. "BluRay
// x264-SPARKS" and "WEB-DL DDP5 1 H 264-FLUX". Characters a file name
// can't hold are removed and a label is at most 40 characters. When a
// label comes out empty or two are the same, they're numbered "1", "2", …
// instead. One name (or none) needs no label: "".
func Labels(names []string) []string {
	out := make([]string, len(names))
	if len(names) < 2 {
		return out
	}
	words := make([][]string, len(names))
	for i, n := range names {
		stem := strings.TrimSuffix(n, path.Ext(n))
		words[i] = strings.FieldsFunc(stem, func(r rune) bool { return r == '.' || r == '_' || r == ' ' })
	}
	lead, trail := common(words, false), common(words, true)
	seen := map[string]bool{}
	numbered := false
	for i, w := range words {
		end := len(w) - trail
		if end < lead {
			end = lead
		}
		out[i] = cleanLabel(strings.Join(w[lead:end], " "))
		key := strings.ToLower(out[i])
		if out[i] == "" || seen[key] {
			numbered = true
		}
		seen[key] = true
	}
	if numbered {
		for i := range out {
			out[i] = strconv.Itoa(i + 1)
		}
	}
	return out
}

// common is how many words all lists share (case-insensitively) at the
// start, or at the end with fromEnd, never all of the shortest list.
func common(words [][]string, fromEnd bool) int {
	shortest := len(words[0])
	for _, w := range words {
		shortest = min(shortest, len(w))
	}
	n := 0
	for ; n < shortest; n++ {
		at := func(w []string) string {
			if fromEnd {
				return strings.ToLower(w[len(w)-1-n])
			}
			return strings.ToLower(w[n])
		}
		first := at(words[0])
		for _, w := range words[1:] {
			if at(w) != first {
				return n
			}
		}
	}
	return n
}

// cleanLabel drops what a file name can't hold, keeps at most maxLabel
// characters and trims spaces and dots at the ends.
func cleanLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > maxLabel {
		s = string(r[:maxLabel])
	}
	return strings.Trim(s, " .")
}
