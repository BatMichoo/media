// Package handlers is the media app's HTTP layer: request parsing,
// validation and rendering, over the services.
package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Pinger reports whether the database answers.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps is everything the handlers need, built once in main.
type Deps struct {
	DB          Pinger
	Catalog     Catalog
	Player      Player
	Downloads   Downloads
	Watching    Watching
	Upstream    Upstream
	LanDomain   string       // cookies are set for it; POSTs must come from it
	AuthURL     string       // the login service, e.g. https://auth.homeserver.lan
	LinkProblem func() error // why downloads can't be filed, nil when fine
}

// NewRouter is the base router with the health check. It depends on the
// database only, so the app counts as healthy while Jellyfin starts.
func NewRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) {
		if err := d.DB.Ping(c.Request.Context()); err != nil {
			c.String(http.StatusServiceUnavailable, "database unavailable")
			return
		}
		c.String(http.StatusOK, "ok")
	})
	return r
}
