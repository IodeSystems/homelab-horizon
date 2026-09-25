package system

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeProfile(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "usr.sbin.rsyslogd")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const profileWithoutFlag = "#include <tunables/global>\nprofile rsyslogd /usr/sbin/rsyslogd {\n}\n"
const profileWithFlag = "#include <tunables/global>\nprofile rsyslogd /usr/sbin/rsyslogd flags=(attach_disconnected) {\n}\n"

func TestDiagnoseReadsBothFacts(t *testing.T) {
	dir := t.TempDir()
	profile := writeProfile(t, dir, profileWithFlag)
	logPath := filepath.Join(dir, "haproxy.log")
	if err := os.WriteFile(logPath, nil, 0o640); err != nil {
		t.Fatal(err)
	}

	d := DiagnoseHAProxyLogging(profile, logPath)
	if d.AppArmor != LogOK || d.LogFile != LogOK {
		t.Fatalf("a correctly configured host reads as %+v", d)
	}
	if d.Broken() || d.Undetermined() {
		t.Fatalf("a correctly configured host must be neither broken nor undetermined: %+v", d)
	}
}

func TestAProfileWithoutTheFlagIsBroken(t *testing.T) {
	dir := t.TempDir()
	profile := writeProfile(t, dir, profileWithoutFlag)

	d := DiagnoseHAProxyLogging(profile, filepath.Join(dir, "nope.log"))
	if d.AppArmor != LogBroken {
		t.Errorf("apparmor state %q, want %q", d.AppArmor, LogBroken)
	}
	if d.LogFile != LogBroken {
		t.Errorf("log file state %q, want %q — a missing log file is measured, not unknown", d.LogFile, LogBroken)
	}
	if !d.Broken() {
		t.Error("Broken() must be true")
	}
	// The card names the fix, so the diagnosis has to carry enough to write
	// the sentence: which file, and what is wrong with it.
	joined := strings.Join(d.Details(), " ")
	if !strings.Contains(joined, "attach_disconnected") || !strings.Contains(joined, "nope.log") {
		t.Errorf("the details must name the flag and the file; got %q", joined)
	}
}

// No apparmor profile is not a failure to read one. A host that does not
// confine rsyslogd has nothing to patch, and telling the operator to run a fix
// there would send them after a file that will never exist.
func TestNoProfileIsNotApplicableRatherThanBroken(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "haproxy.log")
	if err := os.WriteFile(logPath, nil, 0o640); err != nil {
		t.Fatal(err)
	}

	d := DiagnoseHAProxyLogging(filepath.Join(dir, "absent"), logPath)
	if d.AppArmor != LogNotApplicable {
		t.Fatalf("apparmor state %q, want %q", d.AppArmor, LogNotApplicable)
	}
	if d.Broken() || d.Undetermined() {
		t.Fatalf("an unconfined host is fully answered: %+v", d)
	}
}

// THE STATE THE OLD BOOL COULD NOT EXPRESS.
//
// checkHAProxyApparmor returned `true, ""` — healthy — for every read error,
// including permission denied. That is the exact shape of this repo's founding
// bug: unreadable and fine were the same value. After item 12 hz runs
// unprivileged, so this is the state the live gateway will actually be in.
func TestAnUnreadableProfileIsUnknownNotFine(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file; this can only be measured unprivileged")
	}
	if runtime.GOOS != "linux" {
		t.Skip("mode-based refusal is not portable")
	}
	dir := t.TempDir()
	profile := writeProfile(t, dir, profileWithFlag)
	if err := os.Chmod(profile, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(profile, 0o644) })

	logPath := filepath.Join(dir, "haproxy.log")
	if err := os.WriteFile(logPath, nil, 0o640); err != nil {
		t.Fatal(err)
	}

	d := DiagnoseHAProxyLogging(profile, logPath)
	if d.AppArmor != LogUnknown {
		t.Fatalf("an unreadable profile reads as %q, want %q — unreadable is not fine", d.AppArmor, LogUnknown)
	}
	if !d.Undetermined() {
		t.Error("Undetermined() must be true when a fact could not be measured")
	}
	if d.Broken() {
		t.Error("undetermined is not broken either — it is a statement about the instrument")
	}
	if !strings.Contains(strings.Join(d.Details(), " "), "cannot tell") {
		t.Errorf("the unknown state has to SAY it could not tell; got %q", d.Details())
	}
}

// A directory hz cannot traverse makes Stat fail with something other than
// ErrNotExist, which is the log file's version of the same distinction.
func TestAnUnstatableLogFileIsUnknown(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root traverses a 0000 directory")
	}
	if runtime.GOOS != "linux" {
		t.Skip("mode-based refusal is not portable")
	}
	dir := t.TempDir()
	profile := writeProfile(t, dir, profileWithFlag)

	closed := filepath.Join(dir, "closed")
	if err := os.Mkdir(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })

	d := DiagnoseHAProxyLogging(profile, filepath.Join(closed, "haproxy.log"))
	if d.LogFile != LogUnknown {
		t.Fatalf("log file state %q, want %q", d.LogFile, LogUnknown)
	}
	if !d.Undetermined() {
		t.Error("Undetermined() must be true")
	}
}

// The remedy is one string. Three copies of a command is three chances for the
// card to name something the CLI does not have.
func TestTheFixCommandIsTheCLIVerb(t *testing.T) {
	if !strings.Contains(FixHAProxyLoggingCommand, "homelab-horizon fix-haproxy-logging") {
		t.Fatalf("the remedy must name the verb that exists: %q", FixHAProxyLoggingCommand)
	}
}
