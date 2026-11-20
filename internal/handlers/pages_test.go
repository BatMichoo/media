package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/BatMichoo/media/internal/downloads"
	"github.com/BatMichoo/media/internal/jellyfin"
	"github.com/BatMichoo/media/internal/watching"
	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func movie(name, title string, year int) jellyfin.Item {
	return jellyfin.Item{ID: id(name), Type: "Movie", Name: name, Year: year, HasPrimary: true,
		Path: "/data/library/Movies/" + title + "/" + title + ".mkv"}
}

func TestHomeSections(t *testing.T) {
	a := newApp(t)
	dune := movie("Dune: Part One", "Dune (2021)", 2021)
	ep := jellyfin.Item{ID: id("ep"), Type: "Episode", Name: "Pilot", SeriesID: id("sev"), ParentIndex: 1, Index: 2,
		Path: "/data/library/Shows/Severance/S01/S01E02.mkv"}
	a.cat.latest["Movie"] = []jellyfin.Item{dune}
	a.cat.latest["Series"] = []jellyfin.Item{{ID: id("sev"), Type: "Series", Name: "Severance (US)", Path: "/data/library/Shows/Severance"}}
	a.w.cont = []watching.Entry{
		{Item: dune, Progress: &watching.Progress{ItemID: dune.ID, Watched: 30, Duration: 120}},
		{Item: ep},
	}
	w, doc := a.get(t, "/")
	require.Equal(t, 200, w.Code)
	var headings []string
	doc.Find("main h2").Each(func(_ int, s *goquery.Selection) { headings = append(headings, strings.TrimSpace(s.Text())) })
	assert.Equal(t, []string{"Continue watching", "Movies", "Shows"}, headings)

	cont := doc.Find("#continue .card-title")
	require.Equal(t, 2, cont.Length())
	assert.Equal(t, "Dune (2021)", strings.TrimSpace(cont.First().Text()), "typed title, not Jellyfin's")
	assert.Equal(t, "Severance", strings.TrimSpace(doc.Find("#continue .card-title").Eq(1).Text()))
	assert.Contains(t, doc.Find("#continue .card-sub").Eq(1).Text(), "S1 · E2")
	bar, _ := doc.Find("#continue .bar span").First().Attr("style")
	assert.Equal(t, "width: 25%", bar)
	href, _ := doc.Find("#continue a").First().Attr("href")
	assert.Equal(t, "/watch/"+dune.ID, href)

	assert.Equal(t, "Dune (2021)", strings.TrimSpace(doc.Find("#movies .card-title").Text()))
	mh, _ := doc.Find("#movies a.card-link").Attr("href")
	assert.Equal(t, "/movies/"+dune.ID, mh)
	img, _ := doc.Find("#movies img").Attr("src")
	assert.Equal(t, "/images/"+dune.ID+"/Primary?w=300", img)
	assert.Equal(t, "Severance", strings.TrimSpace(doc.Find("#shows .card-title").Text()))
	assert.Equal(t, 0, doc.Find("#shows img").Length(), "no poster in Jellyfin, no picture to break")
	assert.Equal(t, "Severance", strings.TrimSpace(doc.Find("#shows .art-title").Text()), "the title stands in")
	assert.True(t, doc.Find("#continue .media-card").First().HasClass("wide"))
	assert.Equal(t, 1, doc.Find("#continue img").Length(), "Dune falls back to its poster, the episode has nothing")
	assert.Equal(t, 1, doc.Find(`a[href="/add"]`).Length())
	assert.Equal(t, "Media", strings.TrimSpace(doc.Find("header h3 .app-crumb").Text()))
}

func TestCardPictures(t *testing.T) {
	sev := id("sev")
	ep := jellyfin.Item{ID: id("ep"), Type: "Episode", SeriesID: sev}
	assert.Equal(t, "", itemCard(ep).Image, "no series poster")
	ep.HasSeriesPrimary = true
	assert.Equal(t, "/images/"+sev+"/Primary?w=300", itemCard(ep).Image)
	assert.Equal(t, "/images/"+sev+"/Primary?w=300", wideCard(ep).Image, "no frame: the poster")
	ep.HasPrimary = true
	assert.Equal(t, "/images/"+ep.ID+"/Primary?w=480", wideCard(ep).Image, "the episode's own frame")

	m := jellyfin.Item{ID: id("m"), Type: "Movie", HasPrimary: true}
	assert.Equal(t, "/images/"+m.ID+"/Primary?w=300", wideCard(m).Image)
	m.HasBackdrop = true
	assert.Equal(t, "/images/"+m.ID+"/Backdrop?w=480", wideCard(m).Image)
	assert.Equal(t, hue("Dune"), hue("Dune"), "the same tint each time")
}

