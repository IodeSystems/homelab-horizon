package main

import (
	"strings"
	"testing"
)

// Push installs get an updater; the agent itself must not be able to do it.
func TestPushUnitHasNoListenerAndUpdaterIsSeparate(t *testing.T) {
	f := &serveFlags{
		pushTo: "https://kiosk.example.com", vantage: "v",
		tokenFile: "/etc/hz-probe/token", statePath: "/var/lib/hz-probe/state.json",
	}
	unit := generateUnit(f, "/usr/local/bin/hz-probe")

	// The agent is unprivileged and reaches nothing inbound.
	if !strings.Contains(unit, "DynamicUser=yes") {
		t.Fatal("the agent unit should stay unprivileged")
	}
	for _, forbidden := range []string{"--listen", "--tls-cert", "--tls-key"} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("a push unit should not carry %s — nothing dials this agent", forbidden)
		}
	}
	if !strings.Contains(unit, "--push-to https://kiosk.example.com") {
		t.Fatal("the push target is missing from the unit")
	}

	// The updater is a different unit, and it is the one with privilege.
	upd := strings.NewReplacer("__EXEC__", "x").Replace(updateUnitTemplate)
	if strings.Contains(upd, "DynamicUser") {
		t.Fatal("the updater has to be root; it replaces a binary and restarts a service")
	}
	if !strings.Contains(upd, "Type=oneshot") {
		t.Fatal("the updater should be a oneshot, not a service that stays up")
	}
	if !strings.Contains(updateTimerTemplate, "RandomizedDelaySec") {
		t.Fatal("without a randomised delay a fleet asks hz at the same second")
	}
}

// A pull install has no token hz recognises for an unattended download, so
// it must not get a timer that cannot work.
func TestPullInstallGetsNoUpdateTimer(t *testing.T) {
	f := &serveFlags{
		listen: ":8443", vantage: "v",
		tokenFile: "/etc/hz-probe/token", statePath: "/var/lib/hz-probe/state.json",
	}
	if f.pushMode() {
		t.Fatal("this fixture should be pull mode")
	}
	unit := generateUnit(f, "/usr/local/bin/hz-probe")
	if !strings.Contains(unit, "--listen :8443") {
		t.Fatal("a pull unit should listen")
	}
}
