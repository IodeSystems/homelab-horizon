package server

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Host facts that need a subprocess or a file read: clock synchronisation and
// pending package updates.
//
// Refreshed on the 60s health tick and cached, never gathered during a scrape.
// apt-check reads the whole package cache and takes the best part of a second;
// doing that per scrape would make Prometheus the most expensive thing running
// on the box, and these values change on the order of hours.

type hostFacts struct {
	mu sync.RWMutex

	measured        bool
	timeSynced      bool
	securityUpdates int
	totalUpdates    int
	lastAptUpdate   time.Time
}

// hostFactsSnapshot is an immutable read of the cache.
type hostFactsSnapshot struct {
	measured        bool
	timeSynced      bool
	securityUpdates int
	totalUpdates    int
	lastAptUpdate   time.Time
}

func (h *hostFacts) snapshot() (facts hostFactsSnapshot) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	facts.measured = h.measured
	facts.timeSynced = h.timeSynced
	facts.securityUpdates = h.securityUpdates
	facts.totalUpdates = h.totalUpdates
	facts.lastAptUpdate = h.lastAptUpdate
	return
}

// refresh re-measures. Each fact is independent: a box without apt still
// reports its clock.
func (h *hostFacts) refresh() {
	synced := readTimeSynchronised()
	sec, total, aptOK := readPendingUpdates()
	stamp := readLastAptUpdate()

	h.mu.Lock()
	defer h.mu.Unlock()
	h.measured = true
	h.timeSynced = synced
	if aptOK {
		h.securityUpdates = sec
		h.totalUpdates = total
	}
	h.lastAptUpdate = stamp
}

// readTimeSynchronised asks systemd whether the clock is disciplined.
//
// PCI DSS 10.6 requires synchronised time, and it is not merely paperwork
// here: TOTP is a function of the clock, so a drifting gateway rejects every
// correct code and looks like a broken authenticator to every user at once.
func readTimeSynchronised() bool {
	out, err := exec.Command("timedatectl", "show", "-p", "NTPSynchronized", "--value").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "yes"
}

// readPendingUpdates returns pending (security, total) package updates.
//
// Uses update-notifier's apt-check, which reads the existing package cache
// rather than touching the network — hz must never trigger an implicit
// apt-get update as a side effect of being scraped. Its count goes to stderr
// as "total;security".
func readPendingUpdates() (security, total int, ok bool) {
	const aptCheck = "/usr/lib/update-notifier/apt-check"
	if _, err := os.Stat(aptCheck); err != nil {
		return 0, 0, false
	}
	out, err := exec.Command(aptCheck).CombinedOutput()
	if err != nil {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ";")
	if len(parts) != 2 {
		return 0, 0, false
	}
	t, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	s, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return s, t, true
}

// readLastAptUpdate returns when the package lists were last refreshed. A
// pending-update count is only as trustworthy as the cache it came from: zero
// pending against lists last fetched in March means nothing.
func readLastAptUpdate() time.Time {
	for _, p := range []string{
		"/var/lib/apt/periodic/update-success-stamp",
		"/var/lib/apt/lists",
	} {
		if fi, err := os.Stat(p); err == nil {
			return fi.ModTime()
		}
	}
	return time.Time{}
}