func TestHomeJellyfinDown(t *testing.T) {
	a := newApp(t)
	a.cat.ready = false
	w, doc := a.get(t, "/")
	assert.Equal(t, 200, w.Code)
	assert.Contains(t, doc.Find(".unavailable").Text(), "can't be reached")
	assert.Equal(t, 0, doc.Find("#movies").Length())
	assert.Equal(t, 1, doc.Find(`a[href="/downloads"], [data-path="/downloads"]`).Length())

	a.cat.ready, a.cat.fail = true, true
	w, doc = a.get(t, "/")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, 1, doc.Find(".unavailable").Length())
}

func TestNavFragment(t *testing.T) {
	a := newApp(t)
	a.cat.latest["Movie"] = []jellyfin.Item{movie("Dune", "Dune (2021)", 2021)}
	w, doc := a.get(t, "/movies", "HX-Request", "true", "HX-Target", "content")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, 0, doc.Find("header").Length(), "only the content")
	assert.Equal(t, 1, doc.Find(".media-card").Length())
	_, doc = a.get(t, "/movies")
	assert.Equal(t, 1, doc.Find("header").Length(), "a full page otherwise")
}

func TestShowPageMarksWatched(t *testing.T) {
	a := newApp(t)
	sev := jellyfin.Item{ID: id("sev"), Type: "Series", Name: "Severance (US)", Path: "/data/library/Shows/Severance", Overview: "Work."}
	a.cat.items[sev.ID] = sev
	a.cat.seasons[sev.ID] = []jellyfin.Item{{ID: id("s1"), Type: "Season", Name: "Season 1", Index: 1}}
	e1 := jellyfin.Item{ID: id("e1"), Type: "Episode", Name: "One", ParentIndex: 1, Index: 1}
	e2 := jellyfin.Item{ID: id("e2"), Type: "Episode", Name: "Two", ParentIndex: 1, Index: 2}
	e3 := jellyfin.Item{ID: id("e3"), Type: "Episode", Name: "Three", ParentIndex: 1, Index: 3}
	a.cat.episodes[sev.ID] = []jellyfin.Item{e1, e2, e3}
	a.w.prog[e1.ID] = watching.Progress{Finished: true, Watched: 95, Duration: 100}
	a.w.prog[e2.ID] = watching.Progress{Watched: 50, Duration: 100}

	w, doc := a.get(t, "/shows/"+sev.ID)
	require.Equal(t, 200, w.Code)
	assert.Equal(t, "Severance", strings.TrimSpace(doc.Find("main h1").Text()))
	assert.Equal(t, "Season 1", strings.TrimSpace(doc.Find("main h2").First().Text()))
	eps := doc.Find(".episode")
	require.Equal(t, 3, eps.Length())
	assert.True(t, eps.Eq(0).HasClass("watched"))
	style, _ := eps.Eq(1).Find(".bar span").Attr("style")
	assert.Equal(t, "width: 50%", style)
	assert.Equal(t, 0, eps.Eq(2).Find(".bar").Length())
	href, _ := eps.Eq(2).Find("a").Attr("href")
	assert.Equal(t, "/watch/"+e3.ID, href)
}

func TestMoviePageResume(t *testing.T) {
	a := newApp(t)
	dune := movie("Dune", "Dune (2021)", 2021)
	dune.Overview = "Spice."
	a.cat.items[dune.ID] = dune
	extra := jellyfin.Item{ID: id("x"), Type: "Video", Name: "Behind the scenes"}
	a.cat.extras[dune.ID] = []jellyfin.Item{extra}
	a.w.prog[dune.ID] = watching.Progress{Watched: 754, Duration: 6000}

	_, doc := a.get(t, "/movies/"+dune.ID)
	assert.Equal(t, "Dune (2021)", strings.TrimSpace(doc.Find("main h1").Text()))
	assert.Contains(t, doc.Find("main").Text(), "Spice.")
	play := doc.Find("a.play")
	assert.Equal(t, "Resume at 12:34", strings.TrimSpace(play.Text()))
	href, _ := play.Attr("href")
	assert.Equal(t, "/watch/"+dune.ID, href)
	xh, _ := doc.Find(".extras a").Attr("href")
	assert.Equal(t, "/watch/"+extra.ID, xh)

	delete(a.w.prog, dune.ID)
	_, doc = a.get(t, "/movies/"+dune.ID)
	assert.Equal(t, "Play", strings.TrimSpace(doc.Find("a.play").Text()))

	w, _ := a.get(t, "/movies/"+id("nope"))
	assert.Equal(t, 404, w.Code)
}

