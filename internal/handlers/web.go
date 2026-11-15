package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BatMichoo/media/internal/i18n"
	"github.com/BatMichoo/media/internal/static"
	"github.com/BatMichoo/media/internal/templates"
	"github.com/BatMichoo/theme"
	"github.com/gin-gonic/gin"
)

type web struct {
	d   Deps
	set *template.Template
}

// NewWebRouter is the whole app: health check, pages, player, streams.
func NewWebRouter(d Deps) (*gin.Engine, error) {
	set, err := theme.AddTemplates(template.New("").Funcs(funcs))
	if err != nil {
		return nil, err
	}
	if set, err = set.ParseFS(templates.FS, "pages/*.html", "components/*.html"); err != nil {
		return nil, err
	}
	if d.LinkProblem == nil {
		d.LinkProblem = func() error { return nil }
	}
	h := &web{d: d, set: set}

	r := NewRouter(d)
	r.Use(OriginCheck(d.LanDomain))
	r.GET(theme.DefaultPrefix+"/*file", gin.WrapH(http.StripPrefix(theme.DefaultPrefix, theme.Handler())))
	r.GET("/static/media/*file", gin.WrapH(http.StripPrefix("/static/media", filesOnly(http.FS(static.FS)))))

	g := r.Group("/", RequireUser())
	RegisterStream(g, d.Upstream)
	g.GET("/", h.home)
	g.GET("/movies", h.movies)
	g.GET("/shows", h.shows)
	g.GET("/movies/:id", h.movie)
	g.GET("/shows/:id", h.show)
	g.GET("/add", h.getAdd)
	g.POST("/add", h.postAdd)
	g.GET("/downloads", h.downloads)
	g.GET("/downloads/list", h.downloadsList)
	g.GET("/downloads/:hash/edit", h.getEdit)
	g.POST("/downloads/:hash/edit", h.postEdit)
	g.GET("/downloads/:hash/remove", h.getRemove)
	g.POST("/downloads/:hash/remove", h.postRemove)
	g.POST("/lang", h.postLang)
	h.registerPlayer(g)
	return r, nil
}

var funcs = template.FuncMap{
	"t": i18n.T,
	// pct is a 0..1 fraction as a whole percentage.
	"pct":   func(f float64) int { return int(math.Round(f * 100)) },
	"clock": clock,
	// rate is bytes per second in words.
	"rate": func(b int64) string {
		switch {
		case b >= 1_000_000:
			return fmt.Sprintf("%.1f MB/s", float64(b)/1_000_000)
		case b >= 1_000:
			return fmt.Sprintf("%d kB/s", b/1_000)
		}
		return fmt.Sprintf("%d B/s", b)
	},
	"size":         sizeWords,
	"timeLeft":     timeLeft,
	"when":         func(t time.Time) string { return t.Local().Format("2 Jan 15:04") },
	"assetVersion": func() string { return staticVersion },
}

