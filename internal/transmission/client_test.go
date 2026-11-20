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
	assert.Equal(t, map[string]any{"filename": "magnet:?xt=urn:btih:abcdef", "download-dir": "/data/downloads", "paused": true, "sequential_download": true}, f.calls[0].Arguments)
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
				"sizeWhenDone": 4000, "leftUntilDone": 1000, "eta": 90, "error": 0, "errorString": "", "doneDate": 0, "status": 4, "sequential_download": true,
				"files":     []any{map[string]any{"name": "a/a.mkv", "length": 42}, map[string]any{"name": "a/x.exe", "length": 1}},
				"fileStats": []any{map[string]any{"wanted": true}, map[string]any{"wanted": false}}},
			map[string]any{"hashString": "bbb", "name": "b", "percentDone": 1.0, "rateDownload": 0,
				"sizeWhenDone": 0, "leftUntilDone": 0, "eta": -1, "error": 3, "errorString": "No data found", "doneDate": done.Unix(), "status": 0,
				"files": []any{}},
		}})
	}}
	_, c := f.server(t)
	got, err := c.Get(context.Background(), []string{"aaa", "bbb", "ccc"})
	require.NoError(t, err)
	assert.Equal(t, []any{"aaa", "bbb", "ccc"}, f.calls[0].Arguments["ids"])
	assert.ElementsMatch(t, []any{"hashString", "name", "percentDone", "rateDownload", "error",
		"errorString", "files", "doneDate", "status", "sizeWhenDone", "leftUntilDone", "eta", "sequential_download", "fileStats",
		"rateUpload", "uploadedEver", "uploadRatio", "peersConnected", "peersSendingToUs", "queuePosition", "trackerStats"},
		f.calls[0].Arguments["fields"])
	assert.Len(t, got, 2)
	a := got["aaa"]
	assert.Equal(t, 0.5, a.PercentDone)
	assert.Equal(t, int64(1000), a.RateDownload)
	assert.Equal(t, int64(4000), a.Size)
	assert.Equal(t, int64(1000), a.Left)
	assert.Equal(t, 90*time.Second, a.ETA)
	assert.True(t, a.DoneDate.IsZero())
	assert.False(t, a.Stopped)
	assert.True(t, a.Sequential)
	assert.Equal(t, "", a.Error)
	assert.Equal(t, []File{{Name: "a/a.mkv", Length: 42, Wanted: true}, {Name: "a/x.exe", Length: 1}}, a.Files)
	b := got["bbb"]
	assert.True(t, b.Stopped)
	assert.False(t, b.Sequential, "older Transmission leaves the field out")
	// -1 and -2 mean Transmission can't tell.
	assert.Equal(t, time.Duration(0), b.ETA)
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

func TestStartAndSequential(t *testing.T) {
	f := &fake{}
	_, c := f.server(t)
	require.NoError(t, c.Start(context.Background(), "abc"))
	assert.Equal(t, "torrent-start", f.calls[0].Method)
	assert.Equal(t, []any{"abc"}, f.calls[0].Arguments["ids"])
	require.NoError(t, c.SetSequential(context.Background(), "abc", true))
	assert.Equal(t, "torrent-set", f.calls[1].Method)
	assert.Equal(t, map[string]any{"ids": []any{"abc"}, "sequential_download": true}, f.calls[1].Arguments)
}

func TestSetWanted(t *testing.T) {
	f := &fake{}
	_, c := f.server(t)
	require.NoError(t, c.SetWanted(context.Background(), "abc", []int{0}, []int{1, 2}))
	assert.Equal(t, map[string]any{"ids": []any{"abc"}, "files-wanted": []any{0.0}, "files-unwanted": []any{1.0, 2.0}}, f.calls[0].Arguments)
	require.NoError(t, c.SetWanted(context.Background(), "abc", nil, []int{3}))
	assert.Equal(t, map[string]any{"ids": []any{"abc"}, "files-unwanted": []any{3.0}}, f.calls[1].Arguments,
		"an empty list would mean every file")
	require.NoError(t, c.SetWanted(context.Background(), "abc", nil, nil))
	assert.Len(t, f.calls, 2, "nothing to change, nothing sent")
}

func TestConfigureSession(t *testing.T) {
	// Unfinished downloads go straight to the media folder too (the image's
	// default "incomplete" folder is inside the container), and the app
	// decides when seeding stops.
	f := &fake{}
	_, c := f.server(t)
	require.NoError(t, c.ConfigureSession(context.Background(), "/data/downloads"))
	assert.Equal(t, "session-set", f.calls[0].Method)
	assert.Equal(t, map[string]any{"seedRatioLimited": false, "idle-seeding-limit-enabled": false,
		"incomplete-dir-enabled": false, "download-dir": "/data/downloads"}, f.calls[0].Arguments)
}

