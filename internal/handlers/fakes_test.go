package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/BatMichoo/media/internal/downloads"
	"github.com/BatMichoo/media/internal/jellyfin"
	"github.com/BatMichoo/media/internal/model"
	"github.com/BatMichoo/media/internal/watching"
	"github.com/PuerkitoBio/goquery"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeCatalog struct {
	ready    bool
	fail     bool
	latest   map[string][]jellyfin.Item
	items    map[string]jellyfin.Item
	seasons  map[string][]jellyfin.Item
	episodes map[string][]jellyfin.Item
	parts    map[string][]jellyfin.Item
	extras   map[string][]jellyfin.Item
}

func newCatalog() *fakeCatalog {
	return &fakeCatalog{ready: true, latest: map[string][]jellyfin.Item{}, items: map[string]jellyfin.Item{},
		seasons: map[string][]jellyfin.Item{}, episodes: map[string][]jellyfin.Item{},
		parts: map[string][]jellyfin.Item{}, extras: map[string][]jellyfin.Item{}}
}

var errDown = errors.New("jellyfin down")

func (c *fakeCatalog) Ready() bool { return c.ready }
func (c *fakeCatalog) Latest(_ context.Context, kind string, limit int) ([]jellyfin.Item, error) {
	if c.fail {
		return nil, errDown
	}
	l := c.latest[kind]
	if limit > 0 && len(l) > limit {
		l = l[:limit]
	}
	return l, nil
}
func (c *fakeCatalog) Item(_ context.Context, id string) (jellyfin.Item, error) {
	if c.fail {
		return jellyfin.Item{}, errDown
	}
	it, ok := c.items[id]
	if !ok {
		return it, fmt.Errorf("%s: %w", id, jellyfin.ErrNotFound)
	}
	return it, nil
}
func (c *fakeCatalog) Seasons(_ context.Context, id string) ([]jellyfin.Item, error) {
	return c.seasons[id], nil
}
func (c *fakeCatalog) Episodes(_ context.Context, id string) ([]jellyfin.Item, error) {
	return c.episodes[id], nil
}
func (c *fakeCatalog) Parts(_ context.Context, id string) ([]jellyfin.Item, error) {
	if p, ok := c.parts[id]; ok {
		return p, nil
	}
	it, err := c.Item(context.Background(), id)
	return []jellyfin.Item{it}, err
}
func (c *fakeCatalog) Extras(_ context.Context, id string) ([]jellyfin.Item, error) {
	return c.extras[id], nil
}

type removeCall struct {
	hash        string
	deleteFiles bool
}

type fakeDownloads struct {
	list      []downloads.Download
	listErr   error
	added     []downloads.AddInput
	addErr    error
	removed   []removeCall
	edited    []string
	editErr   error
	addedUser model.User
	paused    []string // "pause h" or "resume h", in order
	libSubs   []libSubCall
	libSubErr error
}

type libSubCall struct {
	Video, Kind string
	Upload      downloads.Upload
}

func (f *fakeDownloads) LibrarySubtitle(_ context.Context, video, kind string, up downloads.Upload) error {
	if f.libSubErr != nil {
		return f.libSubErr
	}
	f.libSubs = append(f.libSubs, libSubCall{video, kind, up})
	return nil
}

func (f *fakeDownloads) Pause(_ context.Context, hash string) error {
	return f.pauseCall("pause", hash)
}
func (f *fakeDownloads) Resume(_ context.Context, hash string) error {
	return f.pauseCall("resume", hash)
}
func (f *fakeDownloads) pauseCall(what, hash string) error {
	for _, d := range f.list {
		if d.Hash == hash {
			f.paused = append(f.paused, what+" "+hash)
			return nil
		}
	}
	return downloads.ErrNotFound
}

func (f *fakeDownloads) Add(_ context.Context, u model.User, in downloads.AddInput) (downloads.Download, bool, error) {
	if f.addErr != nil {
		return downloads.Download{}, false, f.addErr
	}
	f.added = append(f.added, in)
	f.addedUser = u
	return downloads.Download{Hash: "h1", Title: in.Title}, false, nil
}
func (f *fakeDownloads) List(context.Context) ([]downloads.Download, error) { return f.list, f.listErr }
func (f *fakeDownloads) Remove(_ context.Context, hash string, del bool) error {
	f.removed = append(f.removed, removeCall{hash, del})
	return nil
}
func (f *fakeDownloads) Edit(_ context.Context, hash, kind, title string, year int) error {
	if f.editErr != nil {
		return f.editErr
	}
	f.edited = append(f.edited, fmt.Sprintf("%s %s %s %d", hash, kind, title, year))
	return nil
}

