package downloads

// Why a download isn't moving: stalls, the queue, the peer port.

import (
	"context"
	"sync"
	"time"

	"github.com/BatMichoo/media/internal/model"
	"github.com/BatMichoo/media/internal/transmission"
)

// liveState is what the stuck reasons remember between looks. It has its
// own lock, not Service.mu: List and the port test don't wait for a poll
// round or an edit.
type liveState struct {
	mu           sync.Mutex
	portTried    time.Time // last port test attempt; zero = none yet
	portAnswered bool      // the last attempt answered
	portOpen     bool
	port         int                  // Transmission's peer port, from that test
	stalledSince map[string]time.Time // hash -> when it last stopped moving
}

// Stuck is why a download isn't moving.
type Stuck string

const (
	StuckQueued     Stuck = "queued"      // waiting its turn in Transmission's queue
	StuckTracker    Stuck = "tracker"     // the tracker reports an error
	StuckNoPeers    Stuck = "no_peers"    // nobody to download from
	StuckInOrder    Stuck = "in_order"    // peers, none sending, pieces asked for in order
	StuckNotSending Stuck = "not_sending" // peers, none sending
)

// queueRank is t's place among the queued torrents in live (1 = next),
// 0 when it isn't queued. Transmission's own position counts every
// torrent, queued or not.
func queueRank(live map[string]transmission.Torrent, t transmission.Torrent) int {
	if !t.Queued {
		return 0
	}
	rank := 1
	for _, o := range live {
		if o.Queued && o.QueuePos < t.QueuePos {
			rank++
		}
	}
	return rank
}

// stuckReason is why a downloading torrent isn't moving, "" when it is
// (or isn't downloading). See Download.Stuck.
func stuckReason(state model.State, paused bool, t transmission.Torrent) Stuck {
	switch {
	case state != model.StateDownloading || paused || t.Stopped || t.Checking || t.PercentDone >= 1:
		return ""
	case t.Queued:
		return StuckQueued
	case t.RateDownload > 0 || t.PeersSending > 0:
		return ""
	case t.TrackerError != "":
		return StuckTracker
	case t.Peers == 0:
		return StuckNoPeers
	case t.Sequential:
		return StuckInOrder
	}
	return StuckNotSending
}

// stuck is reason once the download has been stalled for StallAfter, ""
// before that: a just-started torrent often waits a moment for peers.
func (s *Service) stuck(hash string, reason Stuck) Stuck {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	if reason == "" {
		delete(s.live.stalledSince, hash)
		return ""
	}
	since, ok := s.live.stalledSince[hash]
	if !ok {
		since = s.Now()
		s.live.stalledSince[hash] = since
	}
	if s.Now().Sub(since) < s.StallAfter {
		return ""
	}
	return reason
}

// portClosed is true, with the port, when the last port test said the
// peer port is closed.
func (s *Service) portClosed() (bool, int) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	return s.live.portAnswered && !s.live.portOpen, s.live.port
}

// testPort runs Transmission's port test when it's due. A failed test is
// only logged: the page then just doesn't mention the port.
func (s *Service) testPort(ctx context.Context) {
	// The attempt counts, answered or not, so a port test that keeps
	// failing still runs only every PortEvery.
	s.live.mu.Lock()
	due := s.live.portTried.IsZero() || s.Now().Sub(s.live.portTried) >= s.PortEvery
	if due {
		s.live.portTried = s.Now()
	}
	s.live.mu.Unlock()
	if !due {
		return
	}
	open, port, err := s.t.PortOpen(ctx)
	if err != nil {
		s.Log.Printf("port test: %v", err)
		return
	}
	s.live.mu.Lock()
	s.live.portAnswered, s.live.portOpen, s.live.port = true, open, port
	s.live.mu.Unlock()
}
