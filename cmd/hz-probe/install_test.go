package main

import (
	"os"
	"path/filepath"
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

// The ntfy URL is a capability: it reaches the agent as a credential, the
// same way the token does, and never on the command line.
func TestPushUnitCarriesNtfyAsACredential(t *testing.T) {
	dir := t.TempDir()
	ntfy := filepath.Join(dir, "ntfy-url")
	secret := "https://ntfy.example.com/secret-topic-7f3a"
	if err := os.WriteFile(ntfy, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &serveFlags{
		pushTo: "https://kiosk.example.com", vantage: "v",
		tokenFile: "/etc/hz-probe/token", statePath: "/var/lib/hz-probe/state.json",
		ntfyFile: ntfy, ntfyAfter: 5,
	}
	unit := generateUnit(f, "/usr/local/bin/hz-probe")
	if !strings.Contains(unit, "LoadCredential=ntfy-url:"+ntfy) {
		t.Fatalf("the ntfy file should be a credential:\n%s", unit)
	}
	if !strings.Contains(unit, "--ntfy-url-file %d/ntfy-url --ntfy-after 5") {
		t.Fatalf("serve should read the ntfy URL from the credentials directory:\n%s", unit)
	}
	if strings.Contains(unit, secret) || strings.Contains(unit, "--ntfy-url ") {
		t.Fatal("the ntfy URL itself must never be in the unit")
	}

	// No file: no credential (it would stop the service starting), and the
	// default path disabled explicitly.
	f.ntfyFile = filepath.Join(dir, "absent")
	unit = generateUnit(f, "/usr/local/bin/hz-probe")
	if strings.Contains(unit, "LoadCredential=ntfy-url") || strings.Contains(unit, "%d/ntfy-url") {
		t.Fatalf("a missing ntfy file must not become a credential:\n%s", unit)
	}
	if !strings.Contains(unit, "--ntfy-url-file=") {
		t.Fatalf("without a file the unit should disable ntfy explicitly:\n%s", unit)
	}

	// Pull mode never dials hz, so it cannot alert on hz being down.
	f.pushTo, f.listen, f.ntfyFile = "", ":8443", ntfy
	if unit = generateUnit(f, "/usr/local/bin/hz-probe"); strings.Contains(unit, "ntfy") {
		t.Fatal("a pull unit should carry nothing about ntfy")
	}
}

func TestResolveNtfyURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HZ_PROBE_NTFY_URL", "")

	// Unset everywhere: off, not an error.
	if got, err := resolveNtfyURL("", filepath.Join(dir, "absent")); err != nil || got != "" {
		t.Fatalf("unset should be off: %q, %v", got, err)
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveNtfyURL("", empty); err == nil {
		t.Fatal("an empty file is intent gone wrong, not off")
	}

	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte(" https://ntfy.sh/topic \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveNtfyURL("https://flag.example/x", good); err != nil || got != "https://ntfy.sh/topic" {
		t.Fatalf("the file wins: %q, %v", got, err)
	}

	t.Setenv("HZ_PROBE_NTFY_URL", "https://env.example/t")
	if got, _ := resolveNtfyURL("https://flag.example/x", ""); got != "https://env.example/t" {
		t.Fatalf("the environment beats the flag: %q", got)
	}
	t.Setenv("HZ_PROBE_NTFY_URL", "")
	if got, _ := resolveNtfyURL("https://flag.example/x", ""); got != "https://flag.example/x" {
		t.Fatalf("the flag is the last resort: %q", got)
	}

	_, err := resolveNtfyURL("ntfy.sh/secret-topic", "")
	if err == nil {
		t.Fatal("a URL without a scheme should be refused")
	}
	if strings.Contains(err.Error(), "secret-topic") {
		t.Fatal("the error must not echo the secret")
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
