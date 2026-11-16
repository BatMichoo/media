package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var required = map[string]string{
	"DB_HOST":               "db",
	"DB_NAME":               "media",
	"DB_USER":               "media",
	"DB_PASS":               "secret",
	"LAN_DOMAIN":            "homeserver.lan",
	"JELLYFIN_USER":         "media",
	"JELLYFIN_PASSWORD":     "jf-secret",
	"TRANSMISSION_USER":     "media",
	"TRANSMISSION_PASSWORD": "tr-secret",
}

func env(extra map[string]string) func(string) string {
	return func(k string) string {
		if v, ok := extra[k]; ok {
			return v
		}
		return required[k]
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(nil))
	require.NoError(t, err)
	assert.Equal(t, "/data", c.DataDir)
	assert.Equal(t, 30*time.Second, c.PollInterval)
	assert.Equal(t, "http://jellyfin:8096", c.JellyfinURL)
	assert.Equal(t, "http://transmission:9091", c.TransmissionURL)
	assert.Equal(t, ":8080", c.Addr)
	assert.Equal(t, 443, c.HTTPSPort)
	assert.Equal(t, 5432, c.DB.Port)
	assert.Equal(t, "jf-secret", c.JellyfinPassword)
	assert.Equal(t, "tr-secret", c.TransmissionPassword)
	assert.Equal(t, "vaapi", c.JellyfinHWAccel)
}

func TestLoadHWAccel(t *testing.T) {
	c, err := Load(env(map[string]string{"JELLYFIN_HWACCEL": "none"}))
	require.NoError(t, err)
	assert.Equal(t, "none", c.JellyfinHWAccel)
	_, err = Load(env(map[string]string{"JELLYFIN_HWACCEL": "cuda"}))
	assert.Error(t, err)
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	require.Error(t, err)
	for _, k := range []string{"DB_HOST", "DB_NAME", "DB_USER", "DB_PASS", "LAN_DOMAIN",
		"JELLYFIN_USER", "JELLYFIN_PASSWORD", "TRANSMISSION_USER", "TRANSMISSION_PASSWORD"} {
		assert.Contains(t, err.Error(), k)
	}
}

func TestLoadBadInterval(t *testing.T) {
	for _, v := range []string{"abc", "0s"} {
		_, err := Load(env(map[string]string{"POLL_INTERVAL": v}))
		assert.Error(t, err, v)
	}
}

func TestAuthURL(t *testing.T) {
	c, err := Load(env(nil))
	require.NoError(t, err)
	assert.Equal(t, "https://auth.homeserver.lan", c.AuthURL())
	c, err = Load(env(map[string]string{"HTTPS_PORT": "8443"}))
	require.NoError(t, err)
	assert.Equal(t, "https://auth.homeserver.lan:8443", c.AuthURL())
}

func TestHomeServerURL(t *testing.T) {
	c, err := Load(env(nil))
	require.NoError(t, err)
	assert.Equal(t, "https://homeserver.lan/", c.HomeServerURL())
	c, err = Load(env(map[string]string{"HTTPS_PORT": "8443"}))
	require.NoError(t, err)
	assert.Equal(t, "https://homeserver.lan:8443/", c.HomeServerURL())
	c.LanDomain = ""
	assert.Equal(t, "", c.HomeServerURL(), "no domain: no link")
}