type fakeWatching struct {
	cont   []watching.Entry
	prog   map[string]watching.Progress
	saved  []watching.SaveInput
	prefs  watching.Prefs
	setSub *string
	setAud *string
}

func (f *fakeWatching) Continue(context.Context, string, int) ([]watching.Entry, error) {
	return f.cont, nil
}
func (f *fakeWatching) Get(_ context.Context, _ string, id string) (watching.Progress, bool, error) {
	p, ok := f.prog[id]
	return p, ok, nil
}
func (f *fakeWatching) Many(_ context.Context, _ string, ids []string) (map[string]watching.Progress, error) {
	out := map[string]watching.Progress{}
	for _, id := range ids {
		if p, ok := f.prog[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}
func (f *fakeWatching) Save(_ context.Context, _ string, in watching.SaveInput) (watching.Progress, error) {
	f.saved = append(f.saved, in)
	return watching.Progress{}, nil
}
func (f *fakeWatching) Prefs(context.Context, string) (watching.Prefs, error) { return f.prefs, nil }
func (f *fakeWatching) SetPrefs(_ context.Context, _ string, sub, aud *string) error {
	f.setSub, f.setAud = sub, aud
	return nil
}

type app struct {
	cat  *fakeCatalog
	dl   *fakeDownloads
	w    *fakeWatching
	play *fakePlayer
	lib  *fakeLibrary
	link error
	r    *gin.Engine
}

func newApp(t *testing.T) *app {
	t.Helper()
	a := &app{cat: newCatalog(), dl: &fakeDownloads{}, w: &fakeWatching{prog: map[string]watching.Progress{}}, play: &fakePlayer{}, lib: &fakeLibrary{}}
	r, err := NewWebRouter(Deps{
		DB: pinger{}, Catalog: a.cat, Downloads: a.dl, Watching: a.w, Player: a.play, Library: a.lib,
		Upstream:  &fakeUpstream{reply: func(string) *http.Response { return resp(200, nil, "") }},
		LanDomain: "homeserver.lan", AuthURL: "https://auth.homeserver.lan", HomeServerURL: "https://homeserver.lan/",
		LinkProblem: func() error { return a.link },
	})
	require.NoError(t, err)
	a.r = r
	return a
}

const origin = "https://media.homeserver.lan"

func (a *app) get(t *testing.T, url string, headers ...string) (*httptest.ResponseRecorder, *goquery.Document) {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	req.Header.Set("X-User-Id", testUser)
	req.Header.Set("X-User-Name", "Alice")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	a.r.ServeHTTP(w, req)
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(w.Body.String()))
	require.NoError(t, err)
	return w, doc
}

func (a *app) post(t *testing.T, url string, form url.Values, headers ...string) (*httptest.ResponseRecorder, *goquery.Document) {
	t.Helper()
	req := httptest.NewRequest("POST", url, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Id", testUser)
	req.Header.Set("Origin", origin)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	a.r.ServeHTTP(w, req)
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(w.Body.String()))
	require.NoError(t, err)
	return w, doc
}

// id makes a 32-hex Jellyfin id from a short name.
func id(s string) string {
	return fmt.Sprintf("%032x", []byte(s))[:32]
}

type fakePlayer struct {
	src     jellyfin.Source
	err     error
	calls   []playCall
	reports []string
}

type playCall struct {
	item     string
	caps     jellyfin.Caps
	aud, sub *int
	start    float64
}

func (p *fakePlayer) Play(_ context.Context, item string, caps jellyfin.Caps, aud, sub *int, start float64) (jellyfin.Source, error) {
	p.calls = append(p.calls, playCall{item, caps, aud, sub, start})
	return p.src, p.err
}

func (p *fakePlayer) Report(_ context.Context, event, item, ms, ps string, pos float64) error {
	p.reports = append(p.reports, fmt.Sprintf("%s %s %s %s %.0f", event, item, ms, ps, pos))
	return nil
}
