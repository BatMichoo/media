package handlers

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/BatMichoo/media/internal/jellyfin"
	"github.com/BatMichoo/media/internal/model"

	"github.com/BatMichoo/media/internal/downloads"
	"github.com/BatMichoo/media/internal/i18n"
	"github.com/BatMichoo/media/internal/subtitles"
	"github.com/gin-gonic/gin"
)

// libraryPath is a Jellyfin file path as the app sees it, relative to the
// data folder: the part from /library/ on, which must be a plain path
// under library/Movies or library/Shows.
func libraryPath(jellyfinPath string) (string, bool) {
	p := strings.ReplaceAll(jellyfinPath, "\\", "/")
	i := strings.Index(p, "/library/")
	if i < 0 {
		return "", false
	}
	rel := "library/" + p[i+len("/library/"):]
	if path.Clean(rel) != rel {
		return "", false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return "", false
		}
	}
	if !strings.HasPrefix(rel, "library/Movies/") && !strings.HasPrefix(rel, "library/Shows/") {
		return "", false
	}
	return rel, true
}

// postSubtitles takes a subtitle file for a movie (or one of its parts)
// or an episode, puts it next to the video and asks Jellyfin to look.
func (h *web) postSubtitles(c *gin.Context) {
	ctx := c.Request.Context()
	// Room for a file somewhat over the limit, so it's reported as too big
	// rather than as missing.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*subtitles.MaxSize+64<<10)
	it, err := h.d.Catalog.Item(ctx, c.Param("id"))
	if isNotFound(err) {
		h.notFound(c)
		return
	}
	if err != nil {
		h.unavailable(c, err)
		return
	}
	var kind model.Kind
	var back string
	switch it.Type {
	case jellyfin.TypeMovie, jellyfin.TypeVideo:
		kind, back = model.KindMovie, "/movies/"+it.ID
	case jellyfin.TypeEpisode:
		kind, back = model.KindSeries, "/shows/"+it.SeriesID
		if it.SeriesID == "" {
			back = "/"
		}
	default:
		c.String(http.StatusBadRequest, "subtitles are for a movie or an episode")
		return
	}
	fail := func(key, file string) {
		q := url.Values{"subs_error": {key}}
		if file != "" {
			q.Set("subs_file", file)
		}
		c.Redirect(http.StatusSeeOther, back+"?"+q.Encode())
	}
	src := it.Path
	if n, err := strconv.Atoi(c.PostForm("part")); kind == model.KindMovie && err == nil && n > 1 {
		parts, err := h.d.Catalog.Parts(ctx, it.ID)
		if err != nil || n > len(parts) {
			fail("error.generic", "")
			return
		}
		src = parts[n-1].Path
	}
	rel, ok := libraryPath(src)
	if !ok {
		fail("error.not_in_library", "")
		return
	}
	ups, err := uploads(c, "file", c.PostForm("subtitle_lang"))
	if err == nil && len(ups) == 0 {
		fail("error.sub_missing", "")
		return
	}
	if err == nil {
		err = h.d.Downloads.LibrarySubtitle(ctx, rel, kind, ups)
	}
	var in *downloads.InvalidError
	if errors.As(err, &in) {
		fail(in.Key, in.Arg)
		return
	}
	if err != nil {
		log.Printf("subtitles for %s: %v", it.ID, err)
		fail("error.generic", "")
		return
	}
	if err := h.d.Library.RefreshItem(ctx, it.ID); err != nil {
		log.Printf("refresh %s: %v", it.ID, err)
		if err := h.d.Library.Refresh(ctx); err != nil {
			log.Printf("library rescan: %v", err)
		}
	}
	c.Redirect(http.StatusSeeOther, back+"?subs=1")
}

// subsNotice is the result of a subtitle upload shown on the page it
// came from: added, or an error message ("" for neither).
func subsNotice(c *gin.Context, l string) (added bool, errMsg string) {
	if c.Query("subs") == "1" {
		return true, ""
	}
	if key := c.Query("subs_error"); strings.HasPrefix(key, "error.") {
		return false, errorText(l, &downloads.InvalidError{Key: key, Arg: fileOr(l, c.Query("subs_file"))})
	}
	return false, ""
}

// fileOr is a file name for an error message (shortened), or "the file"
// when unknown.
func fileOr(l, name string) string {
	if name == "" {
		return i18n.T(l, "subs.the_file")
	}
	if r := []rune(name); len(r) > 80 {
		return string(r[:40]) + "…" + string(r[len(r)-20:])
	}
	return name
}
