package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/BatMichoo/media/internal/downloads"
	"github.com/BatMichoo/media/internal/i18n"
	"github.com/BatMichoo/media/internal/subtitles"
	"github.com/BatMichoo/theme"
	"github.com/gin-gonic/gin"
)

const maxTorrent = 10 << 20

// addForm is what the Add page shows (again, after an error).
type addForm struct {
	Lang                      string
	Magnet, Kind, Title, Year string
	Error                     string
}

func (h *web) getAdd(c *gin.Context) {
	h.render(c, http.StatusOK, "media/add", "add.title", addForm{Lang: lang(c), Kind: "movie"})
}

// errorText is the translated message for a service error.
func errorText(l string, err error) string {
	var in *downloads.InvalidError
	if errors.As(err, &in) {
		msg := i18n.T(l, in.Key)
		if in.Arg != "" && strings.Contains(msg, "%s") {
			return fmt.Sprintf(msg, in.Arg)
		}
		return msg
	}
	log.Printf("downloads: %v", err)
	return i18n.T(l, "error.generic")
}

// parseYear reads an optional year field: "" is 0.
func parseYear(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	y, err := strconv.Atoi(s)
	return y, err == nil
}

func (h *web) postAdd(c *gin.Context) {
	l := lang(c)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxTorrent+downloads.MaxUploads*subtitles.MaxSize+64<<10)
	f := addForm{Lang: l, Magnet: strings.TrimSpace(c.PostForm("magnet")), Kind: c.PostForm("kind"),
		Title: c.PostForm("title"), Year: c.PostForm("year")}
	fail := func(status int, msg string) {
		f.Error = msg
		h.render(c, status, "media/add", "add.title", f)
	}
	var file []byte
	if fh, err := c.FormFile("torrent"); err == nil {
		if fh.Size > maxTorrent {
			fail(http.StatusRequestEntityTooLarge, i18n.T(l, "error.too_big"))
			return
		}
		r, err := fh.Open()
		if err != nil {
			fail(http.StatusBadRequest, i18n.T(l, "error.generic"))
			return
		}
		file, err = io.ReadAll(io.LimitReader(r, maxTorrent))
		r.Close()
		if err != nil {
			fail(http.StatusBadRequest, i18n.T(l, "error.generic"))
			return
		}
	} else if c.Request.MultipartForm == nil && c.ContentType() == "multipart/form-data" {
		fail(http.StatusRequestEntityTooLarge, i18n.T(l, "error.too_big"))
		return
	}
	year, ok := parseYear(f.Year)
	if !ok {
		fail(http.StatusUnprocessableEntity, i18n.T(l, "error.year"))
		return
	}
	subs, err := uploads(c, "subtitles", c.PostForm("subtitle_lang"))
	if err != nil {
		fail(http.StatusUnprocessableEntity, errorText(l, err))
		return
	}
	_, _, err = h.d.Downloads.Add(c.Request.Context(), CurrentUser(c),
		downloads.AddInput{Magnet: f.Magnet, File: file, Kind: f.Kind, Title: f.Title, Year: year, Subtitles: subs})
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, downloads.ErrInvalid) {
			status = http.StatusUnprocessableEntity
		}
		fail(status, errorText(l, err))
		return
	}
	c.Redirect(http.StatusSeeOther, "/downloads")
}

// downloadRow is one download as the list shows it.
type downloadRow struct {
	downloads.Download
	Lang string
}

// Label is "Title (Year)" or "Title".
func (d downloadRow) Label() string {
	if d.Year > 0 {
		return fmt.Sprintf("%s (%d)", d.Title, d.Year)
	}
	return d.Title
}

// ShowStats is true while Transmission is downloading or seeding it.
func (d downloadRow) ShowStats() bool {
	if !d.Live || d.Paused {
		return false
	}
	return d.State == "downloading" || (d.State == "filed" && !d.SeedingUntil.IsZero() && !d.SeedingDone)
}

// UpText is "↑ 410 kB/s · 1.3 GB up · ratio 0.26".
func (d downloadRow) UpText() string {
	return fmt.Sprintf(i18n.T(d.Lang, "downloads.up"), rateWords(d.RateUpload), sizeWords(d.Uploaded), d.Ratio)
}