func multipartAdd(t *testing.T, fields map[string]string, file []byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		require.NoError(t, mw.WriteField(k, v))
	}
	if file != nil {
		fw, err := mw.CreateFormFile("torrent", "x.torrent")
		require.NoError(t, err)
		_, _ = fw.Write(file)
	}
	require.NoError(t, mw.Close())
	return &b, mw.FormDataContentType()
}

func (a *app) postMultipart(t *testing.T, url string, body *bytes.Buffer, ct string) (*httptest.ResponseRecorder, *goquery.Document) {
	req := httptest.NewRequest("POST", url, body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-User-Id", testUser)
	req.Header.Set("X-User-Name", "Alice")
	req.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	a.r.ServeHTTP(w, req)
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(w.Body.String()))
	require.NoError(t, err)
	return w, doc
}

func TestAddRedirects(t *testing.T) {
	a := newApp(t)
	body, ct := multipartAdd(t, map[string]string{"magnet": "magnet:?xt=1", "kind": "movie", "title": "Dune", "year": "2021"}, nil)
	w, _ := a.postMultipart(t, "/add", body, ct)
	assert.Equal(t, 303, w.Code)
	assert.Equal(t, "/downloads", w.Header().Get("Location"))
	require.Len(t, a.dl.added, 1)
	assert.Equal(t, downloads.AddInput{Magnet: "magnet:?xt=1", Kind: "movie", Title: "Dune", Year: 2021}, a.dl.added[0])
	assert.Equal(t, "Alice", a.dl.addedUser.Name)

	body, ct = multipartAdd(t, map[string]string{"kind": "series", "title": "S", "year": ""}, []byte("d8:info"))
	w, _ = a.postMultipart(t, "/add", body, ct)
	assert.Equal(t, 303, w.Code)
	assert.Equal(t, []byte("d8:info"), a.dl.added[1].File)
	assert.Equal(t, 0, a.dl.added[1].Year)
}

func TestAddValidationKeepsInput(t *testing.T) {
	a := newApp(t)
	a.dl.addErr = &downloads.InvalidError{Key: "error.title"}
	body, ct := multipartAdd(t, map[string]string{"magnet": "magnet:?xt=1", "kind": "series", "title": "???", "year": "2020"}, nil)
	w, doc := a.postMultipart(t, "/add", body, ct)
	assert.Equal(t, 422, w.Code)
	assert.Contains(t, doc.Find(".form-error").Text(), "Give a title")
	assert.Contains(t, doc.Find(".form-error").Text(), "left out", "says why ??? doesn't count")
	v, _ := doc.Find(`input[name="title"]`).Attr("value")
	assert.Equal(t, "???", v)
	y, _ := doc.Find(`input[name="year"]`).Attr("value")
	assert.Equal(t, "2020", y)
	_, checked := doc.Find(`input[name="kind"][value="series"]`).Attr("checked")
	assert.True(t, checked)
	m, _ := doc.Find(`input[name="magnet"]`).Attr("value")
	assert.Equal(t, "magnet:?xt=1", m)
}

func TestAddBadYear(t *testing.T) {
	a := newApp(t)
	body, ct := multipartAdd(t, map[string]string{"magnet": "magnet:?xt=1", "kind": "movie", "title": "D", "year": "soon"}, nil)
	w, doc := a.postMultipart(t, "/add", body, ct)
	assert.Equal(t, 422, w.Code)
	assert.Equal(t, "That year doesn't look right.", strings.TrimSpace(doc.Find(".form-error").Text()))
	assert.Empty(t, a.dl.added)
}

