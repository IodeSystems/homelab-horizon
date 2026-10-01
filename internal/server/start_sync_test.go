package server

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The start-sync (start_sync.go): once, for a child with pending changes;
// never for a parent; never when nothing is pending.

// startSyncServer is a test Server with a synced baseline written, then the
// config changed when pending is true — the state `hz pending` reads.
func startSyncServer(t *testing.T, cfg *config.Config, pending bool) (*Server, *atomic.Int32) {
	t.Helper()
	s := newTestServer(t, cfg)
	s.markSynced()
	if pending {
		next := *s.cfg()
		next.Services = append(append([]config.Service(nil), next.Services...),
			config.Service{Name: "redline-loadtest", Domains: []string{"loadtest.example.com"}})
		s.config.Store(&next)
	}
	runs := &atomic.Int32{}
	s.startSyncRun = func(log SyncLogger, _ <-chan struct{}) {
		runs.Add(1)
		log.Done(true)
	}
	return s, runs
}

func TestStartSyncRunsOnceForAChildWithPendingChanges(t *testing.T) {
	s, runs := startSyncServer(t, childTestConfig(t), true)
	if !s.computePending().HasPending {
		t.Fatal("instrument: the fixture has nothing pending")
	}
	if !s.startSyncIfPending(0) {
		t.Fatal("a child with pending changes did not schedule its sync")
	}
	deadline := time.Now().Add(5 * time.Second)
	for runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // a second run would land here
	if n := runs.Load(); n != 1 {
		t.Fatalf("start-sync ran %d times, want 1", n)
	}
	if s.sync.IsRunning() {
		t.Fatal("the broadcaster is still held after the start-sync")
	}
}

func TestStartSyncDoesNotRunWithoutUpstream(t *testing.T) {
	cfg := childTestConfig(t)
	cfg.Upstream = nil
	s, runs := startSyncServer(t, cfg, true)
	if !s.computePending().HasPending {
		t.Fatal("instrument: the fixture has nothing pending, so this would pass for the wrong reason")
	}
	if s.startSyncIfPending(0) {
		t.Fatal("a parent (no upstream) scheduled a start-sync")
	}
	time.Sleep(50 * time.Millisecond)
	if n := runs.Load(); n != 0 {
		t.Fatalf("start-sync ran %d times on a parent", n)
	}
}

func TestStartSyncDoesNotRunWhenNothingIsPending(t *testing.T) {
	s, runs := startSyncServer(t, childTestConfig(t), false)
	if s.computePending().HasPending {
		t.Fatalf("instrument: the fixture has something pending: %+v", s.computePending().Items)
	}
	if s.startSyncIfPending(0) {
		t.Fatal("a child with nothing pending scheduled a start-sync")
	}
	time.Sleep(50 * time.Millisecond)
	if n := runs.Load(); n != 0 {
		t.Fatalf("start-sync ran %d times with nothing pending", n)
	}
}
