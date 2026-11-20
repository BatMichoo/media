package jellyfin

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const ticksPerSecond = 10_000_000

// Item is a movie, series, season, episode or extra.
type Item struct {
	ID, Type, Name, Path, Overview, SeriesID string
	SeriesName                               string // Jellyfin's, for episodes
	Year, ParentIndex, Index                 int    // season and episode numbers
	RunTimeSeconds                           float64
	PartCount                                int
	DateCreated                              time.Time
	// Which pictures Jellyfin has: the item's own poster (an episode's is
	// a frame from the video) and backdrop, and an episode's series poster.
	HasPrimary, HasBackdrop, HasSeriesPrimary bool
}

// DisplayTitle is the title the person typed when adding it (the folder
// name) for movies and series, else Jellyfin's name.
func (i Item) DisplayTitle() string {
	if i.Path == "" {
		return i.Name
	}
	switch i.Type {
	case "Movie":
		return filepath.Base(filepath.Dir(i.Path))
	case "Series":
		return filepath.Base(i.Path)
	}
	return i.Name
}

// ShowTitle is an episode's series title as typed when adding (the folder
// under Shows/), else Jellyfin's series name; "" for anything else.
func (i Item) ShowTitle() string {
	if i.Type != "Episode" {
		return ""
	}
	p := filepath.ToSlash(i.Path)
	if _, rest, ok := strings.Cut(p, "/library/Shows/"); ok {
		if name, _, ok := strings.Cut(rest, "/"); ok && name != "" {
			return name
		}
	}
	return i.SeriesName
}

type apiItem struct {
	Id, Type, Name, Path, Overview, SeriesId string
	SeriesName                               string
	ProductionYear                           int
	ParentIndexNumber, IndexNumber           int
	RunTimeTicks                             int64
	PartCount                                int
	DateCreated                              time.Time
	ImageTags                                map[string]string
	BackdropImageTags                        []string
	SeriesPrimaryImageTag                    string
}

func (a apiItem) item() Item {
	return Item{
		ID: a.Id, Type: a.Type, Name: a.Name, Path: a.Path, Overview: a.Overview, SeriesID: a.SeriesId, SeriesName: a.SeriesName,
		Year: a.ProductionYear, ParentIndex: a.ParentIndexNumber, Index: a.IndexNumber,
		RunTimeSeconds: float64(a.RunTimeTicks) / ticksPerSecond, PartCount: a.PartCount,
		DateCreated: a.DateCreated,
		HasPrimary:  a.ImageTags["Primary"] != "", HasBackdrop: len(a.BackdropImageTags) > 0,
		HasSeriesPrimary: a.SeriesPrimaryImageTag != "",
	}
}

type itemList struct{ Items []apiItem }

func items(l []apiItem) []Item {
	out := make([]Item, 0, len(l))
	for _, a := range l {
		out = append(out, a.item())
	}
	return out
}

const fields = "Path,Overview,DateCreated"

// Latest lists movies ("Movie") or series ("Series"), newest added first.
// limit 0 means all.
func (c *Client) Latest(ctx context.Context, kind string, limit int) ([]Item, error) {
	q := url.Values{
		"userId": {c.uid()}, "IncludeItemTypes": {kind}, "Recursive": {"true"},
		"SortBy": {"DateCreated"}, "SortOrder": {"Descending"}, "Fields": {fields},
	}
	if limit > 0 {
		q.Set("Limit", strconv.Itoa(limit))
	}
	var out itemList
	if err := c.get(ctx, "/Items", q, &out); err != nil {
		return nil, err
	}
	return items(out.Items), nil
}

// Item is one item by id; ErrNotFound when Jellyfin doesn't know it.
func (c *Client) Item(ctx context.Context, id string) (Item, error) {
	var a apiItem
	if err := c.get(ctx, "/Items/"+url.PathEscape(id), url.Values{"userId": {c.uid()}, "Fields": {fields}}, &a); err != nil {
		return Item{}, err
	}
	return a.item(), nil
}

// Seasons of a series, in order.
func (c *Client) Seasons(ctx context.Context, seriesID string) ([]Item, error) {
	var out itemList
	if err := c.get(ctx, "/Shows/"+url.PathEscape(seriesID)+"/Seasons", url.Values{"userId": {c.uid()}}, &out); err != nil {
		return nil, err
	}
	return items(out.Items), nil
}

// Episodes of a series, in Jellyfin's order (season, then episode).
func (c *Client) Episodes(ctx context.Context, seriesID string) ([]Item, error) {
	var out itemList
	q := url.Values{"userId": {c.uid()}, "Fields": {fields}}
	if err := c.get(ctx, "/Shows/"+url.PathEscape(seriesID)+"/Episodes", q, &out); err != nil {
		return nil, err
	}
	return items(out.Items), nil
}

// Parts of a movie in play order: the movie itself, then its additional
// parts (a multi-part movie).
func (c *Client) Parts(ctx context.Context, movieID string) ([]Item, error) {
	first, err := c.Item(ctx, movieID)
	if err != nil {
		return nil, err
	}
	parts := []Item{first}
	if first.PartCount < 2 {
		return parts, nil
	}
	var out itemList
	if err := c.get(ctx, "/Videos/"+url.PathEscape(movieID)+"/AdditionalParts", url.Values{"userId": {c.uid()}}, &out); err != nil {
		return nil, err
	}
	return append(parts, items(out.Items)...), nil
}

// Extras of a movie (files under extras/).
func (c *Client) Extras(ctx context.Context, movieID string) ([]Item, error) {
	var out []apiItem
	if err := c.get(ctx, "/Items/"+url.PathEscape(movieID)+"/SpecialFeatures", url.Values{"userId": {c.uid()}}, &out); err != nil {
		return nil, err
	}
	return items(out), nil
}

// RefreshItem asks Jellyfin to look at one item's files again (e.g. a
// subtitle added next to it).
func (c *Client) RefreshItem(ctx context.Context, id string) error {
	return c.post(ctx, "/Items/"+url.PathEscape(id)+"/Refresh",
		url.Values{"MetadataRefreshMode": {"Default"}, "Recursive": {"false"}}, nil, nil)
}

// Refresh asks Jellyfin to rescan its libraries.
func (c *Client) Refresh(ctx context.Context) error {
	return c.send(ctx, http.MethodPost, "/Library/Refresh", nil, nil, true)
}
