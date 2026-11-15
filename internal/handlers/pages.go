package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/BatMichoo/media/internal/i18n"
	"github.com/BatMichoo/media/internal/jellyfin"
	"github.com/BatMichoo/media/internal/watching"
	"github.com/gin-gonic/gin"
)

var errNotFound = errors.New("not found")

func isJellyfinNotFound(err error) bool { return errors.Is(err, jellyfin.ErrNotFound) }

// card is one poster: a movie, a series or an episode.
type card struct {
	Href, Image, Title, Sub string
	Fraction                float64 // watched share, 0..1
	HasBar                  bool
}

func poster(id string) string { return "/images/" + id + "/Primary?w=300" }

func itemCard(it jellyfin.Item) card {
	c := card{Image: poster(it.ID), Title: it.DisplayTitle()}
	switch it.Type {
	case "Series":
		c.Href = "/shows/" + it.ID
	case "Episode":
		c.Href = "/watch/" + it.ID
		c.Title = it.ShowTitle()
		c.Sub = fmt.Sprintf("S%d · E%d · %s", it.ParentIndex, it.Index, it.Name)
		if it.SeriesID != "" {
			c.Image = poster(it.SeriesID)
		}
	default:
		c.Href = "/movies/" + it.ID
		if it.Year > 0 {
			c.Sub = fmt.Sprint(it.Year)
		}
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
			cd := itemCard(e.Item)
			cd.Href = "/watch/" + e.Item.ID
			if e.Progress != nil {
				cd.Fraction, cd.HasBar = watching.Fraction(*e.Progress), true
			}
			v.Continue = append(v.Continue, cd)
		}
		movies, err := h.d.Catalog.Latest(ctx, "Movie", rowSize)
		if err != nil {
			return err
		}
		shows, err := h.d.Catalog.Latest(ctx, "Series", rowSize)
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

func (h *web) movies(c *gin.Context) { h.grid(c, "Movie", "home.movies", "empty.movies") }
func (h *web) shows(c *gin.Context)  { h.grid(c, "Series", "home.shows", "empty.shows") }

type movieView struct {
	Lang        string
	Item        jellyfin.Item
	Title       string
	Image       string
	Resume      float64 // seconds; 0 = start
	Fraction    float64
	Extras      []card
	Unavailable bool
}

func (h *web) movie(c *gin.Context) {
	ctx := c.Request.Context()
	it, err := h.d.Catalog.Item(ctx, c.Param("id"))
	if isNotFound(err) || (err == nil && it.Type != "Movie" && it.Type != "Video") {
		h.notFound(c)
		return
	}
	if err != nil {
		h.unavailable(c, err)
		return
	}
	v := movieView{Lang: lang(c), Item: it, Title: it.DisplayTitle(), Image: "/images/" + it.ID + "/Primary?w=600"}
	if p, ok, err := h.d.Watching.Get(ctx, CurrentUser(c).ID, it.ID); err == nil && ok && !p.Finished {
		v.Resume, v.Fraction = p.Watched, watching.Fraction(p)
	}
	if extras, err := h.d.Catalog.Extras(ctx, it.ID); err == nil {
		for _, x := range extras {
			v.Extras = append(v.Extras, card{Href: "/watch/" + x.ID, Image: poster(x.ID), Title: x.Name})
		}
	}
	h.renderTitled(c, http.StatusOK, "media/movie", v.Title, v)
}

type episodeView struct {
	ID, Name string
	Number   int
	Watched  bool
	Fraction float64
	HasBar   bool
}

type seasonView struct {
	Name     string
	Episodes []episodeView
}

type showView struct {
	Lang     string
	Item     jellyfin.Item
	Title    string
	Image    string
	Seasons  []seasonView
	Continue string // the episode to play next, "" = none
}

func (h *web) show(c *gin.Context) {
	ctx := c.Request.Context()
	l := lang(c)
	it, err := h.d.Catalog.Item(ctx, c.Param("id"))
	if isNotFound(err) || (err == nil && it.Type != "Series") {
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
	v := showView{Lang: l, Item: it, Title: it.DisplayTitle(), Image: "/images/" + it.ID + "/Primary?w=600"}
	for _, e := range eps {
		if len(v.Seasons) == 0 || v.Seasons[len(v.Seasons)-1].Name != seasonName(l, names, e.ParentIndex) {
			v.Seasons = append(v.Seasons, seasonView{Name: seasonName(l, names, e.ParentIndex)})
		}
		ev := episodeView{ID: e.ID, Name: e.Name, Number: e.Index}
		if p, ok := prog[e.ID]; ok {
			ev.Watched = p.Finished
			if !p.Finished {
				ev.Fraction, ev.HasBar = watching.Fraction(p), true
			}
		}
		s := &v.Seasons[len(v.Seasons)-1]
		s.Episodes = append(s.Episodes, ev)
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
