package handlers

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/BatMichoo/media/internal/jellyfin"
	"github.com/gin-gonic/gin"
)

// Upstream is Jellyfin, with the app's token added to every request.
type Upstream interface {
	Do(ctx context.Context, method, path string, h http.Header) (*http.Response, error)
}

var (
	// Only video endpoints of one item go through /stream.
	streamPath = regexp.MustCompile(`^/[Vv]ideos/[0-9a-f]{32}/`)
	itemID     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	uriAttr    = regexp.MustCompile(`URI="([^"]*)"`)

	forwardRequest  = []string{"Range", "If-Range", "Accept"}
	forwardResponse = []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
		"Cache-Control", "Last-Modified", "ETag"}
	imageTypes = map[string]bool{"Primary": true, "Backdrop": true, "Thumb": true}
)

// StreamURL turns a Jellyfin-relative video URL into the app's own,
// without any token.
func StreamURL(jellyfinRelative string) string {
	return "/stream" + jellyfin.StripToken(jellyfinRelative)
}

// RegisterStream adds /stream/... (video, through Jellyfin) and
// /images/:id/:type (posters) to routes that already require a user.
func RegisterStream(r gin.IRoutes, up Upstream) {
	h := &streamer{up: up}
	r.GET("/stream/*path", h.stream)
	r.HEAD("/stream/*path", h.stream)
	r.GET("/images/:id/:type", h.image)
}

type streamer struct{ up Upstream }

func (s *streamer) stream(c *gin.Context) {
	p := c.Param("path")
	if path.Clean(p) != p || strings.Contains(p, "..") || !streamPath.MatchString(p) {
		c.Status(http.StatusNotFound)
		return
	}
	target := jellyfin.StripToken(p + "?" + c.Request.URL.RawQuery)
	h := http.Header{}
	for _, k := range forwardRequest {
		if v := c.GetHeader(k); v != "" {
			h.Set(k, v)
		}
	}
	resp, err := s.up.Do(c.Request.Context(), c.Request.Method, target, h)
	if err != nil {
		c.Status(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if isPlaylist(p, resp.Header.Get("Content-Type")) && c.Request.Method == http.MethodGet {
		copyHeaders(c, resp, "Content-Length")
		c.Status(resp.StatusCode)
		rewritePlaylist(c.Writer, resp.Body)
		return
	}
	copyHeaders(c, resp)
	c.Status(resp.StatusCode)
	if c.Request.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(c.Writer, resp.Body)
}

func (s *streamer) image(c *gin.Context) {
	id, typ := c.Param("id"), c.Param("type")
	if !itemID.MatchString(id) || !imageTypes[typ] {
		c.Status(http.StatusNotFound)
		return
	}
	w := 400
	if v, err := strconv.Atoi(c.Query("w")); err == nil {
		w = min(max(v, 100), 1920)
	}
	target := "/Items/" + id + "/Images/" + typ + "?maxWidth=" + strconv.Itoa(w) + "&quality=90"
	resp, err := s.up.Do(c.Request.Context(), http.MethodGet, target, http.Header{})
	if err != nil {
		c.Status(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeaders(c, resp, "Cache-Control")
	if resp.StatusCode == http.StatusOK {
		c.Header("Cache-Control", "private, max-age=86400")
	}
	c.Status(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
}

// copyHeaders copies the allowed response headers, except skip.
func copyHeaders(c *gin.Context, resp *http.Response, skip ...string) {
	for _, k := range forwardResponse {
		if contains(skip, k) {
			continue
		}
		if v := resp.Header.Get(k); v != "" {
			c.Header(k, v)
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func isPlaylist(p, contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.HasSuffix(p, ".m3u8") || strings.Contains(ct, "mpegurl")
}

// rewritePlaylist copies an HLS playlist, stripping the token from every
// address in it and sending Jellyfin-absolute addresses through /stream.
func rewritePlaylist(w io.Writer, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "#"):
			line = uriAttr.ReplaceAllStringFunc(line, func(m string) string {
				return `URI="` + rewriteURI(m[len(`URI="`):len(m)-1]) + `"`
			})
		case strings.TrimSpace(line) != "":
			line = rewriteURI(line)
		}
		_, _ = io.WriteString(w, line+"\n")
	}
}

func rewriteURI(u string) string {
	u = jellyfin.StripToken(u)
	if strings.HasPrefix(strings.ToLower(u), "/videos/") {
		return "/stream" + u
	}
	return u
}
