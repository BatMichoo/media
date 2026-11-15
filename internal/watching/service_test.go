package watching

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/BatMichoo/media/internal/db/dbtest"
	"github.com/BatMichoo/media/internal/jellyfin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCatalog struct {
	items    map[string]jellyfin.Item
	episodes map[string][]jellyfin.Item
}

func (c *fakeCatalog) Item(_ context.Context, id string) (jellyfin.Item, error) {
	it, ok := c.items[id]
	if !ok {
		return jellyfin.Item{}, fmt.Errorf("item %s: %w", id, jellyfin.ErrNotFound)
	}
	return it, nil
}

func (c *fakeCatalog) Episodes(_ context.Context, seriesID string) ([]jellyfin.Item, error) {
	return c.episodes[seriesID], nil
}

const user = "6f1c2d3e-0000-4000-8000-000000000001"

type env struct {
	s   *Service
	cat *fakeCatalog
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{cat: &fakeCatalog{items: map[string]jellyfin.Item{}, episodes: map[string][]jellyfin.Item{}},
		now: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	e.s = New(dbtest.New(t), e.cat)
	e.s.Now = func() time.Time { return e.now }
	return e
}

// tick moves the clock so saves have distinct times.
func (e *env) save(t *testing.T, in SaveInput) Progress {
	t.Helper()
	e.now = e.now.Add(time.Minute)
	p, err := e.s.Save(context.Background(), user, in)
	require.NoError(t, err)
	return p
}

func (e *env) movie(id string) {
	e.cat.items[id] = jellyfin.Item{ID: id, Type: "Movie", Name: id}
}

func (e *env) series(id string, n int) {
	var eps []jellyfin.Item
	for i := 1; i <= n; i++ {
		ep := jellyfin.Item{ID: fmt.Sprintf("%s-e%d", id, i), Type: "Episode", SeriesID: id, ParentIndex: 1, Index: i}
		eps = append(eps, ep)
		e.cat.items[ep.ID] = ep
	}
	e.cat.episodes[id] = eps
}

func ids(entries []Entry) []string {
	var out []string
	for _, en := range entries {
		out = append(out, en.Item.ID)
	}
	return out
}

func TestFinishedAt90(t *testing.T) {
	e := newEnv(t)
	assert.False(t, e.save(t, SaveInput{ItemID: "m", Position: 89, PartDurations: []float64{100}}).Finished)
	assert.True(t, e.save(t, SaveInput{ItemID: "m", Position: 90, PartDurations: []float64{100}}).Finished)
	p, ok, err := e.s.Get(context.Background(), user, "m")
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, p.Finished)
	assert.Equal(t, 100.0, p.Duration)
}

func TestMultiPartFinished(t *testing.T) {
	e := newEnv(t)
	parts := []float64{3000, 3000}
	assert.False(t, e.save(t, SaveInput{ItemID: "m", Part: 0, Position: 2999, PartDurations: parts}).Finished)
	p := e.save(t, SaveInput{ItemID: "m", Part: 1, Position: 2400, PartDurations: parts})
	assert.True(t, p.Finished)
	assert.Equal(t, 6000.0, p.Duration)
	assert.Equal(t, 1, p.Part)
	assert.Equal(t, 5400.0, p.Watched)
	assert.InDelta(t, 0.9, Fraction(p), 1e-9)
	got, _, err := e.s.Get(context.Background(), user, "m")
	require.NoError(t, err)
	assert.Equal(t, 5400.0, got.Watched, "stored")
}

func TestSaveRejectsBadPart(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.Save(context.Background(), user, SaveInput{ItemID: "m", Part: 2, Position: 1, PartDurations: []float64{10, 10}})
	assert.Error(t, err)
	_, err = e.s.Save(context.Background(), user, SaveInput{ItemID: "m", Position: -1, PartDurations: []float64{10}})
	assert.Error(t, err)
}

func TestRewatchUnfinishes(t *testing.T) {
	e := newEnv(t)
	e.save(t, SaveInput{ItemID: "m", Position: 95, PartDurations: []float64{100}})
	assert.False(t, e.save(t, SaveInput{ItemID: "m", Position: 10, PartDurations: []float64{100}}).Finished)
}

func TestContinueMovies(t *testing.T) {
	e := newEnv(t)
	e.movie("a")
	e.movie("b")
	e.movie("c")
	e.save(t, SaveInput{ItemID: "a", Position: 10, PartDurations: []float64{100}})
	e.save(t, SaveInput{ItemID: "b", Position: 95, PartDurations: []float64{100}})
	e.save(t, SaveInput{ItemID: "c", Position: 20, PartDurations: []float64{100}})
	got, err := e.s.Continue(context.Background(), user, 12)
	require.NoError(t, err)
	assert.Equal(t, []string{"c", "a"}, ids(got))
	require.NotNil(t, got[0].Progress)
	assert.Equal(t, 20.0, got[0].Progress.Position)
}

