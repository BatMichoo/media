package handlers

import (
	"archive/zip"
	"bytes"
	"fmt"
	"mime/multipart"
	"net/url"
	"strings"
	"testing"

	"github.com/BatMichoo/media/internal/downloads"
	"github.com/BatMichoo/media/internal/subtitles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zipOf makes a zip of name/content pairs.
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

func TestAddZipSubtitles(t *testing.T) {
	a := newApp(t)
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range map[string]string{"magnet": "magnet:?xt=1", "kind": "series", "title": "Show", "subtitle_lang": "bg"} {
		require.NoError(t, mw.WriteField(k, v))
	}
	fw, err := mw.CreateFormFile("subtitles", "Show.S01E01.ZIP")
	require.NoError(t, err)
	_, _ = fw.Write(zipOf(t, "a.S01E01.srt", "one", "b.S01E01.srt", "two", "readme.txt", "x"))
	require.NoError(t, mw.Close())
	w, _ := a.postMultipart(t, "/add", &b, mw.FormDataContentType())
	assert.Equal(t, 303, w.Code)
	require.Len(t, a.dl.added, 1)
	assert.Equal(t, []downloads.Upload{
		{Name: "a.S01E01.srt", Lang: "bg", Data: []byte("one")},
		{Name: "b.S01E01.srt", Lang: "bg", Data: []byte("two")},
	}, a.dl.added[0].Subtitles)
}

func TestMovieZipSubtitles(t *testing.T) {
	a := newApp(t)
	m := movie("Dune", "Dune (2021)", 2021)
	a.cat.items[m.ID] = m
	body, ct := subtitleForm(t, map[string]string{"subtitle_lang": "bg"}, "dune.zip", zipOf(t, "Dune.BluRay.srt", "blu", "Dune.WEB.srt", "web"))
	w, _ := a.postMultipart(t, "/subtitles/"+m.ID, body, ct)
	assert.Equal(t, 303, w.Code)
	assert.Equal(t, "/movies/"+m.ID+"?subs=1", w.Header().Get("Location"))
	require.Len(t, a.dl.libSubs, 1)
	assert.Equal(t, []downloads.Upload{
		{Name: "Dune.BluRay.srt", Lang: "bg", Data: []byte("blu")},
		{Name: "Dune.WEB.srt", Lang: "bg", Data: []byte("web")},
	}, a.dl.libSubs[0].Uploads)
}

func TestBadZip(t *testing.T) {
	for name, c := range map[string]struct {
		data      []byte
		key, file string
	}{
		"not a zip":   {[]byte("junk"), "error.zip_bad", "subs.zip"},
		"no subs":     {zipOf(t, "readme.txt", "x"), "error.zip_empty", "subs.zip"},
		"entry big":   {zipOf(t, "big.srt", strings.Repeat("a", subtitles.MaxSize+1)), "error.sub_size", "big.srt"},
		"zip too big": {bytes.Repeat([]byte("a"), subtitles.MaxSize+1), "error.sub_size", "subs.zip"},
	} {
		t.Run(name, func(t *testing.T) {
			a := newApp(t)
			m := movie("Dune", "Dune (2021)", 2021)
			a.cat.items[m.ID] = m
			body, ct := subtitleForm(t, map[string]string{"subtitle_lang": "bg"}, "subs.zip", c.data)
			w, _ := a.postMultipart(t, "/subtitles/"+m.ID, body, ct)
			assert.Equal(t, 303, w.Code)
			loc, err := url.Parse(w.Header().Get("Location"))
			require.NoError(t, err)
			assert.Equal(t, c.key, loc.Query().Get("subs_error"))
			assert.Equal(t, c.file, loc.Query().Get("subs_file"))
			assert.Empty(t, a.dl.libSubs)
		})
	}
}

func TestBadZipMessages(t *testing.T) {
	a := newApp(t)
	m := movie("Dune", "Dune (2021)", 2021)
	a.cat.items[m.ID] = m
	_, doc := a.get(t, "/movies/"+m.ID+"?subs_error=error.zip_bad&subs_file=subs.zip")
	assert.Contains(t, doc.Text(), "subs.zip can't be opened as a zip file.")
	_, doc = a.get(t, "/movies/"+m.ID+"?subs_error=error.zip_empty&subs_file=subs.zip", "Cookie", "homeserver_lang=bg")
	assert.Contains(t, doc.Text(), "subs.zip")
	assert.NotContains(t, doc.Text(), "error.zip_empty")
}

func TestUploadAcceptsZip(t *testing.T) {
	a := newApp(t)
	_, doc := a.get(t, "/add")
	assert.Contains(t, doc.Find(`input[name=subtitles]`).AttrOr("accept", ""), ".zip")
	m := movie("Dune", "Dune (2021)", 2021)
	a.cat.items[m.ID] = m
	_, doc = a.get(t, "/movies/"+m.ID)
	assert.Contains(t, doc.Find(`form[action="/subtitles/`+m.ID+`"] input[name=file]`).AttrOr("accept", ""), ".zip")
}

func TestManyZipsStopAtTheLimit(t *testing.T) {
	// Each zip is checked against what's left of the 30, so a request
	// full of zips is refused before unpacking them all.
	a := newApp(t)
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range map[string]string{"magnet": "magnet:?xt=1", "kind": "movie", "title": "Dune", "year": "2021", "subtitle_lang": "bg"} {
		require.NoError(t, mw.WriteField(k, v))
	}
	var files []string
	for i := 0; i < 20; i++ {
		files = append(files, fmt.Sprintf("s%02d.srt", i), "x")
	}
	for i := 0; i < 2; i++ {
		fw, err := mw.CreateFormFile("subtitles", fmt.Sprintf("z%d.zip", i))
		require.NoError(t, err)
		_, _ = fw.Write(zipOf(t, files...))
	}
	require.NoError(t, mw.Close())
	w, _ := a.postMultipart(t, "/add", &b, mw.FormDataContentType())
	assert.Equal(t, 422, w.Code)
	assert.Empty(t, a.dl.added)
}
