package supervise

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuf is a log buffer safe to read while the loop writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func fast() (*Loop, *syncBuf) {
	buf := &syncBuf{}
	return &Loop{RetryEvery: time.Millisecond, Timeout: time.Second, LogEvery: 3,
		CheckEvery: time.Millisecond, Log: log.New(buf, "", 0)}, buf
}

var errDown = errors.New("down")

func TestRetryUntilItWorks(t *testing.T) {
	l, buf := fast()
	calls := 0
	ok := l.Retry(context.Background(), "thing", func(context.Context) error {
		calls++
		if calls < 5 {
			return errDown
		}
		return nil
	})
	assert.True(t, ok)
	assert.Equal(t, 5, calls)
	// Failures 1 and 4 are logged (every 3rd after the first), then done.
	assert.Equal(t, "thing: down (trying again)\nthing: down (trying again)\nthing: done\n", buf.String())
}

func TestRetryStopsWithContext(t *testing.T) {
	l, _ := fast()
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	ok := l.Retry(ctx, "thing", func(context.Context) error {
		calls++
		if calls == 3 {
			cancel()
		}
		return errDown
	})
	assert.False(t, ok)
	assert.Equal(t, 3, calls)
}

func TestRetryBoundsEachAttempt(t *testing.T) {
	l, _ := fast()
	l.Timeout = 20 * time.Millisecond
	calls := 0
	ok := l.Retry(context.Background(), "thing", func(ctx context.Context) error {
		calls++
		if calls == 1 {
			<-ctx.Done() // a stuck attempt is cut off
			return ctx.Err()
		}
		_, has := ctx.Deadline()
		assert.True(t, has)
		return nil
	})
	assert.True(t, ok)
	assert.Equal(t, 2, calls)
}

func TestKeepReadySetsUpAgainWhenLost(t *testing.T) {
	l, buf := fast()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var setups atomic.Int32
	var ready atomic.Bool
	done := make(chan struct{})
	go func() {
		l.KeepReady(ctx, "jellyfin setup", func(context.Context) error {
			setups.Add(1)
			ready.Store(true)
			return nil
		}, ready.Load)
		close(done)
	}()
	require.Eventually(t, func() bool { return setups.Load() == 1 }, time.Second, time.Millisecond)
	ready.Store(false) // the login was lost
	require.Eventually(t, func() bool { return setups.Load() == 2 }, time.Second, time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("KeepReady didn't stop")
	}
	assert.Equal(t, int32(2), setups.Load())
	assert.Equal(t, 1, strings.Count(buf.String(), "jellyfin setup: lost, setting up again"))
}

func TestKeepReadyStopsWhileFailing(t *testing.T) {
	l, _ := fast()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		l.KeepReady(ctx, "x", func(context.Context) error { return errDown }, func() bool { return false })
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("KeepReady didn't stop")
	}
}
