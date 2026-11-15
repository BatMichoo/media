package jellyfin

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func playbackInfo(direct bool, transcodingURL string) map[string]any {
	return map[string]any{
		"PlaySessionId": "ps1",
		"MediaSources": []any{map[string]any{
			"Id": "ms1", "SupportsDirectPlay": direct, "SupportsTranscoding": true,
			"TranscodingUrl": transcodingURL, "RunTimeTicks": 100 * 10_000_000,
			"MediaStreams": []any{
				map[string]any{"Type": "Video", "Index": 0, "Codec": "h264"},
				map[string]any{"Type": "Audio", "Index": 1, "Codec": "aac", "Language": "eng", "DisplayTitle": "English AAC", "IsDefault": true},
				map[string]any{"Type": "Audio", "Index": 2, "Codec": "ac3", "Language": "bul", "DisplayTitle": "Bulgarian AC3"},
				map[string]any{"Type": "Subtitle", "Index": 3, "Codec": "subrip", "Language": "eng", "DisplayTitle": "English", "IsTextSubtitleStream": true},
				map[string]any{"Type": "Subtitle", "Index": 4, "Codec": "PGSSUB", "Language": "bul", "DisplayTitle": "Bulgarian PGS", "IsTextSubtitleStream": false},
			},
		}},
	}
}

var caps = Caps{Containers: []string{"mp4", "webm"}, VideoCodecs: []string{"h264", "vp9"}, AudioCodecs: []string{"aac", "opus"}, HLS: false}

func TestPlayDirect(t *testing.T) {
	f, c := loggedIn(t)
	f.json("POST /Items/m1/PlaybackInfo", playbackInfo(true, ""))
	src, err := c.Play(context.Background(), "m1", caps, nil, nil, 0)
	require.NoError(t, err)
	assert.True(t, src.Direct)
	assert.Contains(t, src.URL, "/Videos/m1/stream?")
	assert.Contains(t, src.URL, "static=true")
	assert.Contains(t, src.URL, "mediaSourceId=ms1")
	assert.NotContains(t, strings.ToLower(src.URL), "api_key")
	assert.Equal(t, "ps1", src.PlaySessionID)
	assert.Equal(t, "ms1", src.MediaSourceID)
	assert.Equal(t, 100.0, src.DurationSeconds)
}

func TestPlayTranscodeStripsToken(t *testing.T) {
	f, c := loggedIn(t)
	f.json("POST /Items/m1/PlaybackInfo", playbackInfo(false,
		"/videos/m1/master.m3u8?DeviceId=dev1&MediaSourceId=ms1&api_key=SECRET&PlaySessionId=ps1&ApiKey=SECRET"))
	src, err := c.Play(context.Background(), "m1", caps, nil, nil, 0)
	require.NoError(t, err)
	assert.False(t, src.Direct)
	assert.NotContains(t, src.URL, "SECRET")
	assert.Contains(t, src.URL, "/videos/m1/master.m3u8?")
	assert.Contains(t, src.URL, "PlaySessionId=ps1")
}

func TestPlayRequest(t *testing.T) {
	f, c := loggedIn(t)
	f.json("POST /Items/m1/PlaybackInfo", playbackInfo(true, ""))
	a, s := 2, 4
	_, err := c.Play(context.Background(), "m1", caps, &a, &s, 12.5)
	require.NoError(t, err)
	b := f.find("POST /Items/m1/PlaybackInfo").Body
	assert.Equal(t, "u1", b["UserId"])
	assert.Equal(t, float64(125_000_000), b["StartTimeTicks"])
	assert.Equal(t, float64(2), b["AudioStreamIndex"])
	assert.Equal(t, float64(4), b["SubtitleStreamIndex"])
	assert.NotNil(t, b["DeviceProfile"])
}

func TestPlayTracks(t *testing.T) {
	f, c := loggedIn(t)
	f.json("POST /Items/m1/PlaybackInfo", playbackInfo(true, ""))
	src, err := c.Play(context.Background(), "m1", caps, nil, nil, 0)
	require.NoError(t, err)
	assert.Equal(t, []Track{
		{Index: 3, Label: "English", Lang: "eng", Image: false},
		{Index: 4, Label: "Bulgarian PGS", Lang: "bul", Image: true},
	}, src.Subtitles)
	assert.Equal(t, []Track{
		{Index: 1, Label: "English AAC", Lang: "eng", Default: true},
		{Index: 2, Label: "Bulgarian AC3", Lang: "bul"},
	}, src.Audio)
}

