package handlers

import (
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"

	"github.com/BatMichoo/media/internal/i18n"
	"github.com/BatMichoo/media/internal/jellyfin"
	"github.com/BatMichoo/media/internal/watching"
	"github.com/gin-gonic/gin"
)

// card is one poster: a movie, a series or an episode. Image is "" when
// Jellyfin has no picture; the card then shows its title on a tint.
type card struct {
	Href, Image, Title, Sub string
	Fraction                float64 // watched share, 0..1
	HasBar                  bool
	Wide                    bool // a 16:9 still instead of a 2:3 poster
	Hue                     int  // the tint behind (or instead of) the picture
}

func image(id, typ string, w int) string { return fmt.Sprintf("/images/%s/%s?w=%d", id, typ, w) }

func poster(id string) string { return image(id, "Primary", 300) }

// hue is a stable tint per title, so pictureless cards aren't all alike.
func hue(s string) int {
	h := fnv.New32a()
	h.Write([]byte(s))
	return int(h.Sum32() % 360)
}

// posterOf is the 2:3 poster for it: its own, or an episode's series'.
func posterOf(it jellyfin.Item) string {
	switch {
	case it.Type == jellyfin.TypeEpisode && it.HasSeriesPrimary && it.SeriesID != "":
		return poster(it.SeriesID)
	case it.Type != jellyfin.TypeEpisode && it.HasPrimary:
		return poster(it.ID)
	}
	return ""
}

// stillOf is a 16:9 picture for it: an episode's frame, a backdrop, else "".
func stillOf(it jellyfin.Item) string {
	switch {
	case it.Type == jellyfin.TypeEpisode && it.HasPrimary:
		return image(it.ID, "Primary", 480)
	case it.HasBackdrop:
		return image(it.ID, "Backdrop", 480)
	case it.Type == jellyfin.TypeVideo && it.HasPrimary: // an extra
		return image(it.ID, "Primary", 480)
	}
	return ""
}

func itemCard(it jellyfin.Item) card {
	c := card{Image: posterOf(it), Title: it.DisplayTitle()}
	switch it.Type {
	case jellyfin.TypeSeries:
		c.Href = "/shows/" + it.ID
	case jellyfin.TypeEpisode:
		c.Href = "/watch/" + it.ID
		c.Title = it.ShowTitle()
		c.Sub = fmt.Sprintf("S%d · E%d · %s", it.ParentIndex, it.Index, it.Name)
	default:
		c.Href = "/movies/" + it.ID
		if it.Year > 0 {
			c.Sub = fmt.Sprint(it.Year)
		}
	}
	c.Hue = hue(c.Title)
	return c
}

// wideCard is itemCard as a 16:9 still, falling back to the poster.
func wideCard(it jellyfin.Item) card {
	c := itemCard(it)
	c.Wide = true
	if s := stillOf(it); s != "" {
		c.Image = s
	}
	return c
}

type homeView struct {
	Lang                     string
	Unavailable              bool
	Continue, Movies, Shows  []card
	HasMoreMovies, MoreShows bool
}

const rowSize = 12

func (h *web) home(c *gin.Context) {
	ctx := c.Request.Context()
	v := homeView{Lang: lang(c)}
	err := func() error {
		if !h.d.Catalog.Ready() {
			return errors.New("jellyfin not set up yet")
		}
		cont, err := h.d.Watching.Continue(ctx, CurrentUser(c).ID, rowSize)
		if err != nil {
			return err
		}
		for _, e := range cont {
			cd := wideCard(e.Item)
			cd.Href = "/watch/" + e.Item.ID
			if e.Progress != nil {
				cd.Fraction, cd.HasBar = watching.Fraction(*e.Progress), true
			}
			v.Continue = append(v.Continue, cd)
		}
		movies, err := h.d.Catalog.Latest(ctx, jellyfin.TypeMovie, rowSize)
		if err != nil {
			return err
		}
		shows, err := h.d.Catalog.Latest(ctx, jellyfin.TypeSeries, rowSize)
		if err != nil {
			return err
		}
		for _, m := range movies {
			v.Movies = append(v.Movies, itemCard(m))
		}
		for _, s := range shows {
			v.Shows = append(v.Shows, itemCard(s))
		}
		return nil
	}()
	if err != nil {
		log.Printf("home: %v", err)
		v = homeView{Lang: v.Lang, Unavailable: true}
	}
	h.render(c, http.StatusOK, "media/home", "home.title", v)
}

type gridView struct {
	Lang        string
	Unavailable bool
	Heading     string
	Empty       string
	Cards       []card
}

func (h *web) grid(c *gin.Context, kind, headingKey, emptyKey string) {
	l := lang(c)
	v := gridView{Lang: l, Heading: i18n.T(l, headingKey), Empty: i18n.T(l, emptyKey)}
	items, err := []jellyfin.Item(nil), error(nil)
	if !h.d.Catalog.Ready() {
		err = errors.New("jellyfin not set up yet")
	} else {
		items, err = h.d.Catalog.Latest(c.Request.Context(), kind, 0)
	}
	if err != nil {
		log.Printf("%s: %v", kind, err)
		v.Unavailable = true
	}
	for _, it := range items {
		v.Cards = append(v.Cards, itemCard(it))
	}
	h.render(c, http.StatusOK, "media/grid", headingKey, v)
}

func (h *web) movies(c *gin.Context) { h.grid(c, jellyfin.TypeMovie, "home.movies", "empty.movies") }
func (h *web) shows(c *gin.Context)  { h.grid(c, jellyfin.TypeSeries, "home.shows", "empty.shows") }