// staticVersion changes whenever one of the app's own files does, so
// browsers fetch the new one (?v=...).
var staticVersion = func() string {
	h := sha256.New()
	_ = fs.WalkDir(static.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(static.FS, p)
		h.Write([]byte(p))
		h.Write(b)
		return err
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

// clock is seconds as m:ss or h:mm:ss.
func clock(seconds float64) string {
	s := int(seconds)
	if s < 3600 {
		return fmt.Sprintf("%d:%02d", s/60, s%60)
	}
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}

func lang(c *gin.Context) string {
	v, _ := c.Cookie(i18n.CookieName)
	return i18n.Lang(v)
}

// render draws a page in the theme's shell, or only its content when the
// header's navigation loads it into #content.
func (h *web) render(c *gin.Context, status int, content, titleKey string, data any) {
	h.renderTitled(c, status, content, i18n.T(lang(c), titleKey), data)
}

// renderTitled is render with the page's title as it is (a movie's name).
func (h *web) renderTitled(c *gin.Context, status int, content, title string, data any) {
	l := lang(c)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Status(status)
	if c.GetHeader("HX-Request") == "true" && c.GetHeader("HX-Target") == "content" {
		if err := h.set.ExecuteTemplate(c.Writer, content, data); err != nil {
			log.Printf("render %s: %v", content, err)
		}
		return
	}
	app := i18n.T(l, "app")
	page := theme.Page{
		Lang:         l,
		Title:        title + " · " + app,
		Mode:         theme.ModeFromRequest(c.Request),
		CookieDomain: h.d.LanDomain,
		Header: theme.Header{
			AppName: app, HomeURL: "/", Lang: l, LoggedIn: true,
			Nav: []theme.NavItem{
				{Label: i18n.T(l, "nav.movies"), URL: "/movies"},
				{Label: i18n.T(l, "nav.shows"), URL: "/shows"},
				{Label: i18n.T(l, "nav.downloads"), URL: "/downloads"},
			},
			LanguageURL: "/lang",
			AccountURL:  h.d.AuthURL + "/account",
			LogoutURL:   h.d.AuthURL + "/logout",
		},
		Footer:  theme.Footer{Year: time.Now().Year(), AppName: app, Lang: l},
		Content: content,
		Data:    data,
		CSS:     []string{"/static/media/css/media.css?v=" + staticVersion},
	}
	if err := h.set.ExecuteTemplate(c.Writer, "theme/page", page); err != nil {
		log.Printf("render %s: %v", content, err)
	}
}

// fragment renders one template alone (htmx responses).
func (h *web) fragment(c *gin.Context, status int, name string, data any) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Status(status)
	if err := h.set.ExecuteTemplate(c.Writer, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

// postLang saves the language for every app (the theme header's EN/BG).
func (h *web) postLang(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: i18n.CookieName, Value: i18n.Lang(c.PostForm("lang")), Domain: h.d.LanDomain, Path: "/",
		MaxAge: 365 * 24 * 3600, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	if c.GetHeader("HX-Request") == "true" {
		c.Header("HX-Refresh", "true")
		c.Status(http.StatusOK)
		return
	}
	c.Redirect(http.StatusSeeOther, "/")
}

// OriginCheck refuses a POST whose Origin (or, without one, Referer)
// isn't on the LAN domain: all the home server's apps are one site, so
// SameSite cookies alone don't stop another app's page posting here.
func OriginCheck(lanDomain string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		from := c.GetHeader("Origin")
		if from == "" {
			from = c.GetHeader("Referer")
		}
		if !onDomain(from, lanDomain) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}
}

func onDomain(raw, lanDomain string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	d := strings.ToLower(lanDomain)
	return d != "" && (host == d || strings.HasSuffix(host, "."+d))
}

// filesOnly serves files from fsys, and 404 for directories (no listings).
func filesOnly(fsys http.FileSystem) http.Handler {
	files := http.FileServer(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := fsys.Open(r.URL.Path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		info, err := f.Stat()
		f.Close()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// notFound is a plain 404 page.
func (h *web) notFound(c *gin.Context) {
	h.render(c, http.StatusNotFound, "media/message", "error.not_found",
		message{Lang: lang(c), Text: i18n.T(lang(c), "error.not_found")})
}

type message struct {
	Lang, Text string
}

func isNotFound(err error) bool {
	return errors.Is(err, errNotFound) || isJellyfinNotFound(err)
}

// sizeWords is a size in bytes in words (1 kB = 1000 bytes, as speeds).
func sizeWords(b int64) string {
	switch {
	case b >= 1_000_000_000:
		return fmt.Sprintf("%.1f GB", float64(b)/1_000_000_000)
	case b >= 1_000_000:
		return fmt.Sprintf("%d MB", b/1_000_000)
	case b >= 1_000:
		return fmt.Sprintf("%d kB", b/1_000)
	}
	return fmt.Sprintf("%d B", b)
}

// timeLeft is a time left in words, rounded up to the minute: "15 min",
// "1 h 15 min", "3 d 4 h".
func timeLeft(lang string, d time.Duration) string {
	m := int64((d + time.Minute - 1) / time.Minute)
	days, hours, mins := m/(24*60), m/60%24, m%60
	part := func(key string, n int64) string { return fmt.Sprintf(i18n.T(lang, key), n) }
	switch {
	case days > 0 && hours > 0:
		return part("time.days", days) + " " + part("time.hours", hours)
	case days > 0:
		return part("time.days", days)
	case hours > 0 && mins > 0:
		return part("time.hours", hours) + " " + part("time.minutes", mins)
	case hours > 0:
		return part("time.hours", hours)
	}
	return part("time.minutes", mins)
}
