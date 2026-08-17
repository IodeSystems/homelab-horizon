package server

import (
	"testing"
	"time"
)

// systemd accepts a range of time spans, and getting this wrong reports a
// retention hz does not have — in whichever direction is worse.
func TestParseSystemdDuration(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"1year", 365 * 24 * time.Hour},
		{"1y", 365 * 24 * time.Hour},
		{"6month", 180 * 24 * time.Hour},
		{"2week", 14 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"12h", 12 * time.Hour},
		{"90min", 90 * time.Minute},
		// A bare number is seconds, which is systemd's rule and the easiest
		// thing to misread as days.
		{"31536000", 365 * 24 * time.Hour},
		{"nonsense", 0},
	}
	for _, tc := range tests {
		if got := parseSystemdDuration(tc.in); got != tc.want {
			t.Errorf("parseSystemdDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Whitespace and case appear in real config files; a setting that reads as
// unset because of a space is a false finding.
func TestParseSystemdDurationTolerance(t *testing.T) {
	for _, in := range []string{" 1year ", "1YEAR", "1Year"} {
		if got := parseSystemdDuration(in); got != 365*24*time.Hour {
			t.Errorf("parseSystemdDuration(%q) = %v, want a year", in, got)
		}
	}
}

// The four ways a journal can be configured, and the two that are easy to get
// wrong. "auto" is the shipped default on Ubuntu, so this is the common path
// rather than an edge case.
func TestJournalPersistence(t *testing.T) {
	tests := []struct {
		storage   string
		dirExists bool
		want      bool
	}{
		{"persistent", false, true}, // journald creates the directory itself
		{"persistent", true, true},
		{"volatile", true, false}, // a directory left behind proves nothing
		{"none", true, false},
		{"auto", true, true},   // the default, persisting
		{"auto", false, false}, // the default, silently volatile
		{"", true, true},       // unset means auto
		{"", false, false},
		{" AUTO ", true, true}, // real config files have whitespace and case
	}
	for _, tc := range tests {
		if got := journalPersistence(tc.storage, tc.dirExists); got != tc.want {
			t.Errorf("journalPersistence(%q, dir=%v) = %v, want %v",
				tc.storage, tc.dirExists, got, tc.want)
		}
	}
}
