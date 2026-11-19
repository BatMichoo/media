package handlers

import (
	"encoding/json"
	"github.com/PuerkitoBio/goquery"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/BatMichoo/media/internal/jellyfin"
	"github.com/BatMichoo/media/internal/watching"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (a *app) postJSON(t *testing.T, url string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest("POST", url, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", testUser)
	req.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	a.r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestWatchPageData(t *testing.T) {
	a := newApp(t)
	m := movie("Dune", "Dune (2021)", 2021)
	m.PartCount = 2
	p2 := jellyfin.Item{ID: id("p2"), Type: "Movie", RunTimeSeconds: 3000}
	m.RunTimeSeconds = 3000
	a.cat.items[m.ID] = m
	a.cat.parts[m.ID] = []jellyfin.Item{m, p2}
	a.w.prog[m.ID] = watching.Progress{ItemID: m.ID, Part: 1, Position: 120, Watched: 3120, Duration: 6000}

	w, doc := a.get(t, "/watch/"+m.ID)
	require.Equal(t, 200, w.Code)
	p := doc.Find("#player")
	attr := func(k string) string { v, _ := p.Attr(k); return v }
	assert.Equal(t, m.ID, attr("data-item"))
	assert.Equal(t, "", attr("data-series"))
	assert.Equal(t, "1", attr("data-part"))
	assert.Equal(t, "120", attr("data-start"))
	assert.Equal(t, "/movies/"+m.ID, attr("data-back"))
	var parts []map[string]any
	require.NoError(t, json.Unmarshal([]byte(attr("data-parts")), &parts))
	assert.Equal(t, []map[string]any{{"id": m.ID, "duration": 3000.0}, {"id": p2.ID, "duration": 3000.0}}, parts)
	assert.Equal(t, 1, doc.Find(`script[src^="/static/media/js/player.js"]`).Length())
	assert.Equal(t, "Dune (2021) · Media", doc.Find("title").Text())
	assert.Equal(t, 1, doc.Find(`script[src^="/static/media/js/hls.min.js"]`).Length())
	var labels map[string]string
	require.NoError(t, json.Unmarshal([]byte(attr("data-labels")), &labels))
	for _, k := range []string{"mute", "unmute", "settings", "exitFullscreen", "subtitles", "audio", "off"} {
		assert.NotEmpty(t, labels[k], k)
		assert.NotEqual(t, "player."+k, labels[k], k) // i18n returns the key when missing
	}
	assert.Equal(t, 0, p.Find("select").Length())
	assert.Equal(t, 1, p.Find(`.player-seek[role="slider"]`).Length())
	assert.Equal(t, 2, p.Find(`[data-action="toggle"]`).Length())
	for _, a := range []string{"mute", "settings", "fullscreen"} {
		assert.Equal(t, 1, p.Find(`[data-action="`+a+`"]`).Length(), a)
	}
	assert.Equal(t, 1, p.Find(`.player-menu [data-group="subtitles"]`).Length())
	assert.Equal(t, 1, p.Find(`.player-menu [data-group="audio"]`).Length())
	assert.Equal(t, 1, p.Find(".player-spinner").Length())
	// The menu's groups are named groups whose lists don't hide the items from screen readers.
	p.Find(".player-menu section").Each(func(_ int, s *goquery.Selection) {
		role, _ := s.Attr("role")
		assert.Equal(t, "group", role)
		by, _ := s.Attr("aria-labelledby")
		require.NotEmpty(t, by)
		assert.Equal(t, 1, p.Find("h3#"+by).Length(), by)
		r, _ := s.Find("ul").Attr("role")
		assert.Equal(t, "none", r)
	})

	// The seek bar's name follows the page's language.
	_, bg := a.get(t, "/watch/"+m.ID, "Cookie", "homeserver_lang=bg")
	seekLabel, _ := bg.Find(".player-seek").Attr("aria-label")
	assert.Equal(t, "Превъртане", seekLabel)

	// A finished movie starts over; an explicit ?part= starts that part.
	a.w.prog[m.ID] = watching.Progress{ItemID: m.ID, Part: 1, Position: 2900, Watched: 5900, Duration: 6000, Finished: true}
	_, doc = a.get(t, "/watch/"+m.ID)
	v, _ := doc.Find("#player").Attr("data-start")
	assert.Equal(t, "0", v)
	v, _ = doc.Find("#player").Attr("data-part")
	assert.Equal(t, "0", v)
}

func TestWatchEpisode(t *testing.T) {
	a := newApp(t)
	ep := jellyfin.Item{ID: id("ep"), Type: "Episode", Name: "Pilot", SeriesID: id("sev"), RunTimeSeconds: 1800}
	a.cat.items[ep.ID] = ep
	_, doc := a.get(t, "/watch/"+ep.ID)
	p := doc.Find("#player")
	s, _ := p.Attr("data-series")
	assert.Equal(t, id("sev"), s)
	b, _ := p.Attr("data-back")
	assert.Equal(t, "/shows/"+id("sev"), b)

	w, _ := a.get(t, "/watch/"+id("nope"))
	assert.Equal(t, 404, w.Code)
}

func source() jellyfin.Source {
	return jellyfin.Source{
		Direct: false, URL: "/videos/" + vid + "/master.m3u8?PlaySessionId=ps1", PlaySessionID: "ps1", MediaSourceID: "ms1",
		DurationSeconds: 100,
		Subtitles:       []jellyfin.Track{{Index: 3, Label: "English", Lang: "eng"}, {Index: 4, Label: "Bulgarian PGS", Lang: "bul", Image: true}},
		Audio:           []jellyfin.Track{{Index: 1, Label: "English", Lang: "eng", Default: true}, {Index: 2, Label: "Bulgarian", Lang: "bul"}},
	}
}

var browserCaps = map[string]any{"containers": []string{"mp4"}, "videoCodecs": []string{"h264"}, "audioCodecs": []string{"aac"}, "hls": false}

func TestPlayUsesPrefs(t *testing.T) {
	a := newApp(t)
	a.play.src = source()
	a.w.prefs = watching.Prefs{SubtitleLang: "eng", AudioLang: "bul"}
	w, out := a.postJSON(t, "/play/"+vid, map[string]any{"caps": browserCaps, "start": 12.5})
	require.Equal(t, 200, w.Code)
	require.NotEmpty(t, a.play.calls)
	last := a.play.calls[len(a.play.calls)-1]
	require.NotNil(t, last.aud)
	assert.Equal(t, 2, *last.aud, "Bulgarian audio needs a new stream")
	assert.Nil(t, last.sub, "text subtitles come as their own file")
	assert.Equal(t, 12.5, last.start)
	assert.Equal(t, jellyfin.Caps{Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}}, last.caps)
	assert.Equal(t, 3.0, out["sub"])
	assert.Equal(t, 2.0, out["audio"])
	assert.True(t, strings.HasPrefix(out["url"].(string), "/stream/videos/"))
	assert.NotContains(t, out["url"], "api_key")
	subs := out["subtitles"].([]any)
	require.Len(t, subs, 2)
	assert.Equal(t, "/stream/Videos/"+vid+"/ms1/Subtitles/3/0/Stream.vtt", subs[0].(map[string]any)["url"])
	assert.Equal(t, true, subs[1].(map[string]any)["image"])
	assert.Nil(t, subs[1].(map[string]any)["url"])
	assert.Equal(t, []string{"start " + vid + " ms1 ps1 12"}, a.play.reports)
}

func TestPlayImagePrefBurnsIn(t *testing.T) {
	a := newApp(t)
	a.play.src = source()
	a.w.prefs = watching.Prefs{SubtitleLang: "bul"}
	_, out := a.postJSON(t, "/play/"+vid, map[string]any{"caps": browserCaps})
	last := a.play.calls[len(a.play.calls)-1]
	require.NotNil(t, last.sub)
	assert.Equal(t, 4, *last.sub)
	assert.Equal(t, 4.0, out["sub"])
}

func TestPlayExplicitTrack(t *testing.T) {
	a := newApp(t)
	a.play.src = source()
	a.w.prefs = watching.Prefs{SubtitleLang: "eng", AudioLang: "bul"}
	_, out := a.postJSON(t, "/play/"+vid, map[string]any{"caps": browserCaps, "sub": 4, "audio": nil, "start": 30})
	require.Len(t, a.play.calls, 1, "explicit choices: one request")
	c := a.play.calls[0]
	require.NotNil(t, c.sub)
	assert.Equal(t, 4, *c.sub)
	assert.Nil(t, c.aud)
	assert.Equal(t, 4.0, out["sub"])
}

func TestPlayResponseNoToken(t *testing.T) {
	a := newApp(t)
	src := source()
	src.URL = "/videos/" + vid + "/master.m3u8?api_key=SECRET-TOKEN&x=1"
	a.play.src = src
	w, _ := a.postJSON(t, "/play/"+vid, map[string]any{"caps": browserCaps})
	assert.NotContains(t, w.Body.String(), "SECRET-TOKEN")
}

func TestPlayBadRequests(t *testing.T) {
	a := newApp(t)
	w, _ := a.postJSON(t, "/play/nothex", map[string]any{"caps": browserCaps})
	assert.Equal(t, 404, w.Code)
	a.play.err = errDown
	w, out := a.postJSON(t, "/play/"+vid, map[string]any{"caps": browserCaps})
	assert.Equal(t, 502, w.Code)
	assert.Equal(t, "This video can't be played right now.", out["error"])
}

func TestProgressSavesAndReports(t *testing.T) {
	a := newApp(t)
	w, _ := a.post(t, "/progress", url.Values{"item": {vid}, "series": {""}, "part": {"1"}, "position": {"61.5"},
		"durations": {"100,200"}, "playSessionId": {"ps1"}, "mediaSourceId": {"ms1"}, "event": {"stop"}})
	assert.Equal(t, 204, w.Code)
	require.Len(t, a.w.saved, 1)
	assert.Equal(t, watching.SaveInput{ItemID: vid, Part: 1, Position: 61.5, PartDurations: []float64{100, 200}}, a.w.saved[0])
	assert.Equal(t, []string{"stopped " + vid + " ms1 ps1 62"}, a.play.reports)

	a.post(t, "/progress", url.Values{"item": {vid}, "part": {"0"}, "position": {"5"}, "durations": {"100"},
		"playSessionId": {"ps1"}, "mediaSourceId": {"ms1"}, "event": {"progress"}})
	assert.Equal(t, "progress "+vid+" ms1 ps1 5", a.play.reports[1])

	for _, bad := range []url.Values{
		{"item": {"x"}, "part": {"0"}, "position": {"5"}, "durations": {"100"}},
		{"item": {vid}, "part": {"0"}, "position": {"abc"}, "durations": {"100"}},
		{"item": {vid}, "part": {"0"}, "position": {"5"}, "durations": {"a,b"}},
		{"item": {vid}, "part": {"0"}, "position": {"NaN"}, "durations": {"100"}},
		{"item": {vid}, "part": {"0"}, "position": {"5"}, "durations": {"Inf"}},
		{"item": {vid}, "part": {"0"}, "position": {"-1"}, "durations": {"100"}},
	} {
		w, _ := a.post(t, "/progress", bad)
		assert.Equal(t, 400, w.Code, bad.Encode())
	}
}

func TestPrefsPartial(t *testing.T) {
	a := newApp(t)
	w, _ := a.post(t, "/prefs", url.Values{"subtitle_lang": {"off"}})
	assert.Equal(t, 204, w.Code)
	require.NotNil(t, a.w.setSub)
	assert.Equal(t, "off", *a.w.setSub)
	assert.Nil(t, a.w.setAud, "audio kept")
}
