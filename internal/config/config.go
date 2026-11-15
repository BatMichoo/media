// Package config reads the media app's settings from the environment.
// Only this package reads the environment.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DBConfig is how to reach the media database.
type DBConfig struct {
	Host, Name, User, Pass, SSLMode string
	Port                            int
}

// DSN is a lib/pq connection string.
func (d DBConfig) DSN() string {
	pass := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(d.Pass)
	return fmt.Sprintf("host=%s port=%d user=%s password='%s' dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, pass, d.Name, d.SSLMode)
}

// Config is everything the app needs.
type Config struct {
	DB        DBConfig
	LanDomain string // e.g. homeserver.lan; cookies are set for it
	HTTPSPort int    // where nginx serves HTTPS (443 unless changed)
	Addr      string // where to listen, e.g. :8080

	JellyfinURL, JellyfinUser, JellyfinPassword             string
	JellyfinHWAccel                                         string // vaapi (Intel GPU) or none (CPU)
	TransmissionURL, TransmissionUser, TransmissionPassword string

	DataDir      string        // holds downloads/ and library/
	PollInterval time.Duration // how often Transmission is checked
}

// AuthURL is the login service, e.g. https://auth.homeserver.lan.
func (c *Config) AuthURL() string {
	u := "https://auth." + c.LanDomain
	if c.HTTPSPort != 443 {
		u += ":" + strconv.Itoa(c.HTTPSPort)
	}
	return u
}

// Load reads the settings through getenv (os.Getenv in main).
func Load(getenv func(string) string) (*Config, error) {
	var errs []error
	required := func(k string) string {
		v := strings.TrimSpace(getenv(k))
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is not set", k))
		}
		return v
	}
	orDefault := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	port := func(k, def string) int {
		v := orDefault(k, def)
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			errs = append(errs, fmt.Errorf("%s %q: must be a port number", k, v))
		}
		return n
	}

	c := &Config{
		DB: DBConfig{
			Host:    required("DB_HOST"),
			Port:    port("DB_PORT", "5432"),
			Name:    required("DB_NAME"),
			User:    required("DB_USER"),
			Pass:    required("DB_PASS"),
			SSLMode: orDefault("DB_SSLMODE", "disable"),
		},
		LanDomain: required("LAN_DOMAIN"),
		HTTPSPort: port("HTTPS_PORT", "443"),
		Addr:      orDefault("ADDR", ":8080"),

		JellyfinURL:      strings.TrimRight(orDefault("JELLYFIN_URL", "http://jellyfin:8096"), "/"),
		JellyfinUser:     required("JELLYFIN_USER"),
		JellyfinPassword: required("JELLYFIN_PASSWORD"),

		JellyfinHWAccel: orDefault("JELLYFIN_HWACCEL", "vaapi"),

		TransmissionURL:      strings.TrimRight(orDefault("TRANSMISSION_URL", "http://transmission:9091"), "/"),
		TransmissionUser:     required("TRANSMISSION_USER"),
		TransmissionPassword: required("TRANSMISSION_PASSWORD"),

		DataDir: orDefault("DATA_DIR", "/data"),
	}
	if c.JellyfinHWAccel != "vaapi" && c.JellyfinHWAccel != "none" {
		errs = append(errs, fmt.Errorf("JELLYFIN_HWACCEL %q: must be vaapi or none", c.JellyfinHWAccel))
	}
	interval := orDefault("POLL_INTERVAL", "30s")
	d, err := time.ParseDuration(interval)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("POLL_INTERVAL %q: must be a duration like 30s", interval))
	case d <= 0:
		errs = append(errs, fmt.Errorf("POLL_INTERVAL %q: must be more than 0", interval))
	}
	c.PollInterval = d

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}
