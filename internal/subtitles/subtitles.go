// Package subtitles checks uploaded subtitle files, turns them into UTF-8
// and reads which video they belong to (episode code or movie part) from
// their names.
package subtitles

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxSize is the largest subtitle file accepted.
const MaxSize = 2 << 20

var (
	ErrType   = errors.New("not a subtitle file (.srt, .ass, .ssa, .vtt)")
	ErrSize   = errors.New("subtitle file too big")
	ErrEmpty  = errors.New("subtitle file is empty")
	ErrBinary = errors.New("not a text subtitle file")
)

// Exts are the subtitle formats accepted (text only).
var Exts = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".vtt": true}

// Prepare checks an upload and returns its extension (lower case, with
// the dot) and its text as UTF-8: valid UTF-8 stays as it is without a
// leading BOM; anything else is read as Windows-1251 (Cyrillic).
func Prepare(name string, data []byte) (ext string, out []byte, err error) {
	ext = strings.ToLower(filepath.Ext(name))
	switch {
	case !Exts[ext]:
		return "", nil, ErrType
	case len(data) == 0:
		return "", nil, ErrEmpty
	case len(data) > MaxSize:
		return "", nil, ErrSize
	}
	if u, ok := fromUTF16(data); ok {
		return ext, u, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", nil, ErrBinary
	}
	if utf8.Valid(data) {
		return ext, bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF")), nil
	}
	var b strings.Builder
	b.Grow(len(data) * 2)
	for _, c := range data {
		if c < 0x80 {
			b.WriteByte(c)
		} else {
			b.WriteRune(cp1251[c-0x80])
		}
	}
	return ext, []byte(b.String()), nil
}

// fromUTF16 decodes text that starts with a UTF-16 byte order mark
// (Windows "Unicode" files).
func fromUTF16(data []byte) ([]byte, bool) {
	if len(data) < 2 || len(data)%2 != 0 {
		return nil, false
	}
	var order binary.ByteOrder
	switch {
	case data[0] == 0xFF && data[1] == 0xFE:
		order = binary.LittleEndian
	case data[0] == 0xFE && data[1] == 0xFF:
		order = binary.BigEndian
	default:
		return nil, false
	}
	units := make([]uint16, 0, len(data)/2-1)
	for i := 2; i < len(data); i += 2 {
		units = append(units, order.Uint16(data[i:]))
	}
	return []byte(string(utf16.Decode(units))), true
}

// cp1251 maps Windows-1251 bytes 0x80..0xFF to runes.
var cp1251 = [128]rune{
	'Ђ', 'Ѓ', '‚', 'ѓ', '„', '…', '†', '‡', '€', '‰', 'Љ', '‹', 'Њ', 'Ќ', 'Ћ', 'Џ',
	'ђ', '‘', '’', '“', '”', '•', '–', '—', '�', '™', 'љ', '›', 'њ', 'ќ', 'ћ', 'џ',
	' ', 'Ў', 'ў', 'Ј', '¤', 'Ґ', '¦', '§', 'Ё', '©', 'Є', '«', '¬', '­', '®', 'Ї',
	'°', '±', 'І', 'і', 'ґ', 'µ', '¶', '·', 'ё', '№', 'є', '»', 'ј', 'Ѕ', 'ѕ', 'ї',
	'А', 'Б', 'В', 'Г', 'Д', 'Е', 'Ж', 'З', 'И', 'Й', 'К', 'Л', 'М', 'Н', 'О', 'П',
	'Р', 'С', 'Т', 'У', 'Ф', 'Х', 'Ц', 'Ч', 'Ш', 'Щ', 'Ъ', 'Ы', 'Ь', 'Э', 'Ю', 'Я',
	'а', 'б', 'в', 'г', 'д', 'е', 'ж', 'з', 'и', 'й', 'к', 'л', 'м', 'н', 'о', 'п',
	'р', 'с', 'т', 'у', 'ф', 'х', 'ц', 'ч', 'ш', 'щ', 'ъ', 'ы', 'ь', 'э', 'ю', 'я',
}

var (
	sxxeyy     = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})[ ._-]?e(\d{1,3})(?:[^0-9]|$)`)
	nxnn       = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(\d{1,2})x(\d{2,3})(?:[^0-9a-z]|$)`)
	partMarker = regexp.MustCompile(`(?i)[ ._-](cd|part|pt|disc|disk)[ ._-]?(\d+)`)
)

// EpisodeCode reads S01E03, s1e3 or 1x03 from a file name, as "S01E03".
func EpisodeCode(name string) (string, bool) {
	m := sxxeyy.FindStringSubmatch(name)
	if m == nil {
		m = nxnn.FindStringSubmatch(name)
	}
	if m == nil {
		return "", false
	}
	s, _ := strconv.Atoi(m[1])
	e, _ := strconv.Atoi(m[2])
	return fmt.Sprintf("S%02dE%02d", s, e), true
}

// PartNumber reads a movie part marker (cd2, part2, disc2) from a name.
func PartNumber(stem string) (int, bool) {
	m := partMarker.FindStringSubmatch(stem)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[2])
	return n, true
}

// Target is which video a subtitle with this name belongs to: for a
// movie "partN" (with a part marker) or "" (the main video, always ok);
// for a series its episode code, not ok without one.
func Target(kind, name string) (string, bool) {
	if kind == "series" {
		return EpisodeCode(name)
	}
	if n, ok := PartNumber(name); ok {
		return "part" + strconv.Itoa(n), true
	}
	return "", true
}