// PeersText is "12 of 45 peers, 8 sending" ("12 peers, 8 sending" when no
// tracker knows the seeders).
func (d downloadRow) PeersText() string {
	if d.Seeders >= 0 {
		return fmt.Sprintf(i18n.T(d.Lang, "downloads.peers_of"), d.Peers, d.Seeders, d.PeersSending)
	}
	return fmt.Sprintf(i18n.T(d.Lang, "downloads.peers"), d.Peers, d.PeersSending)
}

// StuckText says why a download isn't moving (see downloads.Download.Stuck).
func (d downloadRow) StuckText() string {
	l := d.Lang
	switch d.Stuck {
	case "queued":
		return fmt.Sprintf(i18n.T(l, "stuck.queued"), d.QueuePos)
	case "tracker":
		return fmt.Sprintf(i18n.T(l, "stuck.tracker"), d.TrackerError)
	case "no_peers":
		text := i18n.T(l, "stuck.no_peers")
		if d.Seeders >= 0 {
			text = fmt.Sprintf(i18n.T(l, "stuck.no_peers_seeders"), d.Seeders)
		}
		if d.PortClosed && d.Port > 0 {
			text += ". " + fmt.Sprintf(i18n.T(l, "stuck.port_closed"), d.Port)
		}
		return text
	case "in_order", "not_sending":
		return fmt.Sprintf(i18n.T(l, "stuck.not_sending"), d.Peers)
	}
	return ""
}

type downloadsView struct {
	Lang        string
	Rows        []downloadRow
	Unreachable bool
	LinkProblem string
	Toast       *theme.Toast
}

func (h *web) downloadsData(c *gin.Context) downloadsView {
	l := lang(c)
	v := downloadsView{Lang: l}
	list, err := h.d.Downloads.List(c.Request.Context())
	if err != nil {
		log.Printf("downloads: %v", err)
		v.Unreachable = true
	}
	for _, d := range list {
		v.Rows = append(v.Rows, downloadRow{Download: d, Lang: l})
	}
	if err := h.d.LinkProblem(); err != nil {
		v.LinkProblem = err.Error()
	}
	return v
}

func (h *web) downloads(c *gin.Context) {
	h.render(c, http.StatusOK, "media/downloads", "downloads.title", h.downloadsData(c))
}

func (h *web) downloadsList(c *gin.Context) {
	h.fragment(c, http.StatusOK, "media/downloads-list", h.downloadsData(c))
}

// find is one download by hash, from the list.
func (h *web) find(c *gin.Context) (downloads.Download, bool) {
	list, _ := h.d.Downloads.List(c.Request.Context())
	for _, d := range list {
		if d.Hash == c.Param("hash") {
			return d, true
		}
	}
	return downloads.Download{}, false
}

type editView struct {
	Lang, Hash, Kind, Title, Year, Error string
	Heading                              theme.SheetHeading
	Actions                              theme.SheetActions
}

func (h *web) editData(c *gin.Context, hash, kind, title, year, errText string) editView {
	l := lang(c)
	return editView{Lang: l, Hash: hash, Kind: kind, Title: title, Year: year, Error: errText,
		Heading: theme.SheetHeading{Title: i18n.T(l, "edit.title"), Lang: l},
		Actions: theme.SheetActions{Lang: l}}
}

func (h *web) getEdit(c *gin.Context) {
	d, ok := h.find(c)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	year := ""
	if d.Year > 0 {
		year = strconv.Itoa(d.Year)
	}
	h.fragment(c, http.StatusOK, "media/edit-sheet", h.editData(c, d.Hash, d.Kind, d.Title, year, ""))
}

// sheetError shows a form again in the sheet, with its error.
func (h *web) sheetError(c *gin.Context, name string, data any) {
	c.Header("HX-Retarget", "#sheet")
	c.Header("HX-Reswap", "innerHTML")
	h.fragment(c, http.StatusOK, name, data)
}

// listWithToast answers with the refreshed list and a toast.
func (h *web) listWithToast(c *gin.Context, titleKey string) {
	v := h.downloadsData(c)
	v.Toast = &theme.Toast{Kind: theme.ToastSuccess, Title: i18n.T(v.Lang, titleKey), Lang: v.Lang}
	h.fragment(c, http.StatusOK, "media/downloads-list", v)
}

