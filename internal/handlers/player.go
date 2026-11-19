package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/BatMichoo/media/internal/i18n"
	"github.com/BatMichoo/media/internal/jellyfin"
	"github.com/BatMichoo/media/internal/watching"
	"github.com/gin-gonic/gin"
)

// registerPlayer adds the player's routes.
func (h *web) registerPlayer(g *gin.RouterGroup) {
	g.GET("/watch/:id", h.watch)
	g.POST("/play/:id", h.play)
	g.POST("/progress", h.progress)
	g.POST("/prefs", h.prefs)
}

type partView struct {
	ID       string  `json:"id"`
	Duration float64 `json:"duration"`
}

type watchView struct {
	Lang, Item, Series, Title, Back string
	Parts                           string // JSON list of partView
	Part                            int
	Start                           float64
	Labels                          string // JSON of the player's texts
}

var playerLabels = []string{"player.back", "player.play", "player.pause", "player.back10", "player.forward10",
	"player.subtitles", "player.audio", "player.off", "player.fullscreen", "player.error",
	"player.mute", "player.unmute", "player.settings", "player.exitFullscreen"}

func (h *web) watch(c *gin.Context) {
	ctx := c.Request.Context()
	l := lang(c)
	it, err := h.d.Catalog.Item(ctx, c.Param("id"))
	if isNotFound(err) {
		h.notFound(c)
		return
	}
	if err != nil {
		h.unavailable(c, err)
		return
	}
	v := watchView{Lang: l, Item: it.ID, Title: it.DisplayTitle(), Back: "/"}
	parts := []partView{{ID: it.ID, Duration: it.RunTimeSeconds}}
	switch it.Type {
	case "Movie":
		v.Back = "/movies/" + it.ID
		if ps, err := h.d.Catalog.Parts(ctx, it.ID); err == nil && len(ps) > 0 {
			parts = parts[:0]
			for _, p := range ps {
				parts = append(parts, partView{ID: p.ID, Duration: p.RunTimeSeconds})
			}
		}
	case "Episode":
		v.Series = it.SeriesID
		v.Title = it.ShowTitle() + " · " + it.Name
		if it.SeriesID != "" {
			v.Back = "/shows/" + it.SeriesID
		}
	}
	if p, ok, err := h.d.Watching.Get(ctx, CurrentUser(c).ID, it.ID); err == nil && ok && !p.Finished && p.Part < len(parts) {
		v.Part, v.Start = p.Part, p.Position
	}
	if s := c.Query("part"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 && n < len(parts) {
			v.Part, v.Start = n, 0
		}
	}
	b, _ := json.Marshal(parts)
	v.Parts = string(b)
	labels := map[string]string{}
	for _, k := range playerLabels {
		labels[strings.TrimPrefix(k, "player.")] = i18n.T(l, k)
	}
	b, _ = json.Marshal(labels)
	v.Labels = string(b)
	h.renderTitled(c, http.StatusOK, "media/watch", v.Title, v)
}

type trackJSON struct {
	Index int     `json:"index"`
	Label string  `json:"label"`
	Lang  string  `json:"lang"`
	Image bool    `json:"image"`
	URL   *string `json:"url"`
}

type playResponse struct {
	URL           string      `json:"url"`
	Direct        bool        `json:"direct"`
	PlaySessionID string      `json:"playSessionId"`
	MediaSourceID string      `json:"mediaSourceId"`
	Duration      float64     `json:"duration"`
	Subtitles     []trackJSON `json:"subtitles"`
	AudioTracks   []trackJSON `json:"audioTracks"`
	Sub           *int        `json:"sub"`
	Audio         *int        `json:"audio"`
}

