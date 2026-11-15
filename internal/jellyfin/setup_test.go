package jellyfin

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// configured makes the fake look like a Jellyfin that is already set up.
func configured(f *fake, libraries []any, encoding map[string]any) {
	f.json("GET /System/Info/Public", map[string]any{"StartupWizardCompleted": true})
	f.json("GET /Library/VirtualFolders", libraries)
	f.json("GET /System/Configuration/encoding", encoding)
	f.on("POST /Library/VirtualFolders", func(*http.Request, map[string]any) (int, any) { return 204, nil })
	f.on("POST /System/Configuration/encoding", func(*http.Request, map[string]any) (int, any) { return 204, nil })
}

func goodEncoding() map[string]any {
	return map[string]any{
		"HardwareAccelerationType":       "vaapi",
		"VaapiDevice":                    "/dev/dri/renderD128",
		"EnableHardwareEncoding":         true,
		"HardwareDecodingCodecs":         []any{"h264", "hevc", "vp9", "mpeg2video", "vc1"},
		"EnableDecodingColorDepth10Hevc": true,
		"EnableTonemapping":              false,
		"EncodingThreadCount":            -1, // other fields are kept as they are
	}
}

func bothLibraries() []any {
	return []any{
		map[string]any{"Name": "Movies", "CollectionType": "movies", "Locations": []any{"/data/library/Movies"}},
		map[string]any{"Name": "Shows", "CollectionType": "tvshows", "Locations": []any{"/data/library/Shows"}},
	}
}

func TestSetupFreshServer(t *testing.T) {
	f, c := newFake(t)
	configured(f, []any{}, map[string]any{"HardwareAccelerationType": "none", "EncodingThreadCount": -1})
	f.json("GET /System/Info/Public", map[string]any{"StartupWizardCompleted": false})
	for _, r := range []string{"POST /Startup/Configuration", "POST /Startup/User", "POST /Startup/RemoteAccess", "POST /Startup/Complete"} {
		f.on(r, func(*http.Request, map[string]any) (int, any) { return 204, nil })
	}
	f.json("GET /Startup/User", map[string]any{"Name": "root"})

	require.NoError(t, c.Setup(context.Background(), "/data/library"))
	assert.True(t, c.Ready())

	assert.Equal(t, []string{
		"POST /Startup/Configuration", "POST /Startup/User", "POST /Startup/RemoteAccess", "POST /Startup/Complete",
		"POST /Users/AuthenticateByName",
		"POST /Library/VirtualFolders", "POST /Library/VirtualFolders",
		"POST /System/Configuration/encoding",
	}, f.posts())
	// GET /Startup/User comes before its POST.
	calls := f.calls()
	assert.Less(t, indexOf(calls, "GET /Startup/User"), indexOf(calls, "POST /Startup/User"))

	cfg := f.find("POST /Startup/Configuration").Body
	assert.Equal(t, "en", cfg["PreferredMetadataLanguage"])
	assert.Equal(t, "US", cfg["MetadataCountryCode"])
	assert.Equal(t, map[string]any{"Name": "media", "Password": "pw"}, f.find("POST /Startup/User").Body)
	assert.Equal(t, false, f.find("POST /Startup/RemoteAccess").Body["EnableAutomaticPortMapping"])

	var queries []string
	for _, r := range f.reqs {
		if r.Method+" "+r.Path == "POST /Library/VirtualFolders" {
			queries = append(queries, r.Query)
		}
	}
	assert.Contains(t, queries[0], "name=Movies")
	assert.Contains(t, queries[0], "collectionType=movies")
	assert.Contains(t, queries[0], "paths=%2Fdata%2Flibrary%2FMovies")
	assert.Contains(t, queries[1], "name=Shows")
	assert.Contains(t, queries[1], "collectionType=tvshows")

	enc := f.find("POST /System/Configuration/encoding").Body
	for k, v := range goodEncoding() {
		assert.True(t, sameJSON(v, enc[k]), "%s: %v", k, enc[k])
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestSetupRepeatChangesNothing(t *testing.T) {
	f, c := newFake(t)
	configured(f, bothLibraries(), goodEncoding())
	require.NoError(t, c.Setup(context.Background(), "/data/library"))
	assert.Equal(t, []string{"POST /Users/AuthenticateByName"}, f.posts())
	assert.True(t, c.Ready())
}

func TestSetupNoHardware(t *testing.T) {
	f, c := newFake(t)
	c.HWAccel = "none"
	configured(f, bothLibraries(), goodEncoding())
	require.NoError(t, c.Setup(context.Background(), "/data/library"))
	enc := f.find("POST /System/Configuration/encoding").Body
	assert.Equal(t, "none", enc["HardwareAccelerationType"])
	assert.Equal(t, false, enc["EnableHardwareEncoding"])
}

func TestSetupAddsOnlyMissingLibrary(t *testing.T) {
	f, c := newFake(t)
	configured(f, bothLibraries()[:1], goodEncoding())
	require.NoError(t, c.Setup(context.Background(), "/data/library"))
	assert.Equal(t, []string{"POST /Users/AuthenticateByName", "POST /Library/VirtualFolders"}, f.posts())
	assert.Contains(t, f.find("POST /Library/VirtualFolders").Query, "name=Shows")
}

func TestSetupFailsNotReady(t *testing.T) {
	_, c := newFake(t) // no /System/Info/Public: 404
	assert.Error(t, c.Setup(context.Background(), "/data/library"))
	assert.False(t, c.Ready())
}

func TestTokenHeaderAndRelogin(t *testing.T) {
	f, c := newFake(t)
	configured(f, bothLibraries(), goodEncoding())
	require.NoError(t, c.Setup(context.Background(), "/data/library"))

	// The token expires once: the next call gets 401, logs in again, retries.
	expired := true
	f.on("GET /Items/abc", func(r *http.Request, _ map[string]any) (int, any) {
		if expired {
			expired = false
			return 401, nil
		}
		return 200, map[string]any{"Id": "abc", "Type": "Movie", "Name": "x"}
	})
	it, err := c.Item(context.Background(), "abc")
	require.NoError(t, err)
	assert.Equal(t, "abc", it.ID)
	r := f.find("GET /Items/abc")
	assert.Contains(t, r.Auth, `Token="TOKEN1"`)
	assert.Contains(t, r.Auth, `Client="media"`)
	assert.Contains(t, r.Auth, `DeviceId="dev1"`)
	logins := 0
	for _, c := range f.calls() {
		if c == "POST /Users/AuthenticateByName" {
			logins++
		}
	}
	assert.Equal(t, 2, logins)
}