func TestProfileFromCaps(t *testing.T) {
	p := Profile(caps)
	direct := p["DirectPlayProfiles"].([]map[string]any)
	require.Len(t, direct, 1)
	assert.Equal(t, "mp4,webm", direct[0]["Container"])
	assert.Equal(t, "h264,vp9", direct[0]["VideoCodec"])
	assert.Equal(t, "aac,opus", direct[0]["AudioCodec"])
	assert.Equal(t, "Video", direct[0]["Type"])
	assert.Equal(t, []map[string]any{{
		"Container": "mp4", "Type": "Video", "VideoCodec": "h264", "AudioCodec": "aac",
		"Protocol": "hls", "Context": "Streaming", "MaxAudioChannels": "2", "BreakOnNonKeyFrames": true,
	}}, p["TranscodingProfiles"])
	subs := map[string]string{}
	for _, s := range p["SubtitleProfiles"].([]map[string]any) {
		subs[s["Format"].(string)] = s["Method"].(string)
	}
	for _, f := range []string{"vtt", "srt", "subrip", "ass", "ssa"} {
		assert.Equal(t, "External", subs[f], f)
	}
	for _, f := range []string{"pgssub", "dvdsub", "dvbsub", "vobsub"} {
		assert.Equal(t, "Encode", subs[f], f)
	}
}

func TestStripToken(t *testing.T) {
	assert.Equal(t, "/videos/x/main.m3u8?DeviceId=d&PlaySessionId=p",
		StripToken("/videos/x/main.m3u8?DeviceId=d&api_key=S&PlaySessionId=p&APIKEY=S"))
	assert.Equal(t, "hls1/main/0.mp4", StripToken("hls1/main/0.mp4?ApiKey=S"))
	assert.Equal(t, "plain", StripToken("plain"))
}

func TestReportStoppedKillsEncoding(t *testing.T) {
	f, c := loggedIn(t)
	for _, r := range []string{"POST /Sessions/Playing", "POST /Sessions/Playing/Progress", "POST /Sessions/Playing/Stopped", "DELETE /Videos/ActiveEncodings"} {
		f.on(r, func(*http.Request, map[string]any) (int, any) { return 204, nil })
	}
	require.NoError(t, c.Report(context.Background(), "start", "m1", "ms1", "ps1", 0))
	require.NoError(t, c.Report(context.Background(), "progress", "m1", "ms1", "ps1", 10))
	require.Nil(t, f.find("DELETE /Videos/ActiveEncodings"))
	require.NoError(t, c.Report(context.Background(), "stopped", "m1", "ms1", "ps1", 20))
	st := f.find("POST /Sessions/Playing/Stopped").Body
	assert.Equal(t, "m1", st["ItemId"])
	assert.Equal(t, "ps1", st["PlaySessionId"])
	assert.Equal(t, float64(200_000_000), st["PositionTicks"])
	del := f.find("DELETE /Videos/ActiveEncodings")
	require.NotNil(t, del)
	assert.Contains(t, del.Query, "deviceId=dev1")
	assert.Contains(t, del.Query, "playSessionId=ps1")
	assert.Error(t, c.Report(context.Background(), "bogus", "m1", "ms1", "ps1", 0))
}

func TestDoAddsToken(t *testing.T) {
	f, c := loggedIn(t)
	f.on("GET /Videos/m1/stream", func(r *http.Request, _ map[string]any) (int, any) {
		return 206, map[string]any{"range": r.Header.Get("Range")}
	})
	h := http.Header{"Range": {"bytes=0-9"}}
	resp, err := c.Do(context.Background(), http.MethodGet, "/Videos/m1/stream?static=true", h)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 206, resp.StatusCode)
	r := f.find("GET /Videos/m1/stream")
	assert.Contains(t, r.Auth, `Token="TOKEN1"`)
	assert.Equal(t, "static=true", r.Query)
}