// play answers how to play one part in this browser. Without "audio" or
// "sub" in the request, the person's languages pick them; null means
// none (subtitles off, Jellyfin's default audio).
func (h *web) play(c *gin.Context) {
	ctx := c.Request.Context()
	l := lang(c)
	id := c.Param("id")
	if !itemID.MatchString(id) {
		c.Status(http.StatusNotFound)
		return
	}
	bad := func() { c.JSON(http.StatusBadRequest, gin.H{"error": i18n.T(l, "error.generic")}) }
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)).Decode(&raw); err != nil {
		bad()
		return
	}
	var caps struct {
		Containers  []string `json:"containers"`
		VideoCodecs []string `json:"videoCodecs"`
		AudioCodecs []string `json:"audioCodecs"`
		HLS         bool     `json:"hls"`
	}
	var start float64
	if v, ok := raw["caps"]; ok && json.Unmarshal(v, &caps) != nil {
		bad()
		return
	}
	if v, ok := raw["start"]; ok && json.Unmarshal(v, &start) != nil {
		bad()
		return
	}
	jc := jellyfin.Caps{Containers: caps.Containers, VideoCodecs: caps.VideoCodecs, AudioCodecs: caps.AudioCodecs, HLS: caps.HLS}
	audio, audioGiven, err1 := optionalIndex(raw, "audio")
	sub, subGiven, err2 := optionalIndex(raw, "sub")
	if err1 != nil || err2 != nil || start < 0 {
		bad()
		return
	}

	src, err := h.d.Player.Play(ctx, id, jc, audio, sub, start)
	if err == nil && (!audioGiven || !subGiven) {
		prefs, perr := h.d.Watching.Prefs(ctx, CurrentUser(c).ID)
		if perr != nil {
			log.Printf("prefs: %v", perr)
		}
		pSub, pAud := watching.PickTracks(prefs, src.Subtitles, src.Audio)
		if !subGiven {
			sub = pSub
		}
		if !audioGiven {
			audio = pAud
		}
		// Ask again only when it changes what Jellyfin sends: another
		// audio track, or picture subtitles to burn in.
		if notDefault(audio, src.Audio) || isImage(sub, src.Subtitles) {
			var burn *int
			if isImage(sub, src.Subtitles) {
				burn = sub
			}
			src, err = h.d.Player.Play(ctx, id, jc, audio, burn, start)
		}
	}
	if err != nil {
		log.Printf("play %s: %v", id, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": i18n.T(l, "player.error")})
		return
	}
	if err := h.d.Player.Report(ctx, "start", id, src.MediaSourceID, src.PlaySessionID, start); err != nil {
		log.Printf("report start: %v", err)
	}
	resp := playResponse{URL: StreamURL(src.URL), Direct: src.Direct, PlaySessionID: src.PlaySessionID,
		MediaSourceID: src.MediaSourceID, Duration: src.DurationSeconds, Sub: sub, Audio: audio,
		Subtitles: []trackJSON{}, AudioTracks: []trackJSON{}}
	for _, t := range src.Subtitles {
		tj := trackJSON{Index: t.Index, Label: t.Label, Lang: t.Lang, Image: t.Image}
		if !t.Image {
			u := fmt.Sprintf("/stream/Videos/%s/%s/Subtitles/%d/0/Stream.vtt", id, src.MediaSourceID, t.Index)
			tj.URL = &u
		}
		resp.Subtitles = append(resp.Subtitles, tj)
	}
	for _, t := range src.Audio {
		resp.AudioTracks = append(resp.AudioTracks, trackJSON{Index: t.Index, Label: t.Label, Lang: t.Lang})
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, resp)
}

// optionalIndex reads a track index that may be missing (given=false),
// null (nil) or a number.
func optionalIndex(raw map[string]json.RawMessage, key string) (idx *int, given bool, err error) {
	v, ok := raw[key]
	if !ok {
		return nil, false, nil
	}
	if string(v) == "null" {
		return nil, true, nil
	}
	var n int
	if err := json.Unmarshal(v, &n); err != nil {
		return nil, true, err
	}
	return &n, true, nil
}

// isImage reports whether sub is a picture subtitle track (burned in by
// Jellyfin); text ones come as their own file.
func isImage(sub *int, tracks []jellyfin.Track) bool {
	if sub == nil {
		return false
	}
	for _, t := range tracks {
		if t.Index == *sub {
			return t.Image
		}
	}
	return false
}

// notDefault reports whether an audio choice differs from the default
// track.
func notDefault(audio *int, tracks []jellyfin.Track) bool {
	if audio == nil || len(tracks) == 0 {
		return false
	}
	for _, t := range tracks {
		if t.Default {
			return t.Index != *audio
		}
	}
	return tracks[0].Index != *audio
}

func (h *web) progress(c *gin.Context) {
	ctx := c.Request.Context()
	item, series := c.PostForm("item"), c.PostForm("series")
	part, err1 := strconv.Atoi(c.PostForm("part"))
	pos, err2 := strconv.ParseFloat(c.PostForm("position"), 64)
	var durations []float64
	var err3 error
	for _, s := range strings.Split(c.PostForm("durations"), ",") {
		d, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			err3 = err
			break
		}
		durations = append(durations, d)
	}
	finite := func(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 }
	ok := finite(pos)
	for _, d := range durations {
		ok = ok && finite(d)
	}
	if !ok || !itemID.MatchString(item) || (series != "" && !itemID.MatchString(series)) || err1 != nil || err2 != nil || err3 != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	if _, err := h.d.Watching.Save(ctx, CurrentUser(c).ID, watching.SaveInput{
		ItemID: item, SeriesID: series, Part: part, Position: pos, PartDurations: durations,
	}); err != nil {
		log.Printf("save progress: %v", err)
		c.Status(http.StatusBadRequest)
		return
	}
	event := "progress"
	if c.PostForm("event") == "stop" {
		event = "stopped"
	}
	if ps := c.PostForm("playSessionId"); ps != "" {
		// Jellyfin knows the session by the part being played; the player
		// sends that part's id with its own session.
		reportItem := c.PostForm("partItem")
		if !itemID.MatchString(reportItem) {
			reportItem = item
		}
		if err := h.d.Player.Report(ctx, event, reportItem, c.PostForm("mediaSourceId"), ps, pos); err != nil {
			log.Printf("report %s: %v", event, err)
		}
	}
	c.Status(http.StatusNoContent)
}

func (h *web) prefs(c *gin.Context) {
	var sub, aud *string
	if v, ok := c.GetPostForm("subtitle_lang"); ok {
		sub = &v
	}
	if v, ok := c.GetPostForm("audio_lang"); ok {
		aud = &v
	}
	if sub == nil && aud == nil {
		c.Status(http.StatusBadRequest)
		return
	}
	if err := h.d.Watching.SetPrefs(c.Request.Context(), CurrentUser(c).ID, sub, aud); err != nil {
		log.Printf("prefs: %v", err)
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusNoContent)
}
