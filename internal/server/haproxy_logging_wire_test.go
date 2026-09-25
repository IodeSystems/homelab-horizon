package server

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/system"
)

// The wire contract for haproxy's logging diagnosis, which is the half of
// "keep the diagnosis card" that hz owns.
//
// It exists because a positive control found nothing here: turning the
// unknown state back into LogOK (the pre-2026-09-25 behaviour) reddened
// internal/system and cmd/homelab-horizon and left every test in this package
// green — so the thing the CARD reads was pinned by nothing at all.

func TestUnknownReachesTheCardAsUnknown(t *testing.T) {
	extras, errs := haproxyLoggingWire(system.HAProxyLogging{
		AppArmor:       system.LogUnknown,
		AppArmorDetail: "could not read rsyslogd's apparmor profile: permission denied — hz cannot tell",
		LogFile:        system.LogOK,
	})

	if got := extras["logging_apparmor"]; got != "unknown" {
		t.Fatalf("logging_apparmor = %v, want \"unknown\" — a card that cannot see this "+
			"renders an unreadable host as a healthy one", got)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "cannot tell") {
		t.Fatalf("an undetermined fact must reach the card as an error that SAYS so; got %v", errs)
	}
}

func TestBrokenReachesTheCardWithItsReasonAndTheCommand(t *testing.T) {
	extras, errs := haproxyLoggingWire(system.HAProxyLogging{
		AppArmor:      system.LogOK,
		LogFile:       system.LogBroken,
		LogFileDetail: "/var/log/haproxy.log does not exist",
	})

	if got := extras["logging_file"]; got != "broken" {
		t.Errorf("logging_file = %v, want \"broken\"", got)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "does not exist") {
		t.Errorf("the reason must travel with the verdict; got %v", errs)
	}
	// The card has no button any more, so the command IS the remedy. If it
	// does not cross, the row is a red chip and a dead end.
	if got := extras["logging_fix_command"]; got != system.FixHAProxyLoggingCommand {
		t.Errorf("logging_fix_command = %v, want %q", got, system.FixHAProxyLoggingCommand)
	}
}

// Not-applicable is a complete answer. A host that does not confine rsyslogd
// has nothing to patch, and reddening its card would send an operator after a
// file that will never exist.
func TestNotApplicableDoesNotReddenTheCard(t *testing.T) {
	extras, errs := haproxyLoggingWire(system.HAProxyLogging{
		AppArmor:       system.LogNotApplicable,
		AppArmorDetail: "no apparmor profile for rsyslogd at /etc/apparmor.d/usr.sbin.rsyslogd",
		LogFile:        system.LogOK,
	})

	if len(errs) != 0 {
		t.Fatalf("an unconfined host is not a fault; got %v", errs)
	}
	// It still says which of the four states it is, so the card can word the
	// green chip honestly rather than claiming a check it never ran.
	if got := extras["logging_apparmor"]; got != "not_applicable" {
		t.Errorf("logging_apparmor = %v, want \"not_applicable\"", got)
	}
}

// The old booleans are gone. A card still reading them would get undefined,
// coerce it to false, and render a healthy host as broken forever.
func TestTheOldBooleansAreGone(t *testing.T) {
	extras, _ := haproxyLoggingWire(system.HAProxyLogging{AppArmor: system.LogOK, LogFile: system.LogOK})
	for _, dead := range []string{"logging_apparmor_ok", "logging_file_exists"} {
		if _, present := extras[dead]; present {
			t.Errorf("%s is still on the wire; it had no way to express \"I could not tell\" "+
				"and that is why it was replaced", dead)
		}
	}
}
