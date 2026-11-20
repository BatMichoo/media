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
	"sync"
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

func main() { os.Exit(run()) }

// run is the app; it returns the exit code once the server and the
// background work have stopped (main's os.Exit would skip defers).
func run() int {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DB)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
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
	// The background work is waited for before the database closes, so a
	// poll round filing a download finishes recording its links.
	var workers sync.WaitGroup
	defer workers.Wait()
	defer stop() // runs first: ends the workers when the server fails
	goWorker := func(f func()) {
		workers.Add(1)
		go func() { defer workers.Done(); f() }()
	}
	goWorker(func() { keepSetUp(ctx, jf, filepath.Join(cfg.DataDir, "library")) })
	goWorker(func() {
		retry(ctx, "transmission settings", func(ctx context.Context) error {
			return tr.ConfigureSession(ctx, filepath.Join(cfg.DataDir, "downloads"))
		})
	})
	goWorker(func() { dl.Run(ctx, cfg.PollInterval) })

	router, err := handlers.NewWebRouter(handlers.Deps{
		DB: database, Catalog: jf, Player: jf, Downloads: dl, Library: jf, Watching: w, Upstream: jf,
		LanDomain: cfg.LanDomain, AuthURL: cfg.AuthURL(), HomeServerURL: cfg.HomeServerURL(),
		LinkProblem: func() error { return linkErr },
	})
	if err != nil {
		log.Print(err)
		return 1
	}
	return serve(ctx, cfg, router)
}

// keepSetUp sets Jellyfin up, and again whenever it stops being ready (its
// login failed: its settings were wiped or the account changed).
func keepSetUp(ctx context.Context, jf *jellyfin.Client, libraryRoot string) {
	for {
		retry(ctx, "jellyfin setup", func(ctx context.Context) error { return jf.Setup(ctx, libraryRoot) })
		for jf.Ready() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
		if ctx.Err() != nil {
			return
		}
		log.Print("jellyfin: login lost, setting up again")
	}
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
