package server

import (
	"sync"
	"time"
)

// Whether a VPN peer is actually being used.
//
// This used to be "how long since its last WireGuard handshake", which cannot
// answer the question. hz puts PersistentKeepalive = 25 in every client config
// it generates — it has to, or peers behind NAT become unreachable — and a
// keepalive is a transmission, so WireGuard rekeys on it every couple of
// minutes whether or not a human is present. A handshake timestamp therefore
// tracks "the device is powered on", and an inactivity timeout built on it can
// essentially never fire.
//
// Byte counters answer it directly. Keepalives cost about 77 bytes a minute
// (32 bytes every 25 seconds); anything a person is doing costs far more, so a
// rate threshold separates the two cleanly.

// idleRateBytesPerMinute is the traffic rate below which a peer counts as idle.
//
// An order of magnitude above the keepalive floor, so rekey handshakes and the
// occasional stray packet cannot masquerade as use, and still far below any
// real session. The deliberate consequence: a peer whose only traffic is a DNS
// lookup a minute reads idle, which is the intent — 8.2.8 asks whether someone
// is *using* the session, and an ssh window nobody has typed into for fifteen
// minutes is exactly what it means to catch.
const idleRateBytesPerMinute = 1024

// peerActivity is one peer's last counter sample and the last time it looked
// busy.
type peerActivity struct {
	bytes      uint64
	sampledAt  time.Time
	lastActive time.Time
}

// observe folds in a new counter reading and returns the updated record.
//
// Pure, so the rules below can be tested without a kernel:
//   - A peer seen for the first time counts as active. Watching only starts
//     now, and revoking on a window we did not observe would log people out
//     whenever hz restarts.
//   - Counters that went backwards mean the interface was recreated, not that
//     traffic was negative. Treated as a fresh start, for the same reason.
//   - Otherwise the rate decides, and the sample is always advanced so a slow
//     trickle cannot accumulate into a false "active".
func (a peerActivity) observe(bytes uint64, now time.Time) peerActivity {
	switch {
	case a.sampledAt.IsZero(), bytes < a.bytes:
		return peerActivity{bytes: bytes, sampledAt: now, lastActive: now}
	}

	elapsed := now.Sub(a.sampledAt)
	next := peerActivity{bytes: bytes, sampledAt: now, lastActive: a.lastActive}
	if elapsed <= 0 {
		// Two samples with no time between them say nothing about a rate.
		return next
	}
	if rate := float64(bytes-a.bytes) / elapsed.Minutes(); rate > idleRateBytesPerMinute {
		next.lastActive = now
	}
	return next
}

// activityTracker holds the per-peer samples between ticks.
//
// In memory rather than in the config: it is a measurement, not a setting, and
// persisting it would mean writing the config file every minute for every peer.
// A restart forgets who was idle, which errs towards leaving sessions alone.
type activityTracker struct {
	mu    sync.Mutex
	peers map[string]peerActivity
}

func newActivityTracker() *activityTracker {
	return &activityTracker{peers: make(map[string]peerActivity)}
}

// observe folds in this tick's counters and returns each peer's last-active
// time. Peers absent from the reading are dropped, so a removed peer does not
// leak a record forever.
func (t *activityTracker) observe(bytesByPeer map[string]uint64, now time.Time) map[string]time.Time {
	// A Server assembled without a tracker reports nobody idle rather than
	// panicking on a background tick. Revoking nothing is the safe direction,
	// and it is the same answer a failed counter read gives.
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	next := make(map[string]peerActivity, len(bytesByPeer))
	lastActive := make(map[string]time.Time, len(bytesByPeer))
	for name, bytes := range bytesByPeer {
		rec := t.peers[name].observe(bytes, now)
		next[name] = rec
		lastActive[name] = rec.lastActive
	}
	t.peers = next
	return lastActive
}
