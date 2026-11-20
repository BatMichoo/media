package subtitles

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zipOf makes a zip of name/content pairs; a name ending in "/" is a
// folder.
func zipOf(t *testing.T, files ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for i := 0; i < len(files); i += 2 {
		f, err := w.Create(files[i])
		require.NoError(t, err)
		_, err = f.Write([]byte(files[i+1]))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestUnzip(t *testing.T) {
	data := zipOf(t,
		"Subs/", "",
		"Subs/Dune.BluRay.srt", "one",
		"readme.txt", "hi",
		"__MACOSX/._Dune.WEB.srt", "junk",
		".hidden.srt", "junk",
		"Dune.WEB.srt", "two",
	)
	got, err := Unzip(data)
	require.NoError(t, err)
	assert.Equal(t, []Entry{{Name: "Dune.BluRay.srt", Data: []byte("one")}, {Name: "Dune.WEB.srt", Data: []byte("two")}}, got)
}

func TestUnzipNoSubtitles(t *testing.T) {
	_, err := Unzip(zipOf(t, "readme.txt", "hi"))
	assert.ErrorIs(t, err, ErrNoSubtitles)
}

func TestUnzipBad(t *testing.T) {
	_, err := Unzip([]byte("not a zip"))
	assert.ErrorIs(t, err, ErrZip)
}

func TestUnzipEntryTooBig(t *testing.T) {
	_, err := Unzip(zipOf(t, "small.srt", "x", "big.srt", strings.Repeat("a", MaxSize+1)))
	assert.ErrorIs(t, err, ErrSize)
	var e *EntryError
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "big.srt", e.Name)
}

func TestUnzipTooMany(t *testing.T) {
	var files []string
	for i := 0; i <= MaxEntries; i++ {
		files = append(files, fmt.Sprintf("e%02d.srt", i), "x")
	}
	_, err := Unzip(zipOf(t, files...))
	assert.ErrorIs(t, err, ErrCount)
}

func TestLabels(t *testing.T) {
	long := strings.Repeat("Ab", 30)
	for _, c := range []struct {
		names, want []string
	}{
		{[]string{"Dune.2021.1080p.BluRay.x264-SPARKS.srt", "Dune.2021.1080p.WEB-DL.DDP5.1.H.264-FLUX.srt"},
			[]string{"BluRay x264-SPARKS", "WEB-DL DDP5 1 H 264-FLUX"}},
		{[]string{"Dune.BluRay.bg.srt", "Dune.WEB.bg.srt"}, []string{"BluRay", "WEB"}},
		{[]string{"a.srt", "a.ass"}, []string{"1", "2"}},
		{[]string{"Movie.BG.srt", "movie.bg.srt"}, []string{"1", "2"}},
		{[]string{"Dune." + long + ".srt", "Dune.WEB.srt"}, []string{long[:40], "WEB"}},
		{[]string{"Dune: Part *Two*.srt", "Dune: Part <One>.srt"}, []string{"Two", "One"}},
		{[]string{"only.srt"}, []string{""}},
		{nil, []string{}},
	} {
		assert.Equal(t, c.want, Labels(c.names), "%v", c.names)
	}
}

func TestUnzipOEMNames(t *testing.T) {
	// Windows tools store names in the OEM code page (cp866 for
	// Cyrillic) without the UTF-8 flag; valid UTF-8 names stay as they are.
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, h := range []*zip.FileHeader{
		{Name: "\x84\xee\xad\xa0.srt", NonUTF8: true}, // "Дюна" in cp866
		{Name: "Дюна.WEB.srt"},
	} {
		f, err := w.CreateHeader(h)
		require.NoError(t, err)
		_, _ = f.Write([]byte("x"))
	}
	require.NoError(t, w.Close())
	got, err := Unzip(buf.Bytes())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "Дюна.srt", got[0].Name)
	assert.Equal(t, "Дюна.WEB.srt", got[1].Name)
}

func TestLabelsAvoidLanguageAndFlagWords(t *testing.T) {
	// Jellyfin would read these as a language or a flag, not a title.
	assert.Equal(t, []string{"1", "2"}, Labels([]string{"Dune.bg.srt", "Dune.en.srt"}))
	assert.Equal(t, []string{"1", "2"}, Labels([]string{"Dune.forced.srt", "Dune.full.srt"}))
	assert.Equal(t, []string{"BluRay", "WEB"}, Labels([]string{"Dune.BluRay.srt", "Dune.WEB.srt"}))
}
