// Command media is the home server's media app.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/BatMichoo/media/internal/config"
	"github.com/BatMichoo/media/internal/db"
	"github.com/BatMichoo/media/internal/downloads"
	"github.com/BatMichoo/media/internal/handlers"
	"github.com/BatMichoo/media/internal/jellyfin"
	"github.com/BatMichoo/media/internal/library"
	"github.com/BatMichoo/media/internal/transmission"
	"github.com/BatMichoo/media/internal/watching"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DB)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer database.Close()

	// Streams can run for hours, so no overall timeout; only waiting for
	// Jellyfin to start answering is bounded.
	streaming := &http.Client{Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
	}}
	sum := sha256.Sum256([]byte(cfg.LanDomain))
	jf := &jellyfin.Client{URL: cfg.JellyfinURL, User: cfg.JellyfinUser, Password: cfg.JellyfinPassword,
		HTTP: streaming, DeviceID: "media-" + hex.EncodeToString(sum[:])[:16], HWAccel: cfg.JellyfinHWAccel}
	tr := &transmission.Client{URL: cfg.TransmissionURL, User: cfg.TransmissionUser, Password: cfg.TransmissionPassword,
		HTTP: &http.Client{Timeout: 30 * time.Second}}

	linkErr := library.CheckLinkable(cfg.DataDir)
	if linkErr != nil {
		log.Printf("media folder: %v", linkErr)
	}

	dl := downloads.New(database, tr, jf, cfg.DataDir)
	w := watching.New(database, jf)
	go retry(ctx, "jellyfin setup", func(ctx context.Context) error {
		return jf.Setup(ctx, filepath.Join(cfg.DataDir, "library"))
	})
	go retry(ctx, "transmission seeding limits", tr.DisableSeedLimits)
	go dl.Run(ctx, cfg.PollInterval)

	router, err := handlers.NewWebRouter(handlers.Deps{
		DB: database, Catalog: jf, Player: jf, Downloads: dl, Watching: w, Upstream: jf,
		LanDomain: cfg.LanDomain, AuthURL: cfg.AuthURL(),
		LinkProblem: func() error { return linkErr },
	})
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	os.Exit(serve(ctx, cfg, router))
}

// retry runs f until it succeeds (every 10 seconds), logging the first
// failure and then every minute or so.
func retry(ctx context.Context, what string, f func(context.Context) error) {
	for attempt := 0; ; attempt++ {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		err := f(c)
		cancel()
		if err == nil {
			log.Printf("%s: done", what)
			return
		}
		if attempt%6 == 0 {
			log.Printf("%s: %v (trying again)", what, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func serve(ctx context.Context, cfg *config.Config, h http.Handler) int {
	srv := &http.Server{Addr: cfg.Addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("media listening on %s", cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print(err)
		return 1
	}
	return 0
}
