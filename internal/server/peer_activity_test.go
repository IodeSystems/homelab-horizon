package server

import (
	"testing"
	"time"
)

// The bug this whole mechanism exists for.
//
// hz ships PersistentKeepalive = 25 in every client config, so an untouched
// laptop sends a 32-byte packet every 25 seconds — about 77 bytes a minute —
// and WireGuard rekeys on those, refreshing the handshake indefinitely. The old
// handshake-based timeout therefore never fired: a device merely powered on
// looked exactly like a device in use.
func TestKeepaliveTrafficIsNotActivity(t *testing.T) {
	const keepaliveBytesPerMinute = 77

	start := time.Unix(1_700_000_000, 0)
	rec := peerActivity{}.observe(0, start)

	// Twenty minutes of nothing but keepalives, sampled once a minute like the
	// real ticker does.
	bytes := uint64(0)
	for i := 1; i <= 20; i++ {
		bytes += keepaliveBytesPerMinute
		rec = rec.observe(bytes, start.Add(time.Duration(i)*time.Minute))
	}

	if !rec.lastActive.Equal(start) {
		t.Errorf("keepalives moved lastActive to %v; the peer never did anything",
			rec.lastActive.Sub(start))
	}
	idleFor := start.Add(20 * time.Minute).Sub(rec.lastActive)
	if idleFor < 15*time.Minute {
		t.Errorf("idle for %v after 20 quiet minutes, want at least 15", idleFor)
	}
}

func TestPeerActivityObserve(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	minute := time.Minute

	tests := []struct {
		name string
		// prior sample; zero value means the peer has not been seen
		prior      peerActivity
		bytes      uint64
		at         time.Time
		wantActive time.Time
	}{
		{
			name:       "first sighting counts as active",
			bytes:      500_000,
			at:         start,
			wantActive: start,
		},
		{
			name:       "real traffic is activity",
			prior:      peerActivity{bytes: 1000, sampledAt: start, lastActive: start},
			bytes:      1000 + 50_000,
			at:         start.Add(minute),
			wantActive: start.Add(minute),
		},
		{
			name:       "a trickle under the threshold is not",
			prior:      peerActivity{bytes: 1000, sampledAt: start, lastActive: start},
			bytes:      1000 + 100,
			at:         start.Add(minute),
			wantActive: start,
		},
		{
			name: "counters going backwards mean the interface restarted",
			// Not negative traffic, and not evidence of idleness: start over.
			prior:      peerActivity{bytes: 9_000_000, sampledAt: start, lastActive: start},
			bytes:      40,
			at:         start.Add(10 * minute),
			wantActive: start.Add(10 * minute),
		},
		{
			name:       "a burst spread thin over a long gap is still idle",
			prior:      peerActivity{bytes: 0, sampledAt: start, lastActive: start},
			bytes:      10_000, // 10 KB, but over 60 minutes — 167 B/min
			at:         start.Add(60 * minute),
			wantActive: start,
		},
		{
			name:       "two samples at the same instant say nothing",
			prior:      peerActivity{bytes: 1000, sampledAt: start, lastActive: start},
			bytes:      9_000_000,
			at:         start,
			wantActive: start,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.prior.observe(tt.bytes, tt.at)
			if !got.lastActive.Equal(tt.wantActive) {
				t.Errorf("lastActive = %v, want %v",
					got.lastActive.Sub(start), tt.wantActive.Sub(start))
			}
			if got.bytes != tt.bytes || !got.sampledAt.Equal(tt.at) {
				t.Errorf("sample not advanced: bytes=%d at=%v", got.bytes, got.sampledAt.Sub(start))
			}
		})
	}
}

// A peer that disappears from the reading must not leave a record behind, or a
// long-gone peer keeps a slot forever and comes back looking idle since its
// last sighting rather than being watched afresh.
func TestActivityTrackerForgetsAbsentPeers(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	tr := newActivityTracker()

	tr.observe(map[string]uint64{"alice": 1000, "bob": 2000}, start)
	tr.observe(map[string]uint64{"alice": 1000}, start.Add(time.Minute))

	if _, still := tr.peers["bob"]; still {
		t.Error("bob was absent from the reading but kept a record")
	}

	// Coming back is a first sighting again, so bob is active, not idle.
	last := tr.observe(map[string]uint64{"alice": 1000, "bob": 5}, start.Add(2*time.Minute))
	if !last["bob"].Equal(start.Add(2 * time.Minute)) {
		t.Errorf("returning peer lastActive = %v, want the current tick", last["bob"])
	}
}

func TestNilTrackerRevokesNothing(t *testing.T) {
	var tr *activityTracker
	if got := tr.observe(map[string]uint64{"alice": 1}, time.Now()); got != nil {
		t.Errorf("nil tracker returned %v, want nil so nothing is revoked", got)
	}
}