func (h *web) postEdit(c *gin.Context) {
	l := lang(c)
	hash, kind, title, yearText := c.Param("hash"), c.PostForm("kind"), c.PostForm("title"), c.PostForm("year")
	year, ok := parseYear(yearText)
	if !ok {
		h.sheetError(c, "media/edit-sheet", h.editData(c, hash, kind, title, yearText, i18n.T(l, "error.year")))
		return
	}
	err := h.d.Downloads.Edit(c.Request.Context(), hash, kind, title, year)
	switch {
	case errors.Is(err, downloads.ErrNotFound):
		c.Status(http.StatusNotFound)
		return
	case err != nil:
		h.sheetError(c, "media/edit-sheet", h.editData(c, hash, kind, title, yearText, errorText(l, err)))
		return
	}
	h.listWithToast(c, "toast.saved")
}

type removeView struct {
	Lang, Hash, Message string
	Heading             theme.SheetHeading
}

func (h *web) getRemove(c *gin.Context) {
	d, ok := h.find(c)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	l := lang(c)
	h.fragment(c, http.StatusOK, "media/remove-sheet", removeView{Lang: l, Hash: d.Hash,
		Message: fmt.Sprintf(i18n.T(l, "remove.message"), downloadRow{Download: d}.Label()),
		Heading: theme.SheetHeading{Title: i18n.T(l, "remove.title"), Lang: l}})
}

func (h *web) postRemove(c *gin.Context) {
	err := h.d.Downloads.Remove(c.Request.Context(), c.Param("hash"), c.PostForm("delete_files") != "")
	if errors.Is(err, downloads.ErrNotFound) {
		c.Status(http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("remove: %v", err)
		v := h.downloadsData(c)
		v.Toast = &theme.Toast{Kind: theme.ToastError, Title: i18n.T(v.Lang, "error.generic"), Lang: v.Lang}
		h.fragment(c, http.StatusOK, "media/downloads-list", v)
		return
	}
	h.listWithToast(c, "toast.removed")
}

// pauseOrResume runs Pause, Resume, StartNow or AnyOrder and answers with
// the refreshed list.
func (h *web) pauseOrResume(c *gin.Context, do func(ctx context.Context, hash string) error) {
	err := do(c.Request.Context(), c.Param("hash"))
	if errors.Is(err, downloads.ErrNotFound) {
		c.Status(http.StatusNotFound)
		return
	}
	v := h.downloadsData(c)
	if err != nil {
		log.Printf("%s: %v", c.FullPath(), err)
		v.Toast = &theme.Toast{Kind: theme.ToastError, Title: i18n.T(v.Lang, "error.generic"), Lang: v.Lang}
	}
	h.fragment(c, http.StatusOK, "media/downloads-list", v)
}

func (h *web) postPause(c *gin.Context)    { h.pauseOrResume(c, h.d.Downloads.Pause) }
func (h *web) postResume(c *gin.Context)   { h.pauseOrResume(c, h.d.Downloads.Resume) }
func (h *web) postStartNow(c *gin.Context) { h.pauseOrResume(c, h.d.Downloads.StartNow) }
func (h *web) postAnyOrder(c *gin.Context) { h.pauseOrResume(c, h.d.Downloads.AnyOrder) }

// uploads reads the subtitle files sent in a form field, all in one
// language; the downloads service checks them.
func uploads(c *gin.Context, field, lang string) ([]downloads.Upload, error) {
	if c.Request.MultipartForm == nil {
		return nil, nil
	}
	var out []downloads.Upload
	for _, fh := range c.Request.MultipartForm.File[field] {
		if fh.Filename == "" && fh.Size == 0 {
			continue // the field left empty
		}
		if fh.Size > subtitles.MaxSize {
			return nil, &downloads.InvalidError{Key: "error.sub_size", Arg: fh.Filename}
		}
		r, err := fh.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(r, subtitles.MaxSize+1))
		r.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, downloads.Upload{Name: fh.Filename, Lang: lang, Data: data})
	}
	return out, nil
}

var unplacedReason = regexp.MustCompile(`^(\d+) subtitles? didn't match a video$`)

// reasonText is a download's reason in the page's language where the
// app wrote it (the rest come from elsewhere and stay as they are).
func reasonText(l, reason string) string {
	if m := unplacedReason.FindStringSubmatch(reason); m != nil {
		n, _ := strconv.Atoi(m[1])
		key := "downloads.subs_unplaced_many"
		if n == 1 {
			key = "downloads.subs_unplaced_one"
		}
		return fmt.Sprintf(i18n.T(l, key), n)
	}
	return reason
}
