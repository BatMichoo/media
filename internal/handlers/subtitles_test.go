package handlers

import (
	"bytes"
	"context"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/BatMichoo/media/internal/downloads"
	"github.com/BatMichoo/media/internal/jellyfin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLibrary struct {
	items     []string
	refreshes int
}

func (l *fakeLibrary) RefreshItem(_ context.Context, id string) error {
	l.items = append(l.items, id)
	return nil
}
func (l *fakeLibrary) Refresh(context.Context) error { l.refreshes++; return nil }

func subtitleForm(t *testing.T, fields map[string]string, name string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		require.NoError(t, mw.WriteField(k, v))
	}
	fw, err := mw.CreateFormFile("file", name)
	require.NoError(t, err)
	_, _ = fw.Write(data)
	require.NoError(t, mw.Close())
	return &b, mw.FormDataContentType()
}

func TestLibraryPath(t *testing.T) {
	for in, want := range map[string]string{
		"/data/library/Movies/X/X.mkv":     "library/Movies/X/X.mkv",
		"/media/library/Shows/S/e.mkv":     "library/Shows/S/e.mkv",
		"/media/other/x.mkv":               "",
		"/data/library/../etc/passwd":      "",
		"/data/library/Movies/../../x.mkv": "",
		"/data/library/Movies/Tick, Tick... Boom! (2021)/Tick, Tick... Boom! (2021).mkv": "library/Movies/Tick, Tick... Boom! (2021)/Tick, Tick... Boom! (2021).mkv",
	} {
		got, ok := libraryPath(in)
		assert.Equal(t, want != "", ok, in)
		assert.Equal(t, want, got, in)
	}
}

func TestMoviePageSubtitleForm(t *testing.T) {
	a := newApp(t)
	m := movie("Dune", "Dune (2021)", 2021)
	a.cat.items[m.ID] = m
	_, doc := a.get(t, "/movies/"+m.ID)
	form := doc.Find(`form[action="/subtitles/` + m.ID + `"]`)
	require.Equal(t, 1, form.Length())
	assert.Equal(t, "multipart/form-data", form.AttrOr("enctype", ""))
	assert.Equal(t, 1, form.Find(`input[type=file][name=file]`).Length())
	assert.Equal(t, 1, form.Find(`select[name=subtitle_lang] option[value=bg][selected]`).Length())
	assert.Equal(t, 0, form.Find(`select[name=part]`).Length())

	m.PartCount = 2
	a.cat.items[m.ID] = m
	a.cat.parts[m.ID] = []jellyfin.Item{m, {ID: id("p2"), Type: "Movie", Path: "/data/library/Movies/Dune (2021)/Dune (2021) - part2.mkv"}}
	_, doc = a.get(t, "/movies/"+m.ID)
	assert.Equal(t, 2, doc.Find(`form select[name=part] option`).Length())
}

func TestShowPageEpisodeSubtitleForm(t *testing.T) {
	a := newApp(t)
	sev := jellyfin.Item{ID: id("sev"), Type: "Series", Name: "Severance", Path: "/data/library/Shows/Severance"}
	a.cat.items[sev.ID] = sev
	a.cat.seasons[sev.ID] = []jellyfin.Item{{ID: id("s1"), Type: "Season", Name: "Season 1", Index: 1}}
	e1 := jellyfin.Item{ID: id("e1"), Type: "Episode", Name: "One", ParentIndex: 1, Index: 1}
	e2 := jellyfin.Item{ID: id("e2"), Type: "Episode", Name: "Two", ParentIndex: 1, Index: 2}
	a.cat.episodes[sev.ID] = []jellyfin.Item{e1, e2}
	_, doc := a.get(t, "/shows/"+sev.ID)
	assert.Equal(t, 1, doc.Find(`.episode form[action="/subtitles/`+e1.ID+`"]`).Length())
	assert.Equal(t, 1, doc.Find(`.episode form[action="/subtitles/`+e2.ID+`"]`).Length())
}

func TestPostSubtitlesMovie(t *testing.T) {
	a := newApp(t)
	m := movie("Dune", "Dune (2021)", 2021)
	a.cat.items[m.ID] = m
	body, ct := subtitleForm(t, map[string]string{"subtitle_lang": "bg"}, "dune.srt", []byte("hi"))
	w, _ := a.postMultipart(t, "/subtitles/"+m.ID, body, ct)
	assert.Equal(t, 303, w.Code)
	assert.Equal(t, "/movies/"+m.ID+"?subs=1", w.Header().Get("Location"))
	require.Len(t, a.dl.libSubs, 1)
	assert.Equal(t, libSubCall{Video: "library/Movies/Dune (2021)/Dune (2021).mkv", Kind: "movie", Upload: downloads.Upload{Name: "dune.srt", Lang: "bg", Data: []byte("hi")}}, a.dl.libSubs[0])
	assert.Equal(t, []string{m.ID}, a.lib.items)

	_, doc := a.get(t, "/movies/"+m.ID+"?subs=1")
	assert.Contains(t, doc.Find(".notice").Text(), "Subtitles added")
}

