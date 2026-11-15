package handlers

import (
	"net/http"
	"net/url"
	"regexp"

	"github.com/BatMichoo/media/internal/model"
	"github.com/gin-gonic/gin"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const userKey = "media.user"

// RequireUser takes the person from the headers the home server's proxy
// sets after login (X-User-Id, X-User-Name). The app is reachable only
// through that proxy, so a request without them isn't logged in: 401.
func RequireUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-User-Id")
		if !uuidRe.MatchString(id) {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		name, err := url.QueryUnescape(c.GetHeader("X-User-Name"))
		if err != nil || name == "" {
			name = c.GetHeader("X-User-Name")
		}
		c.Set(userKey, model.User{ID: id, Name: name})
		c.Next()
	}
}

// CurrentUser is the person RequireUser found.
func CurrentUser(c *gin.Context) model.User {
	u, _ := c.Get(userKey)
	user, _ := u.(model.User)
	return user
}
