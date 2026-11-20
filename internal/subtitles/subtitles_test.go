package subtitles

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareCP1251(t *testing.T) {
	// "Привет" in Windows-1251, inside an SRT cue.
	in := append([]byte("1\r\n00:00:01,000 --> 00:00:02,000\r\n"), 0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2)
	ext, out, err := Prepare("film.bg.srt", in)
	require.NoError(t, err)
	assert.Equal(t, ".srt", ext)
	assert.True(t, strings.HasSuffix(string(out), "\r\nПривет"), string(out))
}

func TestPrepareUTF8BOM(t *testing.T) {
	_, out, err := Prepare("a.vtt", []byte("\xEF\xBB\xBFhello"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(out))
}

func TestPrepareKeepsUTF8(t *testing.T) {
	_, out, err := Prepare("a.srt", []byte("Здравей"))
	require.NoError(t, err)
	assert.Equal(t, "Здравей", string(out))
}

func TestPrepareRejects(t *testing.T) {
	_, _, err := Prepare("x.txt", []byte("a"))
	assert.ErrorIs(t, err, ErrType)
	_, _, err = Prepare("x.srt", nil)
	assert.ErrorIs(t, err, ErrEmpty)
	_, _, err = Prepare("x.srt", bytes.Repeat([]byte("a"), MaxSize+1))
	assert.ErrorIs(t, err, ErrSize)
	_, _, err = Prepare("x.srt", []byte("a\x00b"))
	assert.ErrorIs(t, err, ErrBinary)
	ext, _, err := Prepare("X.SRT", []byte("a"))
	require.NoError(t, err)
	assert.Equal(t, ".srt", ext)
}

func TestEpisodeCode(t *testing.T) {
	for name, want := range map[string]string{
		"Show.S01E03.720p.srt": "S01E03",
		"show s1e3.srt":        "S01E03",
		"Show.1x03.srt":        "S01E03",
		"Show.S10E120.srt":     "S10E120",
		"Movie.1080p.x264.srt": "",
		"Movie.2160p.srt":      "",
	} {
		got, ok := EpisodeCode(name)
		assert.Equal(t, want != "", ok, name)
		assert.Equal(t, want, got, name)
	}
}

func TestTarget(t *testing.T) {
	cases := []struct {
		kind, name, want string
		ok               bool
	}{
		{"movie", "Dune CD2.srt", "part2", true},
		{"movie", "Dune.srt", "", true},
		{"series", "x.S02E05.srt", "S02E05", true},
		{"series", "x.srt", "", false},
	}
	for _, c := range cases {
		got, ok := Target(c.kind, c.name)
		assert.Equal(t, c.ok, ok, c.name)
		assert.Equal(t, c.want, got, c.name)
	}
}

func TestPrepareUTF16(t *testing.T) {
	le := []byte{0xFF, 0xFE}
	be := []byte{0xFE, 0xFF}
	for _, r := range "Здравей\r\n" {
		le = append(le, byte(r), byte(r>>8))
		be = append(be, byte(r>>8), byte(r))
	}
	_, out, err := Prepare("a.srt", le)
	require.NoError(t, err)
	assert.Equal(t, "Здравей\r\n", string(out))
	_, out, err = Prepare("a.srt", be)
	require.NoError(t, err)
	assert.Equal(t, "Здравей\r\n", string(out))
}
