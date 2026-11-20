package handlers

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BatMichoo/media/internal/downloads"
	"github.com/BatMichoo/media/internal/i18n"
)

func live(d downloads.Download) downloads.Download {
	d.Live, d.Kind, d.Year = true, "movie", 2021
	if d.State == "" {
		d.State = "downloading"
	}
	return d
}

func TestDownloadStatsLine(t *testing.T) {
	a := newApp(t)
	until := time.Date(2026, 11, 18, 9, 30, 0, 0, time.Local)
	a.dl.list = []downloads.Download{
		live(downloads.Download{Hash: "a1", Title: "Dune", RateDownload: 10, RateUpload: 410_000, Uploaded: 1_300_000_000,
			Ratio: 0.5, Peers: 3, PeersSending: 1, Seeders: 45}),
		live(downloads.Download{Hash: "b2", Title: "Heat", State: "filed", SeedingUntil: until, Peers: 2, Seeders: -1}),
		{Hash: "c3", Kind: "movie", Title: "Gone", Year: 2000, State: "downloading", Seeders: -1},
		live(downloads.Download{Hash: "d4", Title: "Done", State: "filed", SeedingUntil: until, SeedingDone: true}),
	}
	_, doc := a.get(t, "/downloads")
	rows := doc.Find(".download")
	line := rows.Eq(0).Find(".download-stats")
	require.Equal(t, 1, line.Length())
	assert.False(t, line.HasClass("stuck"))
	assert.Contains(t, line.Text(), "410 kB/s")
	assert.Contains(t, line.Text(), "1.3 GB up")
	assert.Contains(t, line.Text(), "ratio 0.50")
	assert.Contains(t, line.Text(), "3 of 45 peers, 1 sending")
	seed := rows.Eq(1).Find(".download-stats").Text()
	assert.Contains(t, seed, "2 peers")
	assert.NotContains(t, seed, " of ", "no seeder count known")
	assert.Equal(t, 0, rows.Eq(2).Find(".download-stats").Length(), "not in Transmission")
	assert.Equal(t, 0, rows.Eq(3).Find(".download-stats").Length(), "seeding over")
}

func TestStuckQueuedButton(t *testing.T) {
	a := newApp(t)
	a.dl.list = []downloads.Download{live(downloads.Download{Hash: "a1", Title: "Dune", Stuck: "queued", QueuePos: 2, Seeders: -1})}
	_, doc := a.get(t, "/downloads")
	stuck := doc.Find(".download-stats.stuck")
	require.Equal(t, 1, stuck.Length())
	assert.Contains(t, stuck.Text(), "#2")
	b := doc.Find("button.start-now")
	post, _ := b.Attr("hx-post")
	assert.Equal(t, "/downloads/a1/start-now", post)
	target, _ := b.Attr("hx-target")
	assert.Equal(t, "#downloads-list", target)
	assert.Equal(t, 0, doc.Find("button.any-order").Length())
}

func TestStuckInOrderButton(t *testing.T) {
	a := newApp(t)
	a.dl.list = []downloads.Download{live(downloads.Download{Hash: "a1", Title: "Dune", Stuck: "in_order", Peers: 3, Seeders: 45, InOrder: true})}
	_, doc := a.get(t, "/downloads")
	assert.Contains(t, doc.Find(".download-stats.stuck").Text(), "none sending")
	post, _ := doc.Find("button.any-order").Attr("hx-post")
	assert.Equal(t, "/downloads/a1/any-order", post)
	assert.Equal(t, 1, doc.Find(".any-order-note").Length())
}

func TestStuckReasonsText(t *testing.T) {
	a := newApp(t)
	a.dl.list = []downloads.Download{
		live(downloads.Download{Hash: "a1", Title: "A", Stuck: "tracker", TrackerError: "Connection failed", Seeders: -1}),
		live(downloads.Download{Hash: "b2", Title: "B", Stuck: "no_peers", Seeders: 45, PortClosed: true, Port: 51413}),
		live(downloads.Download{Hash: "c3", Title: "C", Stuck: "no_peers", Seeders: -1}),
		live(downloads.Download{Hash: "d4", Title: "D", Stuck: "not_sending", Peers: 3, Seeders: -1}),
	}
	_, doc := a.get(t, "/downloads")
	rows := doc.Find(".download")
	assert.Contains(t, rows.Eq(0).Find(".stuck").Text(), "Connection failed")
	b := rows.Eq(1).Find(".stuck").Text()
	assert.Contains(t, b, "No peers connected")
	assert.Contains(t, b, "45 seeders")
	assert.Contains(t, b, "51413")
	c := rows.Eq(2).Find(".stuck").Text()
	assert.NotContains(t, c, "seeders")
	assert.NotContains(t, c, "port")
	assert.Contains(t, rows.Eq(3).Find(".stuck").Text(), "none sending")
	assert.Equal(t, 0, doc.Find("button.any-order, button.start-now").Length())
}

func TestStuckTranslated(t *testing.T) {
	a := newApp(t)
	a.dl.list = []downloads.Download{live(downloads.Download{Hash: "a1", Title: "A", Stuck: "in_order", Peers: 3, Seeders: -1})}
	_, doc := a.get(t, "/downloads", "Cookie", i18n.CookieName+"=bg")
	assert.Contains(t, doc.Find(".download-stats").Text(), "никой не изпраща")
	assert.Contains(t, doc.Find("button.any-order").Text(), "произволен ред")
}

func TestPostStartNowAndAnyOrder(t *testing.T) {
	a := newApp(t)
	a.dl.list = []downloads.Download{{Hash: "a1", Kind: "movie", Title: "Dune", Year: 2021, State: "downloading"}}
	w, doc := a.post(t, "/downloads/a1/start-now", url.Values{}, "HX-Request", "true")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, 1, doc.Find("#downloads-list").Length(), "answers with the list")
	w, _ = a.post(t, "/downloads/a1/any-order", url.Values{}, "HX-Request", "true")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, []string{"start-now a1", "any-order a1"}, a.dl.paused)
	w, _ = a.post(t, "/downloads/zz/any-order", url.Values{}, "HX-Request", "true")
	assert.Equal(t, 404, w.Code)
}
