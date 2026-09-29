package main

import (
	"bytes"
	"log/slog"
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
	if got, err := resolveNtfyURL("", filepath.Join(dir, "absent"), true); err != nil || got != "" {
		t.Fatalf("unset should be off: %q, %v", got, err)
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveNtfyURL("", empty, true); err == nil {
		t.Fatal("an empty file is intent gone wrong, not off")
	}

	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte(" https://ntfy.sh/topic \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveNtfyURL("https://flag.example/x", good, true); err != nil || got != "https://ntfy.sh/topic" {
		t.Fatalf("the file wins: %q, %v", got, err)
	}

	t.Setenv("HZ_PROBE_NTFY_URL", "https://env.example/t")
	if got, _ := resolveNtfyURL("https://flag.example/x", "", false); got != "https://env.example/t" {
		t.Fatalf("the environment beats the flag: %q", got)
	}
	t.Setenv("HZ_PROBE_NTFY_URL", "")
	if got, _ := resolveNtfyURL("https://flag.example/x", "", false); got != "https://flag.example/x" {
		t.Fatalf("the flag is the last resort: %q", got)
	}

	_, err := resolveNtfyURL("ntfy.sh/secret-topic", "", false)
	if err == nil {
		t.Fatal("a URL without a scheme should be refused")
	}
	if strings.Contains(err.Error(), "secret-topic") {
		t.Fatal("the error must not echo the secret")
	}
}

// The ntfy token travels exactly like the URL: a credential when the file
// exists, an explicit empty --ntfy-token-file= when it does not, and never
// its value on the command line.
func TestPushUnitCarriesNtfyTokenAsACredential(t *testing.T) {
	dir := t.TempDir()
	tokFile := filepath.Join(dir, "ntfy-token")
	secret := "tk_unit_secret_9c1"
	if err := os.WriteFile(tokFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &serveFlags{
		pushTo: "https://kiosk.example.com", vantage: "v",
		tokenFile: "/etc/hz-probe/token", statePath: "/var/lib/hz-probe/state.json",
		ntfyFile: filepath.Join(dir, "no-url"), ntfyAfter: 3,
		ntfyTokenFile: tokFile,
	}
	unit := generateUnit(f, "/usr/local/bin/hz-probe")
	if !strings.Contains(unit, "LoadCredential=ntfy-token:"+tokFile) {
		t.Fatalf("the ntfy token file should be a credential:\n%s", unit)
	}
	if !strings.Contains(unit, "--ntfy-token-file %d/ntfy-token") {
		t.Fatalf("serve should read the ntfy token from the credentials directory:\n%s", unit)
	}
	if strings.Contains(unit, secret) || strings.Contains(unit, "--ntfy-token ") {
		t.Fatal("the ntfy token itself must never be in the unit")
	}

	f.ntfyTokenFile = filepath.Join(dir, "absent")
	unit = generateUnit(f, "/usr/local/bin/hz-probe")
	if strings.Contains(unit, "LoadCredential=ntfy-token") || strings.Contains(unit, "%d/ntfy-token") {
		t.Fatalf("a missing token file must not become a credential:\n%s", unit)
	}
	if !strings.Contains(unit, "--ntfy-token-file=") {
		t.Fatalf("without a file the unit should disable the token explicitly:\n%s", unit)
	}

	f.pushTo, f.listen, f.ntfyTokenFile = "", ":8443", tokFile
	if unit = generateUnit(f, "/usr/local/bin/hz-probe"); strings.Contains(unit, "ntfy") {
		t.Fatal("a pull unit should carry nothing about ntfy")
	}
}

func TestResolveNtfyToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HZ_PROBE_NTFY_TOKEN", "")

	if got, err := resolveNtfyToken("", filepath.Join(dir, "absent"), true); err != nil || got != "" {
		t.Fatalf("unset should mean no token: %q, %v", got, err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveNtfyToken("", empty, true); err == nil {
		t.Fatal("an empty NAMED token file is intent gone wrong, not off")
	}
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte(" tk_file \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HZ_PROBE_NTFY_TOKEN", "tk_env")
	if got, _ := resolveNtfyToken("tk_flag", good, true); got != "tk_file" {
		t.Fatalf("the file wins: %q", got)
	}
	if got, _ := resolveNtfyToken("tk_flag", "", false); got != "tk_env" {
		t.Fatalf("the environment beats the flag: %q", got)
	}
	t.Setenv("HZ_PROBE_NTFY_TOKEN", "")
	if got, _ := resolveNtfyToken("tk_flag", "", false); got != "tk_flag" {
		t.Fatalf("the flag is the last resort: %q", got)
	}
}

// The 2026-09-28 rule, for the token: an unreadable DEFAULT token file warns
// and carries on without a token; it never stops the agent. A NAMED one that
// cannot be read is still an error. The warning never carries the value.
func TestResolveNtfyTokenDefaultPathNeverStopsTheAgent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory, so EACCES cannot be produced")
	}
	t.Setenv("HZ_PROBE_NTFY_TOKEN", "")
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(locked, "ntfy-token")
	if err := os.WriteFile(file, []byte("tk_locked_value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	// Positive control: the instrument really produces EACCES here.
	if _, err := os.ReadFile(file); err == nil || os.IsNotExist(err) {
		t.Fatalf("expected a permission error, got %v", err)
	}

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	got, err := resolveNtfyToken("", file, false)
	if err != nil || got != "" {
		t.Fatalf("default path unreadable: got %q, %v — want no token, no error", got, err)
	}
	if !strings.Contains(logs.String(), "ntfy token file cannot be read") {
		t.Fatalf("an unreadable default token file should warn:\n%s", logs.String())
	}
	if _, err := resolveNtfyToken("", file, true); err == nil {
		t.Fatal("a NAMED token file that cannot be read must still be an error")
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

// The production crash-loop, 2026-09-28: an old unit (no --ntfy-url-file) on a
// new binary, and the default file in a directory the service user cannot
// read. The DEFAULT path failing to read must switch alerting off, never stop
// the agent; a file somebody named still fails loudly.
func TestResolveNtfyURLDefaultPathNeverStopsTheAgent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory, so EACCES cannot be produced")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(locked, "ntfy-url")
	if err := os.WriteFile(file, []byte("https://ntfy.sh/t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	// Positive control: the instrument really produces EACCES here.
	if _, err := os.ReadFile(file); err == nil || os.IsNotExist(err) {
		t.Fatalf("expected a permission error, got %v", err)
	}

	got, err := resolveNtfyURL("", file, false)
	if err != nil || got != "" {
		t.Fatalf("default path unreadable: got %q, %v — want off, no error", got, err)
	}
	if _, err := resolveNtfyURL("", file, true); err == nil {
		t.Fatal("a NAMED file that cannot be read must still be an error")
	}
	empty := filepath.Join(dir, "empty")
	_ = os.WriteFile(empty, nil, 0o600)
	if got, err := resolveNtfyURL("", empty, false); err != nil || got != "" {
		t.Fatalf("an empty DEFAULT file: got %q, %v — want off", got, err)
	}
}