func TestDownloadsPage(t *testing.T) {
	a := newApp(t)
	until := time.Date(2026, 11, 18, 9, 30, 0, 0, time.Local)
	a.dl.list = []downloads.Download{
		{Hash: "a1", Kind: "movie", Title: "Dune", Year: 2021, State: "downloading", Percent: 0.42, RateDownload: 1_500_000, AddedByName: "Alice",
			Size: 4_500_000_000, Done: 1_900_000_000, ETA: 75 * time.Minute},
		{Hash: "b2", Kind: "series", Title: "Severance", State: "filed", SeedingUntil: until, AddedByName: "Bob"},
		{Hash: "c3", Kind: "movie", Title: "Old", Year: 1999, State: "filed", SeedingUntil: until, SeedingDone: true},
		{Hash: "d4", Kind: "movie", Title: "Bad", Year: 2000, State: "failed", ReasonKey: downloads.ReasonNoVideo},
	}
	_, doc := a.get(t, "/downloads")
	rows := doc.Find("#downloads-list .download")
	require.Equal(t, 4, rows.Length())
	assert.Contains(t, rows.Eq(0).Text(), "Dune (2021)")
	assert.Contains(t, rows.Eq(0).Text(), "42%")
	assert.Contains(t, rows.Eq(0).Text(), "1.5 MB/s")
	assert.Contains(t, rows.Eq(0).Text(), "1.9 GB of 4.5 GB")
	assert.Contains(t, rows.Eq(0).Text(), "1 h 15 min left")
	assert.Contains(t, rows.Eq(0).Text(), "Added by Alice")
	assert.Contains(t, rows.Eq(1).Text(), "Seeding until 18 Nov 09:30")
	assert.Contains(t, rows.Eq(2).Text(), "Seeding done")
	assert.Contains(t, rows.Eq(3).Text(), "Failed")
	assert.Contains(t, rows.Eq(3).Text(), "No video files")
	// The sheet's content goes inside the dialog, never replacing it.
	doc.Find(`[hx-target="#sheet"]`).Each(func(_ int, b *goquery.Selection) {
		swap, _ := b.Attr("hx-swap")
		assert.Equal(t, "innerHTML", swap)
	})
	assert.Equal(t, 8, doc.Find(`[hx-target="#sheet"]`).Length())
	poll, _ := doc.Find("#downloads-list").Attr("hx-trigger")
	assert.Equal(t, "every 5s", poll)

	// The list fragment alone, for polling.
	w, doc := a.get(t, "/downloads/list")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, 0, doc.Find("header").Length())
	assert.Equal(t, 4, doc.Find(".download").Length())
}

func TestDownloadsUnknownSizeAndTime(t *testing.T) {
	// A magnet link before its details arrive: no size, no time left.
	a := newApp(t)
	a.dl.list = []downloads.Download{{Hash: "a1", Kind: "movie", Title: "Dune", Year: 2021, State: "downloading"}}
	_, doc := a.get(t, "/downloads")
	text := doc.Find(".download").Text()
	assert.NotContains(t, text, " of ")
	assert.NotContains(t, text, "left")
}

func TestSizeWords(t *testing.T) {
	assert.Equal(t, "512 B", sizeWords(512))
	assert.Equal(t, "12 kB", sizeWords(12_345))
	assert.Equal(t, "700 MB", sizeWords(700_400_000))
	assert.Equal(t, "4.5 GB", sizeWords(4_500_000_000))
}

func TestTimeLeftWords(t *testing.T) {
	assert.Equal(t, "1 min", timeLeft("en", 10*time.Second))
	assert.Equal(t, "15 min", timeLeft("en", 14*time.Minute+30*time.Second))
	assert.Equal(t, "2 h", timeLeft("en", 2*time.Hour))
	assert.Equal(t, "1 h 15 min", timeLeft("en", 75*time.Minute))
	assert.Equal(t, "3 d 4 h", timeLeft("en", 76*time.Hour+20*time.Minute))
	assert.Equal(t, "1 ч 15 мин", timeLeft("bg", 75*time.Minute))
}

func TestDownloadsLinkWarning(t *testing.T) {
	a := newApp(t)
	a.link = assertErr("can't hard-link from downloads/ into library/")
	_, doc := a.get(t, "/downloads")
	assert.Contains(t, doc.Find(".link-problem").Text(), "can't hard-link")
}

