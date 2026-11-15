package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
)

// Setup gets Jellyfin to the state the app needs, changing only what
// differs: the first-run wizard (service account), the Movies and Shows
// libraries under libraryRoot, and hardware video conversion on the Intel
// GPU. Repeating it on a configured server only logs in.
func (c *Client) Setup(ctx context.Context, libraryRoot string) error {
	var info struct{ StartupWizardCompleted bool }
	if err := c.send(ctx, http.MethodGet, "/System/Info/Public", nil, &info, false); err != nil {
		return fmt.Errorf("jellyfin not reachable: %w", err)
	}
	if !info.StartupWizardCompleted {
		if err := c.wizard(ctx); err != nil {
			return fmt.Errorf("jellyfin first-run setup: %w", err)
		}
	}
	if err := c.Login(ctx); err != nil {
		return err
	}
	if err := c.ensureLibraries(ctx, libraryRoot); err != nil {
		return fmt.Errorf("jellyfin libraries: %w", err)
	}
	if err := c.ensureHardware(ctx); err != nil {
		return fmt.Errorf("jellyfin hardware conversion: %w", err)
	}
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	return nil
}

func (c *Client) wizard(ctx context.Context) error {
	steps := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/Startup/Configuration", map[string]any{
			"UICulture": "en-US", "MetadataCountryCode": "US", "PreferredMetadataLanguage": "en"}},
		// The first user exists only after this GET.
		{http.MethodGet, "/Startup/User", nil},
		{http.MethodPost, "/Startup/User", map[string]any{"Name": c.User, "Password": c.Password}},
		{http.MethodPost, "/Startup/RemoteAccess", map[string]any{
			"EnableRemoteAccess": true, "EnableAutomaticPortMapping": false}},
		{http.MethodPost, "/Startup/Complete", nil},
	}
	for _, s := range steps {
		if err := c.send(ctx, s.method, s.path, s.body, nil, false); err != nil {
			return err
		}
	}
	return nil
}

type library struct {
	name, collectionType, dir string
}

func (c *Client) ensureLibraries(ctx context.Context, root string) error {
	var have []struct {
		Name           string
		CollectionType string
		Locations      []string
	}
	if err := c.get(ctx, "/Library/VirtualFolders", nil, &have); err != nil {
		return err
	}
	for _, want := range []library{
		{"Movies", "movies", path.Join(root, "Movies")},
		{"Shows", "tvshows", path.Join(root, "Shows")},
	} {
		found := false
		for _, h := range have {
			for _, l := range h.Locations {
				if l == want.dir {
					found = true
				}
			}
		}
		if found {
			continue
		}
		q := url.Values{"name": {want.name}, "collectionType": {want.collectionType},
			"paths": {want.dir}, "refreshLibrary": {"true"}}
		if err := c.post(ctx, "/Library/VirtualFolders", q, map[string]any{"LibraryOptions": map[string]any{}}, nil); err != nil {
			return err
		}
	}
	return nil
}

// hardware is the conversion setup for the server's Intel GPU (VA-API on
// /dev/dri), output H.264, no tone mapping.
var hardware = map[string]any{
	"HardwareAccelerationType":       "vaapi",
	"VaapiDevice":                    "/dev/dri/renderD128",
	"EnableHardwareEncoding":         true,
	"HardwareDecodingCodecs":         []string{"h264", "hevc", "vp9", "mpeg2video", "vc1"},
	"EnableDecodingColorDepth10Hevc": true,
	"EnableTonemapping":              false,
}

// software is conversion on the CPU only (no GPU, or as a fallback).
var software = map[string]any{
	"HardwareAccelerationType": "none",
	"EnableHardwareEncoding":   false,
	"EnableTonemapping":        false,
}

func (c *Client) ensureHardware(ctx context.Context) error {
	var cfg map[string]any
	if err := c.get(ctx, "/System/Configuration/encoding", nil, &cfg); err != nil {
		return err
	}
	want := hardware
	if c.HWAccel == "none" {
		want = software
	}
	changed := false
	for k, v := range want {
		if !sameJSON(cfg[k], v) {
			cfg[k] = v
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return c.post(ctx, "/System/Configuration/encoding", nil, cfg, nil)
}

func sameJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}
