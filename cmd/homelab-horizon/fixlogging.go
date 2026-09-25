package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/system"
)

// `homelab-horizon fix-haproxy-logging` — the provisioning home of what used to
// be POST /api/v1/haproxy/fix-logging.
//
// WHY IT MOVED. The handler did its work by building a shell string and piping
// it into `systemd-run --pipe --wait --service-type=oneshot bash -c "…"`, three
// times, to escape hz's own ProtectSystem=strict sandbox. That is §5.2 rule 1
// (never a shell string) and rule 3 (never reachable from the web process) in
// one call, and the helper it grew — systemdRun — was a general-purpose root
// executor sitting in the HTTP layer. Deleting it needed its reasons to exist
// to have somewhere else to live; this is that somewhere.
//
// WHY A NAMED VERB RATHER THAN PART OF `install`. Exactly the argument
// deps.go already makes for install-deps: patching an apparmor profile and
// restarting rsyslog bounces a daemon on a live gateway, and an install that
// did it as a side effect would do it at a moment nobody chose. So it is a
// sibling verb a human at the box, or a provisioning script, asks for BY NAME —
// and the System Health card names that command instead of offering a button,
// the same move the unit rows and the dependency rows already made.
//
// WHAT IT DOES NOT NEED ANY MORE. No systemd-run and no shell. The sandbox the
// old handler was escaping is hz's; a root CLI has no such confinement, so the
// profile is written with os.WriteFile, the log file is created with
// os.OpenFile + os.Chown, and the two commands that genuinely are commands
// (apparmor_parser, systemctl) take typed argument slices that carry no
// caller-supplied text at all. Every argument below is a constant.

// logFixer is the verb with its edges seamed, so the test drives real files and
// records the commands rather than running apparmor_parser on the dev box.
type logFixer struct {
	profilePath string
	logPath     string
	// run executes one command. Never a shell: name and args are separate and
	// every one of them is a constant in this file.
	run func(name string, args ...string) ([]byte, error)
	// lookupID resolves a user or group name to an id, or reports that this
	// host has no such account.
	lookupID func(kind, name string) (int, bool)
	chown    func(path string, uid, gid int) error
}

// The account rsyslog writes as on Debian/Ubuntu, and the group that reads
// logs. Constants rather than config: they are the distribution's, not hz's.
const (
	logOwnerUser  = "syslog"
	logOwnerGroup = "adm"
	logFileMode   = 0o640
)

func realLogFixer() logFixer {
	return logFixer{
		profilePath: system.RsyslogAppArmorProfile,
		logPath:     system.HAProxyLogPath,
		run: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		},
		lookupID: func(kind, name string) (int, bool) {
			var raw string
			switch kind {
			case "user":
				u, err := user.Lookup(name)
				if err != nil {
					return 0, false
				}
				raw = u.Uid
			default:
				g, err := user.LookupGroup(name)
				if err != nil {
					return 0, false
				}
				raw = g.Gid
			}
			id, err := strconv.Atoi(raw)
			if err != nil {
				return 0, false
			}
			return id, true
		},
		chown: os.Chown,
	}
}

// runFixHAProxyLogging is the verb's entry point.
func runFixHAProxyLogging(dryRun bool) error {
	return realLogFixer().fix(os.Stdout, dryRun, os.Geteuid())
}