func TestDownloadsTransmissionDown(t *testing.T) {
	a := newApp(t)
	a.dl.list = []downloads.Download{{Hash: "a1", Kind: "movie", Title: "Dune", Year: 2021, State: "downloading"}}
	a.dl.listErr = assertErr("transmission: connection refused")
	w, doc := a.get(t, "/downloads")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, 1, doc.Find(".download").Length())
	assert.Contains(t, doc.Find(".unreachable").Text(), "Transmission can't be reached")
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func TestEditSheetAndSave(t *testing.T) {
	a := newApp(t)
	a.dl.list = []downloads.Download{{Hash: "a1", Kind: "movie", Title: "Dune", Year: 2020, State: "filed"}}
	_, doc := a.get(t, "/downloads/a1/edit", "HX-Request", "true")
	v, _ := doc.Find(`input[name="title"]`).Attr("value")
	assert.Equal(t, "Dune", v)
	action, _ := doc.Find("form").Attr("hx-post")
	assert.Equal(t, "/downloads/a1/edit", action)

	w, doc := a.post(t, "/downloads/a1/edit", url.Values{"kind": {"movie"}, "title": {"Dune"}, "year": {"2021"}}, "HX-Request", "true")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, []string{"a1 movie Dune 2021"}, a.dl.edited)
	assert.Equal(t, 1, doc.Find("#downloads-list").Length())
	assert.Equal(t, 1, doc.Find("#toast").Length())

	a.dl.editErr = &downloads.InvalidError{Key: "error.title"}
	w, doc = a.post(t, "/downloads/a1/edit", url.Values{"kind": {"movie"}, "title": {"?"}, "year": {"2021"}}, "HX-Request", "true")
	assert.Equal(t, "#sheet", w.Header().Get("HX-Retarget"))
	assert.Contains(t, doc.Find(".form-error").Text(), "Give a title")

	w, _ = a.get(t, "/downloads/zz/edit")
	assert.Equal(t, 404, w.Code)
}

func TestRemoveForm(t *testing.T) {
	a := newApp(t)
	a.dl.list = []downloads.Download{{Hash: "a1", Kind: "movie", Title: "Dune", Year: 2021, State: "filed"}}
	_, doc := a.get(t, "/downloads/a1/remove", "HX-Request", "true")
	assert.Contains(t, doc.Text(), "Dune")
	assert.Equal(t, 1, doc.Find(`input[type="checkbox"][name="delete_files"]`).Length())

	a.post(t, "/downloads/a1/remove", url.Values{"delete_files": {"on"}}, "HX-Request", "true")
	a.post(t, "/downloads/a1/remove", url.Values{}, "HX-Request", "true")
	assert.Equal(t, []removeCall{{"a1", true}, {"a1", false}}, a.dl.removed)
}

func TestPauseResume(t *testing.T) {
	a := newApp(t)
	until := time.Now().Add(time.Hour)
	a.dl.list = []downloads.Download{
		{Hash: "a1", Kind: "movie", Title: "Dune", Year: 2021, State: "downloading"},
		{Hash: "b2", Kind: "movie", Title: "Heat", Year: 1995, State: "downloading", Paused: true},
		{Hash: "c3", Kind: "movie", Title: "Seeding", Year: 1995, State: "filed", SeedingUntil: until},
		{Hash: "d4", Kind: "movie", Title: "Done", Year: 1995, State: "filed", SeedingUntil: until, SeedingDone: true},
		{Hash: "e5", Kind: "movie", Title: "Bad", Year: 1995, State: "failed"},
	}
	_, doc := a.get(t, "/downloads")
	rows := doc.Find(".download")
	post := func(i int) string { v, _ := rows.Eq(i).Find("button.pause").Attr("hx-post"); return v }
	assert.Equal(t, "/downloads/a1/pause", post(0))
	assert.Equal(t, "/downloads/b2/resume", post(1))
	assert.True(t, rows.Eq(1).HasClass("paused"))
	assert.Contains(t, rows.Eq(1).Find(".state-paused").Text(), "Paused")
	assert.Equal(t, "/downloads/c3/pause", post(2), "seeding can be paused")
	assert.Equal(t, 0, rows.Eq(3).Find("button.pause").Length(), "seeding over")
	assert.Equal(t, 0, rows.Eq(4).Find("button.pause").Length(), "failed")

	w, doc := a.post(t, "/downloads/a1/pause", url.Values{}, "HX-Request", "true")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, 1, doc.Find("#downloads-list").Length(), "answers with the list")
	a.post(t, "/downloads/b2/resume", url.Values{}, "HX-Request", "true")
	assert.Equal(t, []string{"pause a1", "resume b2"}, a.dl.paused)
	w, _ = a.post(t, "/downloads/zz/pause", url.Values{}, "HX-Request", "true")
	assert.Equal(t, 404, w.Code)
}

