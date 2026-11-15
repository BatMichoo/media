package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testUser = "6f1c2d3e-0000-4000-8000-000000000001"
const vid = "0123456789abcdef0123456789abcdef"

// fakeUpstream answers every request with reply, recording what it got.
type fakeUpstream struct {
	got   []string // "METHOD path?query"
	hdrs  []http.Header
	reply func(path string) *http.Response
}

func (f *fakeUpstream) Do(_ context.Context, method, path string, h http.Header) (*http.Response, error) {
	f.got = append(f.got, method+" "+path)
	f.hdrs = append(f.hdrs, h)
	return f.reply(path), nil
}

func resp(code int, headers map[string]string, body string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func streamRouter(up Upstream) *gin.Engine {
	r := NewRouter(Deps{DB: pinger{}})
	RegisterStream(r.Group("/", RequireUser()), up)
	return r
}

func do(r http.Handler, method, url string, h map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, nil)
	req.Header.Set("X-User-Id", testUser)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestStreamRangePassthrough(t *testing.T) {
	up := &fakeUpstream{reply: func(string) *http.Response {
		return resp(206, map[string]string{"Content-Type": "video/mp4", "Content-Range": "bytes 100-199/1000",
			"Content-Length": "100", "Accept-Ranges": "bytes"}, strings.Repeat("x", 100))
	}}
	w := do(streamRouter(up), "GET", "/stream/Videos/"+vid+"/stream?static=true&mediaSourceId=m", map[string]string{"Range": "bytes=100-199"})
	assert.Equal(t, 206, w.Code)
	assert.Equal(t, "bytes 100-199/1000", w.Header().Get("Content-Range"))
	assert.Equal(t, "bytes", w.Header().Get("Accept-Ranges"))
	assert.Equal(t, "video/mp4", w.Header().Get("Content-Type"))
	assert.Equal(t, 100, w.Body.Len())
	require.Len(t, up.got, 1)
	assert.Equal(t, "GET /Videos/"+vid+"/stream?static=true&mediaSourceId=m", up.got[0])
	assert.Equal(t, "bytes=100-199", up.hdrs[0].Get("Range"))
}

func TestStreamHead(t *testing.T) {
	up := &fakeUpstream{reply: func(string) *http.Response {
		return resp(200, map[string]string{"Content-Length": "1000"}, "")
	}}
	w := do(streamRouter(up), "HEAD", "/stream/videos/"+vid+"/stream?static=true", nil)
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "HEAD /videos/"+vid+"/stream?static=true", up.got[0])
}

func TestStreamRejectsOtherPaths(t *testing.T) {
	up := &fakeUpstream{reply: func(string) *http.Response { return resp(200, nil, "secret") }}
	r := streamRouter(up)
	for _, p := range []string{"/stream/Users/x", "/stream/System/Info", "/stream/videos/../Users",
		"/stream/Videos/" + vid + "/../../Users/x", "/stream/Videos/short/stream", "/stream/Videos/" + vid} {
		w := do(r, "GET", p, nil)
		assert.Equal(t, 404, w.Code, p)
	}
	assert.Empty(t, up.got)
}

func TestStreamDropsTokenFromQuery(t *testing.T) {
	up := &fakeUpstream{reply: func(string) *http.Response { return resp(200, nil, "") }}
	do(streamRouter(up), "GET", "/stream/videos/"+vid+"/main.m3u8?a=1&api_key=FROMBROWSER", nil)
	assert.Equal(t, "GET /videos/"+vid+"/main.m3u8?a=1", up.got[0])
}

func TestPlaylistTokenStripped(t *testing.T) {
	playlist := "#EXTM3U\n#EXT-X-VERSION:7\n" +
		"#EXT-X-MAP:URI=\"/videos/" + vid + "/hls1/main/-1.mp4?ApiKey=SECRET&x=1\"\n" +
		"#EXTINF:3.0,\n" +
		"hls1/main/0.mp4?DeviceId=d&api_key=SECRET\n" +
		"main.m3u8?DeviceId=d&api_key=SECRET&PlaySessionId=p\n"
	up := &fakeUpstream{reply: func(string) *http.Response {
		return resp(200, map[string]string{"Content-Type": "application/vnd.apple.mpegurl", "Content-Length": "999"}, playlist)
	}}
	w := do(streamRouter(up), "GET", "/stream/videos/"+vid+"/master.m3u8?PlaySessionId=p", nil)
	assert.Equal(t, 200, w.Code)
	body := w.Body.String()
	assert.NotContains(t, body, "SECRET")
	assert.Contains(t, body, `URI="/stream/videos/`+vid+`/hls1/main/-1.mp4?x=1"`)
	assert.Contains(t, body, "\nhls1/main/0.mp4?DeviceId=d\n")
	assert.Contains(t, body, "\nmain.m3u8?DeviceId=d&PlaySessionId=p\n")
	assert.Empty(t, w.Header().Get("Content-Length"))
}

