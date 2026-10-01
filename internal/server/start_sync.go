package server

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The start-sync: a seeded nested hz syncs once, shortly after it starts.
//
// Found on the first real bootstrap (2026-10-01): a child has no account and
// nobody is signed in to it, so nothing ever pressed Sync, and a proxied
// service added to its config.json was never rendered into haproxy.cfg. A
// parent has an operator at its UI; a child has a config file and a restart.
//
// It runs only when BOTH hold:
//   - the config has an `upstream` block (this hz is nested), and
//   - something is pending: the live config differs from the last-synced
//     snapshot — computePending, the exact answer `hz pending` and the UI's
//     pending badge give. A restart with nothing new does nothing.
//
// It is runSyncInternal, the same sync the UI and `hz sync` run, so it
// touches exactly what child_sync.go enumerates (and skips what it skips).
// It holds the sync broadcaster like any sync — the UI sees it, and a second
// sync cannot overlap it — and every line it says goes to the journal LOUD.
// A sync that ends with errors does not mark itself synced, so the next start
// tries again.

// startSyncDelay is how long after start the sync runs: long enough for the
// listener and the subsystems to settle, short enough that a restart is the
// whole of what an operator has to do.
const startSyncDelay = 5 * time.Second

// startSyncDecision is the pure rule.
func startSyncDecision(cfg *config.Config, pending apitypes.PendingChanges) (bool, string) {
	if cfg.Upstream == nil {
		return false, "this hz is not nested (no upstream block); syncs are the operator's"
	}
	if !pending.HasPending {
		return false, "nothing has changed since the last sync"
	}
	return true, fmt.Sprintf("%d change(s) not synced", pending.Count)
}

// startSyncIfPending decides now and, if the rule says so, syncs once after
// delay. Reports whether a sync was scheduled.
func (s *Server) startSyncIfPending(delay time.Duration) bool {
	run, why := startSyncDecision(s.cfg(), s.computePending())
	if !run {
		slog.Info("start-sync: not syncing", "reason", why)
		return false
	}
	slog.Warn("LOUD: start-sync: this nested hz has changes that were never synced; syncing once",
		"reason", why, "in", delay.String())
	go func() {
		time.Sleep(delay)
		s.runStartSync()
	}()
	return true
}

// runStartSync runs the one sync, holding the broadcaster.
func (s *Server) runStartSync() {
	if !s.sync.Start() {
		slog.Warn("LOUD: start-sync: a sync is already running; not starting a second one")
		return
	}
	log := teeSyncLogger{
		&BroadcastSyncLogger{broadcaster: s.sync, syncStart: time.Now()},
		loudSyncLogger{},
	}
	defer s.sync.Finish()
	defer func() {
		if r := recover(); r != nil {
			log.Error(fmt.Sprintf("PANIC: %v", r))
			log.Done(false)
		}
	}()
	run := s.runSyncInternal
	if s.startSyncRun != nil {
		run = s.startSyncRun
	}
	run(log, s.sync.CancelChan())
}

// loudSyncLogger writes a sync's lines to the journal, LOUD.
type loudSyncLogger struct{}

func (loudSyncLogger) Info(m string)    { slog.Warn("LOUD: start-sync: " + m) }
func (loudSyncLogger) Success(m string) { slog.Warn("LOUD: start-sync: " + m) }
func (loudSyncLogger) Warning(m string) { slog.Warn("LOUD: start-sync: " + m) }
func (loudSyncLogger) Error(m string)   { slog.Error("LOUD: start-sync: " + m) }
func (loudSyncLogger) Step(m string)    { slog.Warn("LOUD: start-sync: " + m) }
func (loudSyncLogger) Done(ok bool) {
	if ok {
		slog.Warn("LOUD: start-sync: done — rendered, and marked synced")
		return
	}
	slog.Error("LOUD: start-sync: finished WITH ERRORS — not marked synced; the next start tries again")
}

// teeSyncLogger sends every line to each logger in turn.
type teeSyncLogger []SyncLogger

func (t teeSyncLogger) Info(m string) {
	for _, l := range t {
		l.Info(m)
	}
}
func (t teeSyncLogger) Success(m string) {
	for _, l := range t {
		l.Success(m)
	}
}
func (t teeSyncLogger) Warning(m string) {
	for _, l := range t {
		l.Warning(m)
	}
}
func (t teeSyncLogger) Error(m string) {
	for _, l := range t {
		l.Error(m)
	}
}
func (t teeSyncLogger) Step(m string) {
	for _, l := range t {
		l.Step(m)
	}
}
func (t teeSyncLogger) Done(ok bool) {
	for _, l := range t {
		l.Done(ok)
	}
}
