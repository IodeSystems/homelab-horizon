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

	measured          bool
	timeSynced        bool
	securityUpdates   int
	totalUpdates      int
	lastAptUpdate     time.Time
	journalPersistent bool
	journalRetention  time.Duration
}

// hostFactsSnapshot is an immutable read of the cache.
type hostFactsSnapshot struct {
	measured          bool
	timeSynced        bool
	securityUpdates   int
	totalUpdates      int
	lastAptUpdate     time.Time
	journalPersistent bool
	journalRetention  time.Duration
}

func (h *hostFacts) snapshot() (facts hostFactsSnapshot) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	facts.measured = h.measured
	facts.timeSynced = h.timeSynced
	facts.securityUpdates = h.securityUpdates
	facts.totalUpdates = h.totalUpdates
	facts.lastAptUpdate = h.lastAptUpdate
	facts.journalPersistent = h.journalPersistent
	facts.journalRetention = h.journalRetention
	return
}

// refresh re-measures. Each fact is independent: a box without apt still
// reports its clock.
func (h *hostFacts) refresh() {
	synced := readTimeSynchronised()
	sec, total, aptOK := readPendingUpdates()
	stamp := readLastAptUpdate()
	persistent, retention := readJournalState()

	h.mu.Lock()
	defer h.mu.Unlock()
	h.measured = true
	h.timeSynced = synced
	if aptOK {
		h.securityUpdates = sec
		h.totalUpdates = total
	}
	h.lastAptUpdate = stamp
	h.journalPersistent = persistent
	h.journalRetention = retention
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

// readJournalState reports whether the journal survives a reboot and how long
// it is configured to keep.
//
// PCI DSS 10.5.1 wants twelve months of audit history with three immediately
// available. The default Ubuntu journal satisfies neither reliably: it is
// size-bounded rather than time-bounded, and on a box with no /var/log/journal
// it is volatile, so the entire audit trail dies with the next reboot. That is
// the failure worth catching — it is silent, and it looks fine right up until
// the moment someone needs last week's logs.
func readJournalState() (persistent bool, retention time.Duration) {
	fi, err := os.Stat("/var/log/journal")
	dirExists := err == nil && fi.IsDir()

	return journalPersistence(journaldSetting("Storage"), dirExists),
		parseSystemdDuration(journaldSetting("MaxRetentionSec"))
}

// journalPersistence decides whether the journal survives a reboot.
//
// Split from the I/O because "auto" is the subtle case and the one that is
// wrong most often: it is the shipped default, and it persists only when
// /var/log/journal already exists — journald creates nothing itself. Reading
// the setting alone calls an auto box volatile when it is persisting, and
// checking only the directory calls an explicitly volatile box persistent.
func journalPersistence(storage string, dirExists bool) bool {
	switch strings.ToLower(strings.TrimSpace(storage)) {
	case "persistent":
		// journald creates the directory itself in this mode, so the setting
		// is the answer even if the directory has not appeared yet.
		return true
	case "volatile", "none":
		return false
	default: // auto, or unset — auto is the default
		return dirExists
	}
}

// journaldSetting reads one effective journald setting.
//
// systemd-analyze cat-config merges the drop-ins the way journald itself does,
// so an operator who configured retention in /etc/systemd/journald.conf.d is
// read correctly rather than reported as unset.
func journaldSetting(key string) string {
	out, err := exec.Command("systemd-analyze", "cat-config", "systemd/journald.conf").Output()
	if err != nil {
		// Fall back to the main file; a missing systemd-analyze should not
		// turn into a false finding.
		body, ferr := os.ReadFile("/etc/systemd/journald.conf")
		if ferr != nil {
			return ""
		}
		out = body
	}

	value := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// Commented lines in these files are the shipped defaults, not
		// settings; taking one as configured would report a value nothing is
		// enforcing.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), key) {
			continue
		}
		// Last writer wins, the same way journald resolves drop-ins.
		value = strings.TrimSpace(rest)
	}
	return value
}

// parseSystemdDuration reads the subset of systemd time spans journald uses
// for retention: a bare number of seconds, or a value with a unit suffix.
func parseSystemdDuration(s string) time.Duration {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "0" {
		return 0
	}

	units := []struct {
		suffix string
		unit   time.Duration
	}{
		{"year", 365 * 24 * time.Hour},
		{"y", 365 * 24 * time.Hour},
		{"month", 30 * 24 * time.Hour},
		{"week", 7 * 24 * time.Hour},
		{"w", 7 * 24 * time.Hour},
		{"day", 24 * time.Hour},
		{"d", 24 * time.Hour},
		{"hour", time.Hour},
		{"h", time.Hour},
		{"min", time.Minute},
		{"m", time.Minute},
		{"s", time.Second},
	}
	for _, u := range units {
		if !strings.HasSuffix(s, u.suffix) {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), 64)
		if err != nil {
			return 0
		}
		return time.Duration(n * float64(u.unit))
	}

	// No suffix: systemd reads it as seconds.
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(n * float64(time.Second))
	}
	return 0
}