type movieView struct {
	Lang        string
	Item        jellyfin.Item
	Title       string
	Image       string // "" = no poster
	Backdrop    string // "" = none
	Hue         int
	Resume      float64 // seconds; 0 = start
	Fraction    float64
	Extras      []card
	Unavailable bool
	Parts       int    // video files the movie is split into (1 = one)
	SubsAdded   bool   // a subtitle upload just worked
	SubsError   string // why a subtitle upload didn't, "" = it didn't fail
}

func (h *web) movie(c *gin.Context) {
	ctx := c.Request.Context()
	it, err := h.d.Catalog.Item(ctx, c.Param("id"))
	if isNotFound(err) || (err == nil && it.Type != jellyfin.TypeMovie && it.Type != jellyfin.TypeVideo) {
		h.notFound(c)
		return
	}
	if err != nil {
		h.unavailable(c, err)
		return
	}
	v := movieView{Lang: lang(c), Item: it, Title: it.DisplayTitle(), Parts: 1}
	v.SubsAdded, v.SubsError = subsNotice(c, v.Lang)
	if it.PartCount > 1 {
		if parts, err := h.d.Catalog.Parts(ctx, it.ID); err == nil && len(parts) > 1 {
			v.Parts = len(parts)
		}
	}
	v.Image, v.Backdrop, v.Hue = detailImages(it, v.Title)
	if p, ok, err := h.d.Watching.Get(ctx, CurrentUser(c).ID, it.ID); err == nil && ok && !p.Finished {
		v.Resume, v.Fraction = p.Watched, watching.Fraction(p)
	}
	if extras, err := h.d.Catalog.Extras(ctx, it.ID); err == nil {
		for _, x := range extras {
			v.Extras = append(v.Extras, card{Href: "/watch/" + x.ID, Image: stillOf(x), Title: x.Name, Wide: true, Hue: hue(x.Name)})
		}
	}
	h.renderTitled(c, http.StatusOK, "media/movie", v.Title, v)
}

// detailImages are a movie or series page's poster and backdrop ("" when
// Jellyfin has none) and its tint.
func detailImages(it jellyfin.Item, title string) (img, backdrop string, h int) {
	if it.HasPrimary {
		img = image(it.ID, "Primary", 600)
	}
	if it.HasBackdrop {
		backdrop = image(it.ID, "Backdrop", 1280)
	}
	return img, backdrop, hue(title)
}

type episodeView struct {
	ID, Name string
	Image    string // a frame from the episode, "" = none
	Number   int
	Watched  bool
	Fraction float64
	HasBar   bool
}

// episodeViewOf is an episode row with the person's progress (prog may
// be nil).
func episodeViewOf(e jellyfin.Item, prog map[string]watching.Progress) episodeView {
	ev := episodeView{ID: e.ID, Name: e.Name, Number: e.Index, Image: stillOf(e)}
	if p, ok := prog[e.ID]; ok {
		ev.Watched = p.Finished
		if !p.Finished {
			ev.Fraction, ev.HasBar = watching.Fraction(p), true
		}
	}
	return ev
}

type seasonView struct {
	Name     string
	Episodes []episodeView
}

type showView struct {
	Lang      string
	Item      jellyfin.Item
	Title     string
	Image     string // "" = no poster
	Backdrop  string // "" = none
	Hue       int
	Seasons   []seasonView
	Continue  string // the episode to play next, "" = none
	SubsAdded bool   // a subtitle upload just worked
	SubsError string // why a subtitle upload didn't, "" = it didn't fail
}

func (h *web) show(c *gin.Context) {
	ctx := c.Request.Context()
	l := lang(c)
	it, err := h.d.Catalog.Item(ctx, c.Param("id"))
	if isNotFound(err) || (err == nil && it.Type != jellyfin.TypeSeries) {
		h.notFound(c)
		return
	}
	if err != nil {
		h.unavailable(c, err)
		return
	}
	seasons, err := h.d.Catalog.Seasons(ctx, it.ID)
	if err != nil {
		h.unavailable(c, err)
		return
	}
	eps, err := h.d.Catalog.Episodes(ctx, it.ID)
	if err != nil {
		h.unavailable(c, err)
		return
	}
	ids := make([]string, 0, len(eps))
	for _, e := range eps {
		ids = append(ids, e.ID)
	}
	prog, err := h.d.Watching.Many(ctx, CurrentUser(c).ID, ids)
	if err != nil {
		log.Printf("show progress: %v", err)
	}
	names := map[int]string{}
	for _, s := range seasons {
		names[s.Index] = s.Name
	}
	v := showView{Lang: l, Item: it, Title: it.DisplayTitle()}
	v.SubsAdded, v.SubsError = subsNotice(c, l)
	v.Image, v.Backdrop, v.Hue = detailImages(it, v.Title)
	for _, e := range eps {
		if len(v.Seasons) == 0 || v.Seasons[len(v.Seasons)-1].Name != seasonName(l, names, e.ParentIndex) {
			v.Seasons = append(v.Seasons, seasonView{Name: seasonName(l, names, e.ParentIndex)})
		}
		s := &v.Seasons[len(v.Seasons)-1]
		s.Episodes = append(s.Episodes, episodeViewOf(e, prog))
	}
	h.renderTitled(c, http.StatusOK, "media/show", v.Title, v)
}

func seasonName(l string, names map[int]string, n int) string {
	if name := names[n]; name != "" {
		return name
	}
	if n == 0 {
		return i18n.T(l, "show.specials")
	}
	return fmt.Sprintf(i18n.T(l, "show.season"), n)
}

// unavailable is the page when Jellyfin can't answer.
func (h *web) unavailable(c *gin.Context, err error) {
	log.Printf("library: %v", err)
	h.render(c, http.StatusOK, "media/message", "app", message{Lang: lang(c), Text: i18n.T(lang(c), "unavailable")})
}
