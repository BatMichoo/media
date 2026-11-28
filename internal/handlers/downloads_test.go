package handlers

import (
	"math"
	"net/url"
	"strings"
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

func TestOrderToggle(t *testing.T) {
	a := newApp(t)
	a.dl.list = []downloads.Download{
		live(downloads.Download{Hash: "a1", Title: "A", InOrder: true, Seeders: -1}),
		live(downloads.Download{Hash: "b2", Title: "B", InOrder: false, Seeders: -1}),
		live(downloads.Download{Hash: "c3", Title: "C", State: "filed", Seeders: -1}),
	}
	_, doc := a.get(t, "/downloads")
	rows := doc.Find(".download")
	on := rows.Eq(0).Find(".download-actions button.order")
	require.Equal(t, 1, on.Length())
	post, _ := on.Attr("hx-post")
	assert.Equal(t, "/downloads/a1/any-order", post, "pressing turns it off")
	pressed, _ := on.Attr("aria-pressed")
	assert.Equal(t, "true", pressed)
	assert.Contains(t, on.Text(), "In order")
	off := rows.Eq(1).Find(".download-actions button.order")
	post, _ = off.Attr("hx-post")
	assert.Equal(t, "/downloads/b2/in-order", post)
	pressed, _ = off.Attr("aria-pressed")
	assert.Equal(t, "false", pressed)
	assert.Equal(t, 0, rows.Eq(2).Find("button.order").Length(), "only while downloading")

	_, bg := a.get(t, "/downloads", "Cookie", i18n.CookieName+"=bg")
	assert.NotContains(t, bg.Find("button.order").First().Text(), "In order")
}

func TestPostInOrder(t *testing.T) {
	a := newApp(t)
	a.dl.list = []downloads.Download{{Hash: "a1", Kind: "movie", Title: "Dune", Year: 2021, State: "downloading"}}
	w, doc := a.post(t, "/downloads/a1/in-order", url.Values{}, "HX-Request", "true")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, 1, doc.Find("#downloads-list").Length())
	assert.Equal(t, []string{"in-order a1"}, a.dl.paused)
	w, _ = a.post(t, "/downloads/zz/in-order", url.Values{}, "HX-Request", "true")
	assert.Equal(t, 404, w.Code)
}

func TestTrackerErrorShownOnce(t *testing.T) {
	a := newApp(t)
	a.dl.list = []downloads.Download{live(downloads.Download{Hash: "a1", Title: "A", Stuck: "tracker",
		TrackerError: "Connection failed", Reason: "Connection failed", Seeders: -1})}
	_, doc := a.get(t, "/downloads")
	assert.Equal(t, 1, strings.Count(doc.Find(".download").Text(), "Connection failed"))
}

func TestStatsPlurals(t *testing.T) {
	row := func(l string, d downloads.Download) downloadRow { return downloadRow{Download: d, Lang: l} }
	assert.Equal(t, "1 peer, 0 sending", row("en", downloads.Download{Peers: 1, Seeders: -1}).PeersText())
	assert.Equal(t, "2 peers, 1 sending", row("en", downloads.Download{Peers: 2, PeersSending: 1, Seeders: -1}).PeersText())
	assert.Equal(t, "1 of 1 peer, 1 sending", row("en", downloads.Download{Peers: 1, PeersSending: 1, Seeders: 1}).PeersText())
	assert.Equal(t, "No peers connected (the tracker sees 1 seeder)", row("en", downloads.Download{Stuck: "no_peers", Seeders: 1}).StuckText())
	assert.Equal(t, "1 peer connected, but it isn't sending", row("en", downloads.Download{Stuck: "in_order", Peers: 1}).StuckText())
	assert.Equal(t, "3 peers connected, but none sending", row("en", downloads.Download{Stuck: "in_order", Peers: 3}).StuckText())
	assert.Contains(t, row("bg", downloads.Download{Peers: 1, Seeders: -1}).PeersText(), "1 пиър,")
	assert.Contains(t, row("bg", downloads.Download{Stuck: "no_peers", Seeders: 1}).StuckText(), "1 сийдър)")
}

func TestRatioInfinite(t *testing.T) {
	d := downloadRow{Download: downloads.Download{Ratio: math.Inf(1)}, Lang: "en"}
	assert.Contains(t, d.UpText(), "ratio ∞")
}