// fix diagnoses first and repairs only what it measured wrong.
//
// Diagnose-then-act rather than act-unconditionally, because both halves are
// destructive in the small: rewriting a profile that is already correct churns
// a file apparmor is confining a running daemon with, and the rsyslog restart
// at the end drops whatever is mid-write. A box that is already correct must be
// a no-op that says so.
func (f logFixer) fix(out io.Writer, dryRun bool, euid int) error {
	d := system.DiagnoseHAProxyLogging(f.profilePath, f.logPath)

	// Undetermined is neither "fix it" nor "leave it". hz could not read the
	// fact, so it cannot know what writing would do — and a fixer that repairs
	// what it could not measure is guessing at a live gateway.
	if d.Undetermined() {
		for _, s := range d.Details() {
			_, _ = fmt.Fprintln(out, "  "+s)
		}
		return fmt.Errorf("cannot determine the state of haproxy's logging, so nothing was changed")
	}

	if !d.Broken() {
		_, _ = fmt.Fprintln(out, "HAProxy logging: nothing to fix.")
		for _, s := range d.Details() {
			_, _ = fmt.Fprintln(out, "  "+s)
		}
		return nil
	}

	_, _ = fmt.Fprintln(out, "HAProxy logging is misconfigured:")
	for _, s := range d.Details() {
		_, _ = fmt.Fprintln(out, "  "+s)
	}
	_, _ = fmt.Fprintln(out)

	if dryRun {
		if d.AppArmor == system.LogBroken {
			_, _ = fmt.Fprintf(out, "DRY RUN: would add the attach_disconnected flag to %s and reload it.\n", f.profilePath)
		}
		if d.LogFile == system.LogBroken {
			_, _ = fmt.Fprintf(out, "DRY RUN: would create %s, %s:%s, mode %04o.\n",
				f.logPath, logOwnerUser, logOwnerGroup, os.FileMode(logFileMode))
		}
		_, _ = fmt.Fprintln(out, "DRY RUN: would restart rsyslog. Nothing was changed.")
		return nil
	}

	if euid != 0 {
		return fmt.Errorf("must run as root to patch an apparmor profile and create a log file (try: %s)",
			system.FixHAProxyLoggingCommand)
	}

	// Collected rather than returned on the first failure: the two repairs are
	// independent, and a box whose log file was created is better off than one
	// that stopped because apparmor_parser is not installed.
	var errs []string

	if d.AppArmor == system.LogBroken {
		if err := f.patchProfile(out); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if d.LogFile == system.LogBroken {
		if err := f.createLogFile(out); err != nil {
			errs = append(errs, err.Error())
		}
	}

	// The restart is what makes either repair take effect, so it runs even if
	// one half failed.
	if cmdOut, err := f.run("systemctl", "restart", "rsyslog"); err != nil {
		errs = append(errs, "restart rsyslog: "+err.Error()+trailing(cmdOut))
	} else {
		_, _ = fmt.Fprintln(out, "Restarted rsyslog.")
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	_, _ = fmt.Fprintln(out, "HAProxy logging fixed.")
	return nil
}

// profileDeclaration is the line apparmor's profile opens with, and the anchor
// the flag is added to. Matched exactly: a profile whose declaration hz does
// not recognise is reported rather than rewritten by guesswork.
const profileDeclaration = "profile rsyslogd /usr/sbin/rsyslogd {"
const profileDeclarationFixed = "profile rsyslogd /usr/sbin/rsyslogd flags=(attach_disconnected) {"

func (f logFixer) patchProfile(out io.Writer) error {
	data, err := os.ReadFile(f.profilePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", f.profilePath, err)
	}
	// Keep the profile's own mode. This file belongs to the distribution's
	// apparmor package; hz is adding a flag to it, not adopting it.
	mode := os.FileMode(0o644)
	if fi, statErr := os.Stat(f.profilePath); statErr == nil {
		mode = fi.Mode().Perm()
	}

	fixed := strings.Replace(string(data), profileDeclaration, profileDeclarationFixed, 1)
	if fixed == string(data) {
		return fmt.Errorf("could not find %q in %s to patch — add flags=(attach_disconnected) by hand",
			profileDeclaration, f.profilePath)
	}
	if err := os.WriteFile(f.profilePath, []byte(fixed), mode); err != nil {
		return fmt.Errorf("write %s: %w", f.profilePath, err)
	}
	_, _ = fmt.Fprintf(out, "Added flags=(attach_disconnected) to %s.\n", f.profilePath)

	if cmdOut, err := f.run("apparmor_parser", "-r", f.profilePath); err != nil {
		return fmt.Errorf("reload apparmor: %w%s", err, trailing(cmdOut))
	}
	_, _ = fmt.Fprintln(out, "Reloaded the apparmor profile.")
	return nil
}

func (f logFixer) createLogFile(out io.Writer) error {
	fh, err := os.OpenFile(f.logPath, os.O_CREATE|os.O_WRONLY, logFileMode)
	if err != nil {
		return fmt.Errorf("create %s: %w", f.logPath, err)
	}
	if err := fh.Close(); err != nil {
		return fmt.Errorf("create %s: %w", f.logPath, err)
	}
	// O_CREATE honours the umask, so the mode is set explicitly.
	if err := os.Chmod(f.logPath, logFileMode); err != nil {
		return fmt.Errorf("chmod %s: %w", f.logPath, err)
	}

	uid, haveUID := f.lookupID("user", logOwnerUser)
	gid, haveGID := f.lookupID("group", logOwnerGroup)
	if !haveUID || !haveGID {
		// Not a failure. A host with no syslog account runs rsyslog as root,
		// and a root-owned file is the right answer there — but say so, since
		// "created, owner not what the docs say" is otherwise a silent
		// difference somebody debugs later.
		_, _ = fmt.Fprintf(out, "Created %s (mode %04o), owned by the current user: this host has no %s:%s.\n",
			f.logPath, os.FileMode(logFileMode), logOwnerUser, logOwnerGroup)
		return nil
	}
	if err := f.chown(f.logPath, uid, gid); err != nil {
		return fmt.Errorf("chown %s to %s:%s: %w", f.logPath, logOwnerUser, logOwnerGroup, err)
	}
	_, _ = fmt.Fprintf(out, "Created %s, %s:%s, mode %04o.\n",
		f.logPath, logOwnerUser, logOwnerGroup, os.FileMode(logFileMode))
	return nil
}

// trailing renders a command's output as a suffix, or nothing.
func trailing(out []byte) string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return ""
	}
	return " — " + s
}