func TestContinueNextEpisode(t *testing.T) {
	e := newEnv(t)
	e.series("s", 3)
	e.save(t, SaveInput{ItemID: "s-e1", SeriesID: "s", Position: 95, PartDurations: []float64{100}})
	got, err := e.s.Continue(context.Background(), user, 12)
	require.NoError(t, err)
	require.Equal(t, []string{"s-e2"}, ids(got))
	assert.Nil(t, got[0].Progress)
}

func TestContinueNextAlreadyWatched(t *testing.T) {
	e := newEnv(t)
	e.series("s", 3)
	e.save(t, SaveInput{ItemID: "s-e2", SeriesID: "s", Position: 95, PartDurations: []float64{100}})
	e.save(t, SaveInput{ItemID: "s-e1", SeriesID: "s", Position: 95, PartDurations: []float64{100}}) // rewatched e1 last
	got, err := e.s.Continue(context.Background(), user, 12)
	require.NoError(t, err)
	assert.Empty(t, got, "e2 is finished already")
}

func TestContinueSeriesDone(t *testing.T) {
	e := newEnv(t)
	e.series("s", 2)
	e.save(t, SaveInput{ItemID: "s-e2", SeriesID: "s", Position: 95, PartDurations: []float64{100}})
	got, err := e.s.Continue(context.Background(), user, 12)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestContinueOneRowPerSeries(t *testing.T) {
	e := newEnv(t)
	e.series("s", 3)
	e.save(t, SaveInput{ItemID: "s-e1", SeriesID: "s", Position: 95, PartDurations: []float64{100}})
	e.save(t, SaveInput{ItemID: "s-e3", SeriesID: "s", Position: 30, PartDurations: []float64{100}})
	got, err := e.s.Continue(context.Background(), user, 12)
	require.NoError(t, err)
	assert.Equal(t, []string{"s-e3"}, ids(got))
}

func TestContinueSkipsDeleted(t *testing.T) {
	e := newEnv(t)
	e.movie("a")
	e.save(t, SaveInput{ItemID: "a", Position: 10, PartDurations: []float64{100}})
	e.save(t, SaveInput{ItemID: "gone", Position: 10, PartDurations: []float64{100}})
	got, err := e.s.Continue(context.Background(), user, 12)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, ids(got))
}

func TestContinueOtherUsers(t *testing.T) {
	e := newEnv(t)
	e.movie("a")
	_, err := e.s.Save(context.Background(), "6f1c2d3e-0000-4000-8000-000000000002", SaveInput{ItemID: "a", Position: 10, PartDurations: []float64{100}})
	require.NoError(t, err)
	got, err := e.s.Continue(context.Background(), user, 12)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestContinueLimit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 15; i++ {
		id := fmt.Sprintf("m%d", i)
		e.movie(id)
		e.save(t, SaveInput{ItemID: id, Position: 10, PartDurations: []float64{100}})
	}
	got, err := e.s.Continue(context.Background(), user, 12)
	require.NoError(t, err)
	assert.Len(t, got, 12)
	assert.Equal(t, "m14", got[0].Item.ID)
}

func TestMany(t *testing.T) {
	e := newEnv(t)
	e.save(t, SaveInput{ItemID: "a", Position: 10, PartDurations: []float64{100}})
	got, err := e.s.Many(context.Background(), user, []string{"a", "b"})
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, 10.0, got["a"].Position)
}

func TestPickTracks(t *testing.T) {
	subs := []jellyfin.Track{{Index: 3, Lang: "bul"}, {Index: 4, Lang: "eng"}}
	audio := []jellyfin.Track{{Index: 1, Lang: "eng"}, {Index: 2, Lang: "bul"}}

	s, a := PickTracks(Prefs{SubtitleLang: "eng", AudioLang: "bul"}, subs, audio)
	require.NotNil(t, s)
	require.NotNil(t, a)
	assert.Equal(t, 4, *s)
	assert.Equal(t, 2, *a)

	s, a = PickTracks(Prefs{SubtitleLang: "off"}, subs, audio)
	assert.Nil(t, s)
	assert.Nil(t, a)

	s, _ = PickTracks(Prefs{SubtitleLang: "fre"}, subs, audio)
	assert.Nil(t, s, "no matching track")
}

func TestSetPrefsPartial(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p, err := e.s.Prefs(ctx, user)
	require.NoError(t, err)
	assert.Equal(t, Prefs{}, p)
	eng, bul := "eng", "bul"
	require.NoError(t, e.s.SetPrefs(ctx, user, &eng, &bul))
	off := "off"
	require.NoError(t, e.s.SetPrefs(ctx, user, &off, nil))
	p, err = e.s.Prefs(ctx, user)
	require.NoError(t, err)
	assert.Equal(t, Prefs{SubtitleLang: "off", AudioLang: "bul"}, p)
}
