package library

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlaceMovie(t *testing.T) {
	dir := "library/Movies/Dune (2021)"
	links := []Link{
		{"downloads/h/Dune.mkv", dir + "/Dune (2021).mkv"},
		{"downloads/h/Dune.bg.srt", dir + "/Dune (2021).bg.srt"},
		{"downloads/h/trailer.mkv", dir + "/extras/trailer.mkv"},
	}
	out, unplaced := PlaceSubtitles("movie", "h", links, []Subtitle{{ID: "a", Lang: "bg", Ext: ".srt", Name: "whatever.srt"}})
	assert.Equal(t, 0, unplaced)
	assert.Equal(t, []Link{
		{"downloads/h/Dune.mkv", dir + "/Dune (2021).mkv"},
		{"downloads/h/trailer.mkv", dir + "/extras/trailer.mkv"},
		{"subtitles/h/a.srt", dir + "/Dune (2021).bg.srt"},
	}, out)
}

func TestPlaceMultiPart(t *testing.T) {
	dir := "library/Movies/X"
	links := []Link{
		{"downloads/h/x.cd1.mkv", dir + "/X - part1.mkv"},
		{"downloads/h/x.cd2.mkv", dir + "/X - part2.mkv"},
	}
	out, unplaced := PlaceSubtitles("movie", "h", links, []Subtitle{
		{ID: "a", Lang: "bg", Ext: ".srt", Name: "x cd2.srt"},
		{ID: "b", Lang: "en", Ext: ".srt", Name: "x.srt"},
	})
	assert.Equal(t, 0, unplaced)
	assert.Contains(t, out, Link{"subtitles/h/a.srt", dir + "/X - part2.bg.srt"})
	assert.Contains(t, out, Link{"subtitles/h/b.srt", dir + "/X - part1.en.srt"})
}

func TestPlaceSeries(t *testing.T) {
	dir := "library/Shows/S/Season 1"
	links := []Link{
		{"downloads/h/S.S01E03.mkv", dir + "/S.S01E03.mkv"},
		{"downloads/h/S.S01E04.mkv", dir + "/S.S01E04.mkv"},
	}
	out, unplaced := PlaceSubtitles("series", "h", links, []Subtitle{
		{ID: "a", Lang: "bg", Ext: ".srt", Name: "S.1x04.srt"},
		{ID: "b", Lang: "bg", Ext: ".srt", Name: "S.S02E01.srt"},
	})
	assert.Equal(t, 1, unplaced)
	assert.Contains(t, out, Link{"subtitles/h/a.srt", dir + "/S.S01E04.bg.srt"})
	assert.Len(t, out, 3)
}

func TestPlacePinned(t *testing.T) {
	video := "library/Shows/S/03 - Title.mkv"
	links := []Link{{"downloads/h/03 - Title.mkv", video}}
	pinned := []Subtitle{{ID: "a", Lang: "bg", Ext: ".srt", Name: "03 - Title.mkv", Video: video}}
	out, unplaced := PlaceSubtitles("series", "h", links, pinned)
	assert.Equal(t, 0, unplaced)
	assert.Contains(t, out, Link{"subtitles/h/a.srt", "library/Shows/S/03 - Title.bg.srt"})

	_, unplaced = PlaceSubtitles("series", "h", []Link{{"downloads/h/03 - Title.mkv", "library/Shows/Other/03 - Title.mkv"}}, pinned)
	assert.Equal(t, 1, unplaced)
}

func TestPlaceKindChange(t *testing.T) {
	dir := "library/Movies/Dune"
	links := []Link{{"downloads/h/Dune.mkv", dir + "/Dune.mkv"}}
	out, unplaced := PlaceSubtitles("movie", "h", links, []Subtitle{{ID: "a", Lang: "bg", Ext: ".srt", Name: "Dune.S01E01.srt"}})
	assert.Equal(t, 0, unplaced)
	assert.Contains(t, out, Link{"subtitles/h/a.srt", dir + "/Dune.bg.srt"})
}

func TestPlaceLaterWins(t *testing.T) {
	dir := "library/Movies/Dune"
	links := []Link{{"downloads/h/Dune.mkv", dir + "/Dune.mkv"}}
	out, _ := PlaceSubtitles("movie", "h", links, []Subtitle{
		{ID: "old", Lang: "bg", Ext: ".srt", Name: "a.srt"},
		{ID: "new", Lang: "bg", Ext: ".srt", Name: "b.srt"},
	})
	assert.Equal(t, []Link{links[0], {"subtitles/h/new.srt", dir + "/Dune.bg.srt"}}, out)
}
