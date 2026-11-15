package jellyfin

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loggedIn(t *testing.T) (*fake, *Client) {
	t.Helper()
	f, c := newFake(t)
	configured(f, bothLibraries(), goodEncoding())
	require.NoError(t, c.Setup(context.Background(), "/data/library"))
	return f, c
}

func TestDisplayTitle(t *testing.T) {
	assert.Equal(t, "Dune (2021)", Item{Type: "Movie", Name: "Dune: Part One",
		Path: "/data/library/Movies/Dune (2021)/Dune (2021) - part1.mkv"}.DisplayTitle())
	assert.Equal(t, "Severance", Item{Type: "Series", Name: "Severance (US)",
		Path: "/data/library/Shows/Severance"}.DisplayTitle())
	assert.Equal(t, "Good News About Hell", Item{Type: "Episode", Name: "Good News About Hell",
		Path: "/data/library/Shows/Severance/S01E01.mkv"}.DisplayTitle())
	assert.Equal(t, "Name", Item{Type: "Movie", Name: "Name"}.DisplayTitle(), "no path")
}

func TestLatest(t *testing.T) {
	f, c := loggedIn(t)
	f.json("GET /Items", map[string]any{"Items": []any{
		map[string]any{"Id": "m1", "Type": "Movie", "Name": "Dune", "Path": "/p/Dune (2021)/x.mkv",
			"ProductionYear": 2021, "RunTimeTicks": 90 * 60 * 10_000_000, "DateCreated": "2026-11-15T10:00:00.0000000Z",
			"Overview": "Spice."},
	}})
	items, err := c.Latest(context.Background(), "Movie", 12)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, Item{ID: "m1", Type: "Movie", Name: "Dune", Path: "/p/Dune (2021)/x.mkv", Year: 2021,
		RunTimeSeconds: 5400, Overview: "Spice.", DateCreated: items[0].DateCreated}, items[0])
	assert.Equal(t, 2026, items[0].DateCreated.Year())
	q := f.find("GET /Items").Query
	for _, want := range []string{"IncludeItemTypes=Movie", "Recursive=true", "SortBy=DateCreated",
		"SortOrder=Descending", "Limit=12", "userId=u1"} {
		assert.Contains(t, q, want)
	}
}

func TestEpisodesOrder(t *testing.T) {
	f, c := loggedIn(t)
	f.json("GET /Shows/s1/Episodes", map[string]any{"Items": []any{
		map[string]any{"Id": "e1", "Type": "Episode", "SeriesId": "s1", "ParentIndexNumber": 1, "IndexNumber": 1},
		map[string]any{"Id": "e2", "Type": "Episode", "SeriesId": "s1", "ParentIndexNumber": 1, "IndexNumber": 2},
	}})
	eps, err := c.Episodes(context.Background(), "s1")
	require.NoError(t, err)
	require.Len(t, eps, 2)
	assert.Equal(t, "e2", eps[1].ID)
	assert.Equal(t, 1, eps[1].ParentIndex)
	assert.Equal(t, 2, eps[1].Index)
	assert.Equal(t, "s1", eps[1].SeriesID)
}

func TestParts(t *testing.T) {
	f, c := loggedIn(t)
	f.json("GET /Items/m1", map[string]any{"Id": "m1", "Type": "Movie", "PartCount": 2, "RunTimeTicks": 30 * 10_000_000})
	f.json("GET /Videos/m1/AdditionalParts", map[string]any{"Items": []any{
		map[string]any{"Id": "m1b", "Type": "Movie", "RunTimeTicks": 40 * 10_000_000},
	}})
	parts, err := c.Parts(context.Background(), "m1")
	require.NoError(t, err)
	require.Len(t, parts, 2)
	assert.Equal(t, "m1", parts[0].ID)
	assert.Equal(t, "m1b", parts[1].ID)
	assert.Equal(t, 40.0, parts[1].RunTimeSeconds)
}

func TestExtras(t *testing.T) {
	f, c := loggedIn(t)
	f.json("GET /Items/m1/SpecialFeatures", []any{map[string]any{"Id": "x1", "Type": "Video", "Name": "Behind"}})
	ex, err := c.Extras(context.Background(), "m1")
	require.NoError(t, err)
	require.Len(t, ex, 1)
	assert.Equal(t, "x1", ex[0].ID)
}

func TestItemNotFound(t *testing.T) {
	_, c := loggedIn(t)
	_, err := c.Item(context.Background(), "missing")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRefresh(t *testing.T) {
	f, c := loggedIn(t)
	f.on("POST /Library/Refresh", func(*http.Request, map[string]any) (int, any) { return 204, nil })
	require.NoError(t, c.Refresh(context.Background()))
	assert.NotNil(t, f.find("POST /Library/Refresh"))
}
