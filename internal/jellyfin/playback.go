package jellyfin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Caps is what the browser says it can play.
type Caps struct {
	Containers, VideoCodecs, AudioCodecs []string
	HLS                                  bool // native HLS (Safari); else hls.js
}

// Track is a subtitle or audio track.
type Track struct {
	Index   int
	Label   string
	Lang    string
	Image   bool // picture subtitles (PGS, VobSub): burned in by conversion
	Default bool
}

// Source is how to play an item: URL is relative to Jellyfin and carries
// no token.
type Source struct {
	Direct                       bool
	URL                          string
	PlaySessionID, MediaSourceID string
	Subtitles, Audio             []Track
	DurationSeconds              float64
}

// Profile is the device profile for these caps: play directly what the
// browser supports, otherwise HLS with H.264 and AAC; text subtitles are
// fetched separately, picture subtitles burned in.
func Profile(c Caps) map[string]any {
	subs := []map[string]any{}
	for _, f := range []string{"vtt", "srt", "subrip", "ass", "ssa"} {
		subs = append(subs, map[string]any{"Format": f, "Method": "External"})
	}
	for _, f := range []string{"pgssub", "dvdsub", "dvbsub", "vobsub"} {
		subs = append(subs, map[string]any{"Format": f, "Method": "Encode"})
	}
	return map[string]any{
		"Name":                "media",
		"MaxStreamingBitrate": 120_000_000,
		"DirectPlayProfiles": []map[string]any{{
			"Type":       "Video",
			"Container":  strings.Join(c.Containers, ","),
			"VideoCodec": strings.Join(c.VideoCodecs, ","),
			"AudioCodec": strings.Join(c.AudioCodecs, ","),
		}},
		"TranscodingProfiles": []map[string]any{{
			"Container": "mp4", "Type": "Video", "VideoCodec": "h264", "AudioCodec": "aac",
			"Protocol": "hls", "Context": "Streaming", "MaxAudioChannels": "2", "BreakOnNonKeyFrames": true,
		}},
		"SubtitleProfiles": subs,
	}
}

// Play asks Jellyfin how to play an item in this browser, with the given
// audio and subtitle tracks (nil = Jellyfin's default / none), from
// startSeconds.
func (c *Client) Play(ctx context.Context, itemID string, caps Caps, audioIndex, subIndex *int, startSeconds float64) (Source, error) {
	body := map[string]any{
		"UserId":              c.uid(),
		"StartTimeTicks":      int64(startSeconds * ticksPerSecond),
		"MaxStreamingBitrate": 120_000_000,
		"EnableDirectPlay":    true,
		"EnableDirectStream":  true,
		"EnableTranscoding":   true,
		"AutoOpenLiveStream":  true,
		"DeviceProfile":       Profile(caps),
	}
	if audioIndex != nil {
		body["AudioStreamIndex"] = *audioIndex
	}
	if subIndex != nil {
		body["SubtitleStreamIndex"] = *subIndex
	} else {
		body["SubtitleStreamIndex"] = -1
	}
	var out struct {
		PlaySessionId string
		MediaSources  []struct {
			Id                 string
			SupportsDirectPlay bool
			TranscodingUrl     string
			RunTimeTicks       int64
			MediaStreams       []struct {
				Type, Codec, Language, DisplayTitle string
				Index                               int
				IsDefault, IsTextSubtitleStream     bool
			}
		}
	}
	if err := c.post(ctx, "/Items/"+url.PathEscape(itemID)+"/PlaybackInfo", nil, body, &out); err != nil {
		return Source{}, err
	}
	if len(out.MediaSources) == 0 {
		return Source{}, fmt.Errorf("jellyfin: no way to play %s", itemID)
	}
	ms := out.MediaSources[0]
	src := Source{
		PlaySessionID:   out.PlaySessionId,
		MediaSourceID:   ms.Id,
		DurationSeconds: float64(ms.RunTimeTicks) / ticksPerSecond,
	}
	switch {
	case ms.SupportsDirectPlay:
		src.Direct = true
		q := url.Values{"static": {"true"}, "mediaSourceId": {ms.Id}, "playSessionId": {out.PlaySessionId}}
		src.URL = "/Videos/" + url.PathEscape(itemID) + "/stream?" + q.Encode()
	case ms.TranscodingUrl != "":
		src.URL = StripToken(ms.TranscodingUrl)
	default:
		return Source{}, fmt.Errorf("jellyfin: %s can't be played here", itemID)
	}
	for _, s := range ms.MediaStreams {
		t := Track{Index: s.Index, Label: s.DisplayTitle, Lang: s.Language, Default: s.IsDefault}
		switch s.Type {
		case "Subtitle":
			t.Image = !s.IsTextSubtitleStream
			src.Subtitles = append(src.Subtitles, t)
		case "Audio":
			src.Audio = append(src.Audio, t)
		}
	}
	return src, nil
}

// StripToken removes api_key / ApiKey query parameters (any case) from a
// URL, keeping the rest of it as it was.
func StripToken(raw string) string {
	base, query, found := strings.Cut(raw, "?")
	if !found {
		return raw
	}
	var kept []string
	for _, p := range strings.Split(query, "&") {
		name, _, _ := strings.Cut(p, "=")
		if n := strings.ToLower(name); n == "api_key" || n == "apikey" {
			continue
		}
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return base
	}
	return base + "?" + strings.Join(kept, "&")
}

// Report tells Jellyfin about playback: "start", "progress" or
// "stopped". Stopped also ends any conversion for that session at once.
func (c *Client) Report(ctx context.Context, event, itemID, mediaSourceID, playSessionID string, positionSeconds float64) error {
	paths := map[string]string{
		"start":    "/Sessions/Playing",
		"progress": "/Sessions/Playing/Progress",
		"stopped":  "/Sessions/Playing/Stopped",
	}
	p, ok := paths[event]
	if !ok {
		return fmt.Errorf("jellyfin: unknown playback event %q", event)
	}
	body := map[string]any{
		"ItemId": itemID, "MediaSourceId": mediaSourceID, "PlaySessionId": playSessionID,
		"PositionTicks": int64(positionSeconds * ticksPerSecond), "CanSeek": true,
	}
	if err := c.post(ctx, p, nil, body, nil); err != nil {
		return err
	}
	if event != "stopped" {
		return nil
	}
	q := url.Values{"deviceId": {c.DeviceID}, "playSessionId": {playSessionID}}
	return c.send(ctx, http.MethodDelete, "/Videos/ActiveEncodings?"+q.Encode(), nil, nil, true)
}
