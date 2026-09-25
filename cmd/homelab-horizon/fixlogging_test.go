package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/system"
)

type recordedCmd struct {
	name string
	args []string
}

// testFixer builds a fixer over a temp directory with every edge recorded
// rather than performed. chown is recorded too: the test runs unprivileged and
// a real one would fail for a reason that has nothing to do with the code.
func testFixer(t *testing.T, profileBody string, withLog bool) (logFixer, *[]recordedCmd, *[]string) {
	t.Helper()
	dir := t.TempDir()
	f := logFixer{
		profilePath: filepath.Join(dir, "usr.sbin.rsyslogd"),
		logPath:     filepath.Join(dir, "haproxy.log"),
	}
	if profileBody != "" {
		if err := os.WriteFile(f.profilePath, []byte(profileBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if withLog {
		if err := os.WriteFile(f.logPath, nil, 0o640); err != nil {
			t.Fatal(err)
		}
	}

	cmds := &[]recordedCmd{}
	chowns := &[]string{}
	f.run = func(name string, args ...string) ([]byte, error) {
		*cmds = append(*cmds, recordedCmd{name, args})
		return nil, nil
	}
	f.lookupID = func(_, _ string) (int, bool) { return 104, true }
	f.chown = func(path string, uid, gid int) error {
		*chowns = append(*chowns, fmt.Sprintf("%s %d:%d", path, uid, gid))
		return nil
	}
	return f, cmds, chowns
}

const brokenProfile = "#include <tunables/global>\nprofile rsyslogd /usr/sbin/rsyslogd {\n  /var/log/** rw,\n}\n"
const fixedProfile = "#include <tunables/global>\nprofile rsyslogd /usr/sbin/rsyslogd flags=(attach_disconnected) {\n}\n"

func TestFixPatchesTheProfileAndCreatesTheLogFile(t *testing.T) {
	f, cmds, chowns := testFixer(t, brokenProfile, false)

	var out bytes.Buffer
	if err := f.fix(&out, false, 0); err != nil {
		t.Fatalf("fix: %v\n%s", err, out.String())
	}

	got, err := os.ReadFile(f.profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "flags=(attach_disconnected)") {
		t.Errorf("the profile was not patched:\n%s", got)
	}
	fi, err := os.Stat(f.logPath)
	if err != nil {
		t.Fatalf("the log file was not created: %v", err)
	}
	if fi.Mode().Perm() != logFileMode {
		t.Errorf("log file mode %04o, want %04o", fi.Mode().Perm(), os.FileMode(logFileMode))
	}
	if len(*chowns) != 1 || !strings.HasSuffix((*chowns)[0], "104:104") {
		t.Errorf("the log file must be chowned to syslog:adm; got %v", *chowns)
	}

	// THE POINT OF THE MOVE: typed argument slices, no shell, no systemd-run.
	want := []recordedCmd{
		{"apparmor_parser", []string{"-r", f.profilePath}},
		{"systemctl", []string{"restart", "rsyslog"}},
	}
	if len(*cmds) != len(want) {
		t.Fatalf("commands %v, want %v", *cmds, want)
	}
	for i, w := range want {
		got := (*cmds)[i]
		if got.name != w.name || strings.Join(got.args, " ") != strings.Join(w.args, " ") {
			t.Errorf("command %d is %v, want %v", i, got, w)
		}
	}
}

// A box that is already correct must change nothing — including the rsyslog
// restart, which drops whatever is mid-write.
func TestFixOnAHealthyBoxIsANoOp(t *testing.T) {
	f, cmds, _ := testFixer(t, fixedProfile, true)

	var out bytes.Buffer
	if err := f.fix(&out, false, 0); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if len(*cmds) != 0 {
		t.Errorf("a healthy box ran %v; it must run nothing", *cmds)
	}
	if !strings.Contains(out.String(), "nothing to fix") {
		t.Errorf("it must say there was nothing to do; got:\n%s", out.String())
	}
}

// Undetermined is not "repair it anyway". If hz could not read the fact it
// cannot know what writing would do.
func TestFixRefusesWhenTheStateIsUndetermined(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file")
	}
	f, cmds, _ := testFixer(t, brokenProfile, true)
	if err := os.Chmod(f.profilePath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.profilePath, 0o644) })

	var out bytes.Buffer
	err := f.fix(&out, false, 0)
	if err == nil {
		t.Fatal("an undetermined state must be an error, not a silent repair")
	}
	if !strings.Contains(err.Error(), "cannot determine") {
		t.Errorf("the error must say what it could not determine; got %v", err)
	}
	if len(*cmds) != 0 {
		t.Errorf("nothing may run when the state is unknown; got %v", *cmds)
	}
}

// --dry-run says what it would do, needs no root, and changes nothing.
func TestFixDryRunChangesNothing(t *testing.T) {
	f, cmds, _ := testFixer(t, brokenProfile, false)

	var out bytes.Buffer
	if err := f.fix(&out, true, 1000); err != nil {
		t.Fatalf("a dry run must not need root: %v", err)
	}
	if len(*cmds) != 0 {
		t.Errorf("DRY RUN ran %v", *cmds)
	}
	if _, err := os.Stat(f.logPath); err == nil {
		t.Error("DRY RUN created the log file")
	}
	body, _ := os.ReadFile(f.profilePath)
	if strings.Contains(string(body), "attach_disconnected") {
		t.Error("DRY RUN patched the profile")
	}
	for _, want := range []string{"DRY RUN", "attach_disconnected", "restart rsyslog"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the dry run must name %q; got:\n%s", want, out.String())
		}
	}
}

func TestFixNeedsRoot(t *testing.T) {
	f, cmds, _ := testFixer(t, brokenProfile, false)

	var out bytes.Buffer
	err := f.fix(&out, false, 1000)
	if err == nil {
		t.Fatal("a non-root fix must refuse")
	}
	if !strings.Contains(err.Error(), system.FixHAProxyLoggingCommand) {
		t.Errorf("the refusal must name the command to run instead; got %v", err)
	}
	if len(*cmds) != 0 {
		t.Errorf("nothing may run unprivileged; got %v", *cmds)
	}
}

// A profile whose declaration hz does not recognise is REPORTED, never
// rewritten by guesswork — and the other half of the repair still happens.
func TestAnUnrecognisedProfileIsReportedNotGuessedAt(t *testing.T) {
	f, cmds, _ := testFixer(t, "profile something-else /usr/sbin/rsyslogd {\n}\n", false)

	var out bytes.Buffer
	err := f.fix(&out, false, 0)
	if err == nil {
		t.Fatal("hz must not claim success when it could not patch the profile")
	}
	if !strings.Contains(err.Error(), "by hand") {
		t.Errorf("the error must tell the operator what to do; got %v", err)
	}
	if _, statErr := os.Stat(f.logPath); statErr != nil {
		t.Error("the independent half of the repair must still have happened")
	}
	if len(*cmds) != 1 || (*cmds)[0].name != "systemctl" {
		t.Errorf("apparmor_parser must not run over a profile that was not patched; got %v", *cmds)
	}
}

// The verb is registered, takes no arguments, and is not hidden behind a flag
// on some other command.
func TestTheFixVerbIsRegistered(t *testing.T) {
	for _, c := range newRoot().Commands() {
		if c.Name() == "fix-haproxy-logging" {
			return
		}
	}
	t.Fatalf("fix-haproxy-logging is not a verb; the health card names %q and it has to exist",
		system.FixHAProxyLoggingCommand)
}
