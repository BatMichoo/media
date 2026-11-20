package subtitles

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/BatMichoo/media/internal/subtitles/rartest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rarOf(t *testing.T, files ...string) []byte    { t.Helper(); return rartest.Of(files...) }
func rarSplit(t *testing.T, files ...string) []byte { t.Helper(); return rartest.Split(files...) }

// TestRarOfIsReal writes rartest's archive where unrar can check it, when
// RAR_CHECK names a folder (see the commit adding .rar uploads).
func TestRarOfIsReal(t *testing.T) {
	dir := os.Getenv("RAR_CHECK")
	if dir == "" {
		t.Skip("RAR_CHECK not set")
	}
	data := rarOf(t, "Subs/", "", "Subs/Дюна.BluRay.srt", "one", "Dune.WEB.srt", "two")
	if err := os.WriteFile(dir+"/check.rar", data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUnpackRar(t *testing.T) {
	data := rarOf(t, "Subs/", "", "Subs/Дюна.BluRay.srt", "one", "readme.txt", "hi", ".hidden.srt", "x", "Dune.WEB.srt", "two")
	got, err := Unpack("subs.RAR", data, MaxEntries)
	require.NoError(t, err)
	assert.Equal(t, []Entry{{Name: "Дюна.BluRay.srt", Data: []byte("one")}, {Name: "Dune.WEB.srt", Data: []byte("two")}}, got)
}

func TestUnpackZipToo(t *testing.T) {
	got, err := Unpack("subs.Zip", zipOf(t, "a.srt", "one"), MaxEntries)
	require.NoError(t, err)
	assert.Equal(t, []Entry{{Name: "a.srt", Data: []byte("one")}}, got)
}

func TestUnpackRarErrors(t *testing.T) {
	many := []string{}
	for i := 0; i <= MaxEntries; i++ {
		many = append(many, fmt.Sprintf("e%02d.srt", i), "x")
	}
	for name, c := range map[string]struct {
		data []byte
		want error
	}{
		"not a rar":    {[]byte("junk"), ErrArchive},
		"zip as rar":   {zipOf(t, "a.srt", "x"), ErrArchive},
		"no subtitles": {rarOf(t, "readme.txt", "hi"), ErrNoSubtitles},
		"too many":     {rarOf(t, many...), ErrCount},
		"split":        {rarSplit(t, "a.srt", "x"), ErrArchive}, // refused whole, not taken in part
	} {
		_, err := Unpack("subs.rar", c.data, MaxEntries)
		assert.ErrorIs(t, err, c.want, name)
	}
}

func TestUnpackRarEntryTooBig(t *testing.T) {
	_, err := Unpack("subs.rar", rarOf(t, "small.srt", "x", "big.srt", strings.Repeat("a", MaxSize+1)), MaxEntries)
	assert.ErrorIs(t, err, ErrSize)
	var e *EntryError
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "big.srt", e.Name)
}

func TestIsArchive(t *testing.T) {
	for name, want := range map[string]bool{"a.zip": true, "a.RAR": true, "a.srt": false, "rar": false, "a.7z": false} {
		assert.Equal(t, want, IsArchive(name), name)
	}
}
