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
	"unicode/utf8"

	"github.com/nwaples/rardecode/v2"
	"golang.org/x/text/encoding/charmap"
)

// MaxEntries is the most subtitle files taken from one archive.
const MaxEntries = 30

// maxDictionary bounds the memory a RAR archive may ask the decoder for
// (WinRAR's default is 32 MB for RAR5, 4 MB for RAR4).
const maxDictionary = 64 << 20

var (
	ErrArchive     = errors.New("not an archive that can be opened")
	ErrNoSubtitles = errors.New("no subtitle files in the archive")
	ErrCount       = errors.New("too many subtitle files in the archive")
)

// EntryError is a problem with one file inside an archive.
type EntryError struct {
	Name string // the file's name (without folders)
	Err  error
}

func (e *EntryError) Error() string { return e.Name + ": " + e.Err.Error() }
func (e *EntryError) Unwrap() error { return e.Err }

// Entry is a subtitle file taken out of an archive, named without its
// folders.
type Entry struct {
	Name string
	Data []byte
}

// IsArchive reports whether a file name is one Unpack opens (.zip, .rar).
func IsArchive(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".zip", ".rar":
		return true
	}
	return false
}

// Unpack takes the subtitle files (.srt, .ass, .ssa, .vtt) out of a zip or
// a RAR archive (by its name), in the archive's order, at most max of them
// (ErrCount past that), so several archives in one upload share one
// allowance. Folders, macOS's __MACOSX copies, hidden files, links and
// other files are left out. Each file is read through a limit, so one that
// unpacks to more than MaxSize is refused whatever its header says. A RAR
// split into parts or with a password isn't opened (ErrArchive): the
// reader can't tell a split one from a cut-off one, so both are refused
// whole, never taken in part.
func Unpack(name string, data []byte, max int) ([]Entry, error) {
	var out []Entry
	add := func(name string, open func() (io.ReadCloser, error)) error {
		name = path.Base(strings.ReplaceAll(entryName(name), "\\", "/"))
		if strings.HasPrefix(name, ".") || !Exts[strings.ToLower(path.Ext(name))] {
			return nil
		}
		if len(out) >= max {
			return ErrCount
		}
		rc, err := open()
		if err != nil {
			return &EntryError{Name: name, Err: ErrArchive}
		}
		b, err := io.ReadAll(io.LimitReader(rc, MaxSize+1))
		rc.Close()
		switch {
		case err != nil:
			return &EntryError{Name: name, Err: ErrArchive}
		case len(b) > MaxSize:
			return &EntryError{Name: name, Err: ErrSize}
		}
		out = append(out, Entry{Name: name, Data: b})
		return nil
	}
	var err error
	switch strings.ToLower(path.Ext(name)) {
	case ".zip":
		err = unzip(data, add)
	case ".rar":
		err = unrar(data, add)
	default:
		err = ErrArchive
	}
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNoSubtitles
	}
	return out, nil
}

// unzip hands each file of a zip to add.
func unzip(data []byte, add func(string, func() (io.ReadCloser, error)) error) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ErrArchive
	}
	for _, f := range r.File {
		if f.FileInfo().IsDir() || strings.Contains("/"+f.Name+"/", "/__MACOSX/") {
			continue
		}
		if err := add(f.Name, f.Open); err != nil {
			return err
		}
	}
	return nil
}

// unrar hands each file of a RAR archive (RAR4 or RAR5, one part) to add.
func unrar(data []byte, add func(string, func() (io.ReadCloser, error)) error) error {
	r, err := rardecode.NewReader(bytes.NewReader(data), rardecode.MaxDictionarySize(maxDictionary))
	if err != nil {
		return ErrArchive
	}
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return ErrArchive
		}
		if h.IsDir || h.LinkType != 0 || strings.Contains("/"+h.Name+"/", "/__MACOSX/") {
			continue
		}
		if err := add(h.Name, func() (io.ReadCloser, error) { return io.NopCloser(r), nil }); err != nil {
			return err
		}
	}
}

// entryName is an archive entry's name as UTF-8. Windows tools store names in
// the OEM code page (cp866 for Cyrillic) without saying so; a name that
// isn't valid UTF-8 is read as that.
func entryName(name string) string {
	if utf8.ValidString(name) {
		return name
	}
	if s, err := charmap.CodePage866.NewDecoder().String(name); err == nil {
		return s
	}
	return strings.ToValidUTF8(name, "_")
}

// notLabels are words Jellyfin reads as a language or a flag where the
// label goes in <video>.<label>.<lang><ext>, so they can't be labels.
var notLabels = map[string]bool{"bg": true, "bul": true, "bgr": true, "en": true, "eng": true,
	"default": true, "forced": true, "foreign": true, "sdh": true, "cc": true, "hi": true}

// maxLabel is the longest label, in characters.
const maxLabel = 40

// Labels tells apart subtitles for the same video by their file names: the
// words (split on dots, underscores and spaces) left once the ones all
// names share at the start and at the end are dropped, e.g. "BluRay
// x264-SPARKS" and "WEB-DL DDP5 1 H 264-FLUX". Characters a file name
// can't hold are removed and a label is at most 40 characters. When a
// label comes out empty or two are the same, they're numbered "1", "2", …
// instead, and also when a label is a word Jellyfin would read as a
// language or a flag ("bg", "forced"). One name (or none) needs no
// label: "".
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
		if out[i] == "" || seen[key] || notLabels[key] {
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
// start, or at the end with fromEnd (it can be all of the shortest list;
// that one's label is then empty and Labels numbers them).
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
