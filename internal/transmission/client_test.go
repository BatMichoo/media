package transmission

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type call struct {
	Method    string         `json:"method"`
	Arguments map[string]any `json:"arguments"`
}

// fake is a Transmission RPC endpoint that records calls and answers
// with reply(call).
type fake struct {
	mu        sync.Mutex
	calls     []call
	conflicts int
	reply     func(call) map[string]any
	status    int // forced HTTP status, 0 = normal
}

func (f *fake) server(t *testing.T) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path != "/transmission/rpc" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		if u, p, ok := r.BasicAuth(); !ok || u != "media" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != "abc" {
			f.conflicts++
			w.Header().Set("X-Transmission-Session-Id", "abc")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var c call
		require.NoError(t, json.NewDecoder(r.Body).Decode(&c))
		f.calls = append(f.calls, c)
		resp := map[string]any{"result": "success", "arguments": map[string]any{}}
		if f.reply != nil {
			resp = f.reply(c)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &Client{URL: srv.URL, User: "media", Password: "pw", HTTP: srv.Client()}
}

func ok(args map[string]any) map[string]any {
	return map[string]any{"result": "success", "arguments": args}
}

func TestSessionIDHandshake(t *testing.T) {
	f := &fake{reply: func(call) map[string]any {
		return ok(map[string]any{"torrent-added": map[string]any{"hashString": "ABC", "name": "x"}})
	}}
	_, c := f.server(t)
	_, _, _, err := c.Add(context.Background(), "magnet:?xt=1", nil, "/data/downloads")
	require.NoError(t, err)
	assert.Equal(t, 1, f.conflicts)
	_, _, _, err = c.Add(context.Background(), "magnet:?xt=2", nil, "/data/downloads")
	require.NoError(t, err)
	assert.Equal(t, 1, f.conflicts, "session id reused")
	assert.Len(t, f.calls, 2)
}

func TestBasicAuth(t *testing.T) {
	f := &fake{}
	_, c := f.server(t)
	c.Password = "wrong"
	_, err := c.Get(context.Background(), []string{"a"})
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestAddMagnet(t *testing.T) {
	f := &fake{reply: func(call) map[string]any {
		return ok(map[string]any{"torrent-added": map[string]any{"hashString": "ABCDEF", "name": "Dune.2021"}})
	}}
	_, c := f.server(t)
	hash, name, dup, err := c.Add(context.Background(), "magnet:?xt=urn:btih:abcdef", nil, "/data/downloads")
	require.NoError(t, err)
	assert.Equal(t, "abcdef", hash)
	assert.Equal(t, "Dune.2021", name)
	assert.False(t, dup)
	require.Len(t, f.calls, 1)
	assert.Equal(t, "torrent-add", f.calls[0].Method)
	assert.Equal(t, map[string]any{"filename": "magnet:?xt=urn:btih:abcdef", "download-dir": "/data/downloads"}, f.calls[0].Arguments)
}

func TestAddFileDuplicate(t *testing.T) {
	f := &fake{reply: func(call) map[string]any {
		return ok(map[string]any{"torrent-duplicate": map[string]any{"hashString": "aa", "name": "Show"}})
	}}
	_, c := f.server(t)
	file := []byte("d8:announce...e")
	hash, _, dup, err := c.Add(context.Background(), "", file, "/data/downloads")
	require.NoError(t, err)
	assert.Equal(t, "aa", hash)
	assert.True(t, dup)
	assert.Equal(t, base64.StdEncoding.EncodeToString(file), f.calls[0].Arguments["metainfo"])
	assert.NotContains(t, f.calls[0].Arguments, "filename")
}

func TestAddNeedsExactlyOne(t *testing.T) {
	f := &fake{}
	_, c := f.server(t)
	_, _, _, err := c.Add(context.Background(), "", nil, "/d")
	assert.Error(t, err)
	_, _, _, err = c.Add(context.Background(), "magnet:?x", []byte("x"), "/d")
	assert.Error(t, err)
	assert.Empty(t, f.calls)
}

func TestGetFields(t *testing.T) {
	done := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	f := &fake{reply: func(call) map[string]any {
		return ok(map[string]any{"torrents": []any{
			map[string]any{"hashString": "AAA", "name": "a", "percentDone": 0.5, "rateDownload": 1000,
				"error": 0, "errorString": "", "doneDate": 0, "status": 4,
				"files": []any{map[string]any{"name": "a/a.mkv", "length": 42}}},
			map[string]any{"hashString": "bbb", "name": "b", "percentDone": 1.0, "rateDownload": 0,
				"error": 3, "errorString": "No data found", "doneDate": done.Unix(), "status": 0,
				"files": []any{}},
		}})
	}}
	_, c := f.server(t)
	got, err := c.Get(context.Background(), []string{"aaa", "bbb", "ccc"})
	require.NoError(t, err)
	assert.Equal(t, []any{"aaa", "bbb", "ccc"}, f.calls[0].Arguments["ids"])
	assert.ElementsMatch(t, []any{"hashString", "name", "percentDone", "rateDownload", "error",
		"errorString", "files", "doneDate", "status"}, f.calls[0].Arguments["fields"])
	assert.Len(t, got, 2)
	a := got["aaa"]
	assert.Equal(t, 0.5, a.PercentDone)
	assert.Equal(t, int64(1000), a.RateDownload)
	assert.True(t, a.DoneDate.IsZero())
	assert.False(t, a.Stopped)
	assert.Equal(t, "", a.Error)
	assert.Equal(t, []File{{Name: "a/a.mkv", Length: 42}}, a.Files)
	b := got["bbb"]
	assert.True(t, b.Stopped)
	assert.True(t, b.DoneDate.Equal(done))
	assert.Equal(t, "No data found", b.Error)
	_, has := got["ccc"]
	assert.False(t, has)
}

func TestRemove(t *testing.T) {
	for _, del := range []bool{true, false} {
		f := &fake{}
		_, c := f.server(t)
		require.NoError(t, c.Remove(context.Background(), "abc", del))
		assert.Equal(t, "torrent-remove", f.calls[0].Method)
		assert.Equal(t, []any{"abc"}, f.calls[0].Arguments["ids"])
		assert.Equal(t, del, f.calls[0].Arguments["delete-local-data"])
	}
}

func TestRPCError(t *testing.T) {
	f := &fake{reply: func(call) map[string]any {
		return map[string]any{"result": "invalid or corrupt torrent file", "arguments": map[string]any{}}
	}}
	_, c := f.server(t)
	_, _, _, err := c.Add(context.Background(), "", []byte("x"), "/d")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or corrupt torrent file")
}

func TestStop(t *testing.T) {
	f := &fake{}
	_, c := f.server(t)
	require.NoError(t, c.Stop(context.Background(), "abc"))
	assert.Equal(t, "torrent-stop", f.calls[0].Method)
	assert.Equal(t, []any{"abc"}, f.calls[0].Arguments["ids"])
}

func TestDisableSeedLimits(t *testing.T) {
	f := &fake{}
	_, c := f.server(t)
	require.NoError(t, c.DisableSeedLimits(context.Background()))
	assert.Equal(t, "session-set", f.calls[0].Method)
	assert.Equal(t, map[string]any{"seedRatioLimited": false, "idle-seeding-limit-enabled": false}, f.calls[0].Arguments)
}