func TestPostSubtitlesPart(t *testing.T) {
	a := newApp(t)
	m := movie("Dune", "Dune (2021)", 2021)
	m.PartCount = 2
	a.cat.items[m.ID] = m
	a.cat.parts[m.ID] = []jellyfin.Item{m, {ID: id("p2"), Type: "Movie", Path: "/data/library/Movies/Dune (2021)/Dune (2021) - part2.mkv"}}
	body, ct := subtitleForm(t, map[string]string{"subtitle_lang": "en", "part": "2"}, "d.srt", []byte("hi"))
	w, _ := a.postMultipart(t, "/subtitles/"+m.ID, body, ct)
	assert.Equal(t, 303, w.Code)
	require.Len(t, a.dl.libSubs, 1)
	assert.Equal(t, "library/Movies/Dune (2021)/Dune (2021) - part2.mkv", a.dl.libSubs[0].Video)
}

func TestPostSubtitlesEpisode(t *testing.T) {
	a := newApp(t)
	ep := jellyfin.Item{ID: id("ep"), Type: "Episode", SeriesID: id("sev"), Path: "/data/library/Shows/Severance/S01E02.mkv"}
	a.cat.items[ep.ID] = ep
	body, ct := subtitleForm(t, map[string]string{"subtitle_lang": "bg"}, "x.srt", []byte("hi"))
	w, _ := a.postMultipart(t, "/subtitles/"+ep.ID, body, ct)
	assert.Equal(t, 303, w.Code)
	assert.Equal(t, "/shows/"+id("sev")+"?subs=1", w.Header().Get("Location"))
	require.Len(t, a.dl.libSubs, 1)
	assert.Equal(t, "series", a.dl.libSubs[0].Kind)
}

func TestPostSubtitlesRejects(t *testing.T) {
	a := newApp(t)
	sev := jellyfin.Item{ID: id("sev"), Type: "Series", Path: "/data/library/Shows/Severance"}
	a.cat.items[sev.ID] = sev
	body, ct := subtitleForm(t, nil, "x.srt", []byte("hi"))
	w, _ := a.postMultipart(t, "/subtitles/"+sev.ID, body, ct)
	assert.Equal(t, 400, w.Code)

	outside := jellyfin.Item{ID: id("o"), Type: "Movie", Path: "/elsewhere/x.mkv"}
	a.cat.items[outside.ID] = outside
	body, ct = subtitleForm(t, map[string]string{"subtitle_lang": "bg"}, "x.srt", []byte("hi"))
	w, _ = a.postMultipart(t, "/subtitles/"+outside.ID, body, ct)
	assert.Equal(t, 303, w.Code)
	assert.Equal(t, "/movies/"+outside.ID+"?subs_error=error.not_in_library", w.Header().Get("Location"))
	assert.Empty(t, a.dl.libSubs)
}

func TestPostSubtitlesInvalidShowsError(t *testing.T) {
	a := newApp(t)
	m := movie("Dune", "Dune (2021)", 2021)
	a.cat.items[m.ID] = m
	a.dl.libSubErr = &downloads.InvalidError{Key: "error.sub_type", Arg: "x.txt"}
	body, ct := subtitleForm(t, map[string]string{"subtitle_lang": "bg"}, "x.txt", []byte("hi"))
	w, _ := a.postMultipart(t, "/subtitles/"+m.ID, body, ct)
	assert.Equal(t, 303, w.Code)
	assert.Equal(t, "/movies/"+m.ID+"?subs_error=error.sub_type&subs_file=x.txt", w.Header().Get("Location"))
	_, doc := a.get(t, w.Header().Get("Location"))
	assert.Contains(t, doc.Find(".form-error").Text(), "x.txt isn't a subtitle file")
}

func TestPostSubtitlesTooBig(t *testing.T) {
	a := newApp(t)
	m := movie("Dune", "Dune (2021)", 2021)
	a.cat.items[m.ID] = m
	body, ct := subtitleForm(t, map[string]string{"subtitle_lang": "bg"}, "big.srt", bytes.Repeat([]byte("a"), 2<<20+512<<10))
	w, _ := a.postMultipart(t, "/subtitles/"+m.ID, body, ct)
	assert.Equal(t, 303, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "subs_error=error.sub_size")
}

func TestSubsErrorFileName(t *testing.T) {
	a := newApp(t)
	m := movie("Dune", "Dune (2021)", 2021)
	a.cat.items[m.ID] = m
	_, doc := a.get(t, "/movies/"+m.ID+"?subs_error=error.sub_missing&subs_file=Your+session+expired")
	assert.NotContains(t, doc.Find(".form-error").Text(), "session", "a message without a file name shows none")

	long := strings.Repeat("x", 300) + ".srt"
	_, doc = a.get(t, "/movies/"+m.ID+"?subs_error=error.sub_type&subs_file="+long)
	msg := doc.Find(".form-error").Text()
	assert.Less(t, len([]rune(msg)), 160, msg)
}

func TestDownloadsUnplacedReasonTranslated(t *testing.T) {
	a := newApp(t)
	a.dl.list = []downloads.Download{{Hash: "a1", Kind: "movie", Title: "Dune", Year: 2021, State: "filed", Reason: "2 subtitles didn't match a video"}}
	_, doc := a.get(t, "/downloads", "Cookie", "homeserver_lang=bg")
	assert.Contains(t, doc.Find(".reason").Text(), "2 субтитри")
	_, doc = a.get(t, "/downloads")
	assert.Contains(t, doc.Find(".reason").Text(), "2 subtitles didn't match a video")
}