func TestAddPausedAndMove(t *testing.T) {
	f := &fake{reply: func(c call) map[string]any {
		if c.Method == "torrent-add" {
			return ok(map[string]any{"torrent-added": map[string]any{"hashString": "AB", "name": "x"}})
		}
		return ok(map[string]any{})
	}}
	_, c := f.server(t)
	_, _, _, err := c.Add(context.Background(), "magnet:?xt=1", nil, "/data/downloads")
	require.NoError(t, err)
	assert.Equal(t, true, f.calls[0].Arguments["paused"], "added paused, so nothing lands before it has its own folder")
	assert.Equal(t, true, f.calls[0].Arguments["sequential_download"], "pieces in order")
	require.NoError(t, c.Place(context.Background(), "ab", "/data/downloads/ab"))
	assert.Equal(t, "torrent-set-location", f.calls[1].Method)
	assert.Equal(t, map[string]any{"ids": []any{"ab"}, "location": "/data/downloads/ab", "move": false}, f.calls[1].Arguments)
	assert.Equal(t, "torrent-start", f.calls[2].Method)
	assert.Equal(t, []any{"ab"}, f.calls[2].Arguments["ids"])
}

func TestErrorKinds(t *testing.T) {
	f := &fake{reply: func(call) map[string]any {
		return ok(map[string]any{"torrents": []any{
			map[string]any{"hashString": "a", "error": 2, "errorString": "unregistered torrent", "percentDone": 1.0},
			map[string]any{"hashString": "b", "error": 3, "errorString": "No data found"},
		}})
	}}
	_, c := f.server(t)
	got, err := c.Get(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	assert.Equal(t, "unregistered torrent", got["a"].Error)
	assert.False(t, got["a"].LocalError, "a tracker problem")
	assert.True(t, got["b"].LocalError)
}

func TestGetStats(t *testing.T) {
	f := &fake{reply: func(call) map[string]any {
		return ok(map[string]any{"torrents": []any{map[string]any{
			"hashString": "A", "rateUpload": 1000, "uploadedEver": 5e9, "uploadRatio": 0.5,
			"peersConnected": 3, "peersSendingToUs": 0, "status": 3, "queuePosition": 1,
			"trackerStats": []any{
				map[string]any{"seederCount": -1},
				map[string]any{"seederCount": 45, "hasAnnounced": true, "lastAnnounceSucceeded": false, "lastAnnounceResult": "Connection failed"},
			},
		}}})
	}}
	_, c := f.server(t)
	got, err := c.Get(context.Background(), []string{"a"})
	require.NoError(t, err)
	a := got["a"]
	assert.Equal(t, int64(1000), a.RateUpload)
	assert.Equal(t, int64(5e9), a.Uploaded)
	assert.Equal(t, 0.5, a.Ratio)
	assert.Equal(t, 3, a.Peers)
	assert.Equal(t, 0, a.PeersSending)
	assert.True(t, a.Queued)
	assert.False(t, a.Stopped)
	assert.Equal(t, 2, a.QueuePos)
	assert.Equal(t, 45, a.Seeders)
	assert.Equal(t, "Connection failed", a.TrackerError)
	assert.Contains(t, f.calls[0].Arguments["fields"], "trackerStats")
}

func TestGetTrackerErrorOnlyWhenNoneSucceeded(t *testing.T) {
	f := &fake{reply: func(call) map[string]any {
		return ok(map[string]any{"torrents": []any{map[string]any{
			"hashString": "A", "status": 4,
			"trackerStats": []any{
				map[string]any{"seederCount": 10, "hasAnnounced": true, "lastAnnounceSucceeded": false, "lastAnnounceResult": "timed out"},
				map[string]any{"seederCount": 5, "hasAnnounced": true, "lastAnnounceSucceeded": true},
			},
		}}})
	}}
	_, c := f.server(t)
	got, err := c.Get(context.Background(), []string{"a"})
	require.NoError(t, err)
	assert.Equal(t, "", got["a"].TrackerError)
	assert.Equal(t, 10, got["a"].Seeders)
	assert.False(t, got["a"].Queued)
}

func TestGetRatioUnknown(t *testing.T) {
	f := &fake{reply: func(call) map[string]any {
		return ok(map[string]any{"torrents": []any{map[string]any{"hashString": "A", "uploadRatio": -1, "status": 4}}})
	}}
	_, c := f.server(t)
	got, err := c.Get(context.Background(), []string{"a"})
	require.NoError(t, err)
	assert.Equal(t, 0.0, got["a"].Ratio)
	assert.Equal(t, -1, got["a"].Seeders)
}

func TestStartNow(t *testing.T) {
	f := &fake{}
	_, c := f.server(t)
	require.NoError(t, c.StartNow(context.Background(), "abc"))
	assert.Equal(t, "torrent-start-now", f.calls[0].Method)
	assert.Equal(t, []any{"abc"}, f.calls[0].Arguments["ids"])
}

func TestSetSequentialOff(t *testing.T) {
	f := &fake{}
	_, c := f.server(t)
	require.NoError(t, c.SetSequential(context.Background(), "abc", false))
	assert.Equal(t, map[string]any{"ids": []any{"abc"}, "sequential_download": false}, f.calls[0].Arguments)
}

func TestPortOpen(t *testing.T) {
	f := &fake{reply: func(call) map[string]any { return ok(map[string]any{"port-is-open": true}) }}
	_, c := f.server(t)
	open, err := c.PortOpen(context.Background())
	require.NoError(t, err)
	assert.True(t, open)
	assert.Equal(t, "port-test", f.calls[0].Method)
}