func TestOriginCheck(t *testing.T) {
	a := newApp(t)
	w, _ := a.post(t, "/lang", url.Values{"lang": {"bg"}}, "Origin", "https://evil.example")
	assert.Equal(t, 403, w.Code)
	w, _ = a.post(t, "/lang", url.Values{"lang": {"bg"}}, "Origin", "https://media.homeserver.lan")
	assert.NotEqual(t, 403, w.Code)
}

func TestLangCookie(t *testing.T) {
	a := newApp(t)
	w, _ := a.post(t, "/lang", url.Values{"lang": {"bg"}}, "HX-Request", "true")
	assert.Equal(t, "true", w.Header().Get("HX-Refresh"))
	c := w.Header().Get("Set-Cookie")
	assert.Contains(t, c, "homeserver_lang=bg")
	assert.Contains(t, c, "Domain=homeserver.lan")
}

func TestHomeHidesEmptyContinue(t *testing.T) {
	a := newApp(t)
	_, doc := a.get(t, "/")
	assert.Equal(t, 0, doc.Find("#continue").Length())
	assert.Equal(t, "Movies", strings.TrimSpace(doc.Find("main h2").First().Text()))
}

func TestBulgarian(t *testing.T) {
	a := newApp(t)
	a.w.cont = []watching.Entry{{Item: movie("Dune", "Dune (2021)", 2021)}}
	_, doc := a.get(t, "/", "Cookie", "homeserver_lang=bg")
	assert.Equal(t, "Продължи гледането", strings.TrimSpace(doc.Find("main h2").First().Text()))
	lang, _ := doc.Find("html").Attr("lang")
	assert.Equal(t, "bg", lang)
}

func TestPagesNeedUser(t *testing.T) {
	a := newApp(t)
	for _, p := range []string{"/", "/movies", "/downloads", "/add"} {
		w := httptest.NewRecorder()
		a.r.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		assert.Equal(t, 401, w.Code, p)
	}
}

func TestHeaderLeadsHome(t *testing.T) {
	a := newApp(t)
	_, doc := a.get(t, "/")
	assert.Equal(t, "https://homeserver.lan/", doc.Find("header a.home-crumb").AttrOr("href", ""))
	assert.Contains(t, doc.Find("header .app-crumb").Text(), "Media")
	_, doc = a.get(t, "/movies", "HX-Request", "true", "HX-Target", "content")
	assert.Equal(t, 0, doc.Find(".home-crumb").Length(), "fragments have no header")
}

func TestAddPageSubtitleField(t *testing.T) {
	a := newApp(t)
	_, doc := a.get(t, "/add")
	assert.Equal(t, 1, doc.Find(`input[type=file][name=subtitles][multiple]`).Length())
	assert.Equal(t, 1, doc.Find(`select[name=subtitle_lang] option[value=bg][selected]`).Length())
	assert.Equal(t, 1, doc.Find(`select[name=subtitle_lang] option[value=en]`).Length())
}

func TestPostAddWithSubtitle(t *testing.T) {
	a := newApp(t)
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range map[string]string{"magnet": "magnet:?xt=1", "kind": "movie", "title": "Dune", "year": "2021", "subtitle_lang": "bg"} {
		require.NoError(t, mw.WriteField(k, v))
	}
	fw, err := mw.CreateFormFile("subtitles", "dune.srt")
	require.NoError(t, err)
	_, _ = fw.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"))
	require.NoError(t, mw.Close())
	w, _ := a.postMultipart(t, "/add", &b, mw.FormDataContentType())
	assert.Equal(t, 303, w.Code)
	require.Len(t, a.dl.added, 1)
	require.Len(t, a.dl.added[0].Subtitles, 1)
	assert.Equal(t, "bg", a.dl.added[0].Subtitles[0].Lang)
	assert.Equal(t, "dune.srt", a.dl.added[0].Subtitles[0].Name)
}

func TestAddSubtitleErrorNamesFile(t *testing.T) {
	a := newApp(t)
	a.dl.addErr = &downloads.InvalidError{Key: "error.sub_episode", Arg: "x.srt"}
	body, ct := multipartAdd(t, map[string]string{"magnet": "magnet:?xt=1", "kind": "series", "title": "S"}, nil)
	w, doc := a.postMultipart(t, "/add", body, ct)
	assert.Equal(t, 422, w.Code)
	assert.Contains(t, doc.Find(".form-error").Text(), "x.srt")
}