func TestPlaylistByExtension(t *testing.T) {
	up := &fakeUpstream{reply: func(string) *http.Response {
		return resp(200, map[string]string{"Content-Type": "application/octet-stream"}, "a.ts?api_key=SECRET\n")
	}}
	w := do(streamRouter(up), "GET", "/stream/videos/"+vid+"/main.m3u8", nil)
	assert.NotContains(t, w.Body.String(), "SECRET")
}

func TestNoTokenInHeaders(t *testing.T) {
	up := &fakeUpstream{reply: func(string) *http.Response {
		return resp(200, map[string]string{"Set-Cookie": "x=1", "X-Emby-Token": "SECRET", "X-Other": "y", "Content-Type": "video/mp4"}, "v")
	}}
	w := do(streamRouter(up), "GET", "/stream/Videos/"+vid+"/stream", nil)
	assert.Empty(t, w.Header().Get("Set-Cookie"))
	assert.Empty(t, w.Header().Get("X-Emby-Token"))
	assert.Empty(t, w.Header().Get("X-Other"))
	// Only the allowed request headers go upstream.
	up2 := &fakeUpstream{reply: func(string) *http.Response { return resp(200, nil, "") }}
	do(streamRouter(up2), "GET", "/stream/Videos/"+vid+"/stream", map[string]string{"Cookie": "homeserver_theme=dark", "Range": "bytes=0-1"})
	assert.Empty(t, up2.hdrs[0].Get("Cookie"))
	assert.Empty(t, up2.hdrs[0].Get("X-User-Id"))
	assert.Equal(t, "bytes=0-1", up2.hdrs[0].Get("Range"))
}

func TestImageProxy(t *testing.T) {
	up := &fakeUpstream{reply: func(string) *http.Response {
		return resp(200, map[string]string{"Content-Type": "image/jpeg"}, "jpg")
	}}
	r := streamRouter(up)
	w := do(r, "GET", "/images/"+vid+"/Primary?w=5000", nil)
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "image/jpeg", w.Header().Get("Content-Type"))
	assert.Equal(t, "private, max-age=86400", w.Header().Get("Cache-Control"))
	assert.Equal(t, "GET /Items/"+vid+"/Images/Primary?maxWidth=1920&quality=90", up.got[0])

	do(r, "GET", "/images/"+vid+"/Backdrop?w=10", nil)
	assert.Equal(t, "GET /Items/"+vid+"/Images/Backdrop?maxWidth=100&quality=90", up.got[1])
	do(r, "GET", "/images/"+vid+"/Thumb", nil)
	assert.Equal(t, "GET /Items/"+vid+"/Images/Thumb?maxWidth=400&quality=90", up.got[2])

	for _, p := range []string{"/images/nothex/Primary", "/images/" + vid + "/Logo"} {
		assert.Equal(t, 404, do(r, "GET", p, nil).Code, p)
	}
	assert.Len(t, up.got, 3)
}

func TestStreamNeedsUser(t *testing.T) {
	up := &fakeUpstream{reply: func(string) *http.Response { return resp(200, nil, "") }}
	r := streamRouter(up)
	for _, id := range []string{"", "not-a-uuid"} {
		req := httptest.NewRequest("GET", "/stream/Videos/"+vid+"/stream", nil)
		if id != "" {
			req.Header.Set("X-User-Id", id)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, 401, w.Code)
	}
	assert.Empty(t, up.got)
	// /healthz needs no user.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	assert.Equal(t, 200, w.Code)
}

func TestCurrentUserName(t *testing.T) {
	r := NewRouter(Deps{DB: pinger{}})
	var got string
	r.GET("/me", RequireUser(), func(c *gin.Context) { got = CurrentUser(c).Name })
	req := httptest.NewRequest("GET", "/me", nil)
	req.Header.Set("X-User-Id", testUser)
	req.Header.Set("X-User-Name", "%D0%98%D0%B2%D0%B0%D0%BD") // Иван
	r.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "Иван", got)
}

func TestStreamURL(t *testing.T) {
	assert.Equal(t, "/stream/videos/x/master.m3u8?a=1", StreamURL("/videos/x/master.m3u8?a=1&api_key=S"))
}
