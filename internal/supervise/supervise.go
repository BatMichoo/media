// Package supervise runs the app's startup work against services that
// may not be up yet: trying again until it works, and doing it again
// when it stops holding (Jellyfin's setup after its login is lost).
package supervise

import (
	"context"
	"log"
	"time"
)

// Loop holds the timings. The zero value is not usable; use New.
type Loop struct {
	RetryEvery time.Duration // the wait after a failed attempt
	Timeout    time.Duration // how long one attempt may take
	LogEvery   int           // failures are logged on the 1st, then every LogEvery-th
	CheckEvery time.Duration // how often KeepReady checks it still holds
	Log        *log.Logger
}

// New is a Loop with the app's timings: an attempt every 10 seconds of at
// most a minute, failures logged about once a minute, and a check every
// 30 seconds.
func New() *Loop {
	return &Loop{RetryEvery: 10 * time.Second, Timeout: time.Minute, LogEvery: 6,
		CheckEvery: 30 * time.Second, Log: log.Default()}
}

// Retry runs f until it succeeds or ctx ends, each attempt bounded by
// Timeout. It reports whether f succeeded.
func (l *Loop) Retry(ctx context.Context, what string, f func(context.Context) error) bool {
	for attempt := 0; ; attempt++ {
		c, cancel := context.WithTimeout(ctx, l.Timeout)
		err := f(c)
		cancel()
		if err == nil {
			l.Log.Printf("%s: done", what)
			return true
		}
		if l.LogEvery <= 1 || attempt%l.LogEvery == 0 {
			l.Log.Printf("%s: %v (trying again)", what, err)
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(l.RetryEvery):
		}
	}
}

// KeepReady runs setup (with Retry) until ctx ends: once at the start,
// and again whenever ready turns false (for Jellyfin: its login failed,
// because its settings were wiped or the account changed).
func (l *Loop) KeepReady(ctx context.Context, what string, setup func(context.Context) error, ready func() bool) {
	for {
		if !l.Retry(ctx, what, setup) {
			return
		}
		for ready() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(l.CheckEvery):
			}
		}
		if ctx.Err() != nil {
			return
		}
		l.Log.Printf("%s: lost, setting up again", what)
	}
}
