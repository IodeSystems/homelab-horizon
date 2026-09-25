package system

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// HAProxy's logging is diagnosed here and FIXED SOMEWHERE ELSE, on purpose.
//
// A chrooted haproxy reaches syslog over a unix socket that is, from inside the
// chroot, disconnected. Two things break it, both invisible from haproxy's own
// status:
//
//  1. rsyslogd's apparmor profile has no `attach_disconnected` flag, so the
//     kernel refuses the socket and every line is dropped silently;
//  2. /var/log/haproxy.log does not exist, and rsyslog drops privileges before
//     it could create one.
//
// hz used to fix both from an HTTP handler, by piping an hz-built shell string
// into `systemd-run … bash -c` to escape its own ProtectSystem=strict sandbox
// (POST /api/v1/haproxy/fix-logging). That is the §5.2 rule-1 shape — a shell
// string reaching a privileged executor — and the sandbox it was escaping is
// hz's own confinement, which makes "escape it" the wrong verb for a web
// request regardless of how the escape is spelt.
//
// So the split is by capability, the same one `install-deps` already makes
// (cmd/homelab-horizon/deps.go): patching an apparmor profile and bouncing
// rsyslog is provisioning, and provisioning is `sudo homelab-horizon
// fix-haproxy-logging` — a verb a human at the box or a provisioning script
// asks for by name. What stays in hz is this file: READING the two facts and
// saying which command fixes them. The card keeps its diagnosis and loses its
// button.
//
// Nothing here writes, execs or needs root. A diagnosis that needed privilege
// to run would be the thing we just removed.

// LogCheck is the state of one of the two facts. FOUR states, not a bool.
//
// The founding bug of this repo is an empty value that was indistinguishable
// from an absent one (CLAUDE.md §2), and this check had the same shape: the old
// checkHAProxyApparmor returned `true, ""` for ANY read error, so a profile hz
// could not read — a tightened mode, a confined hz after the flip — reported as
// healthy. "There is no apparmor profile for rsyslogd on this host" and "I could
// not tell" are different answers and only one of them is good news.
type LogCheck string

const (
	// LogOK — measured, and correct.
	LogOK LogCheck = "ok"
	// LogBroken — measured, and wrong. This is the only state the fix verb is
	// the answer to.
	LogBroken LogCheck = "broken"
	// LogUnknown — hz could not determine it. NOT a synonym for broken and not
	// a synonym for fine: it is a statement about the instrument.
	LogUnknown LogCheck = "unknown"
	// LogNotApplicable — the thing being checked does not exist on this host,
	// which is a complete answer rather than a failed one. A box whose rsyslogd
	// is unconfined has no apparmor profile to patch.
	LogNotApplicable LogCheck = "not_applicable"
)

const (
	// RsyslogAppArmorProfile is the profile that confines rsyslogd on Debian
	// and Ubuntu. Absent on a host that does not confine it.
	RsyslogAppArmorProfile = "/etc/apparmor.d/usr.sbin.rsyslogd"

	// HAProxyLogPath is the file haproxy's syslog lines land in.
	HAProxyLogPath = "/var/log/haproxy.log"

	// attachDisconnected is the profile flag that lets a confined rsyslogd
	// accept a connection from inside haproxy's chroot.
	attachDisconnected = "attach_disconnected"

	// FixHAProxyLoggingCommand is the remedy, named once so the health card,
	// the CLI and this package cannot suggest three different spellings.
	FixHAProxyLoggingCommand = "sudo homelab-horizon fix-haproxy-logging"
)

// HAProxyLogging is what hz can say about haproxy's logging without touching
// anything. Each half carries its own state and its own sentence, because
// "logging is broken" with no reason is a dead end for whoever reads the card.
type HAProxyLogging struct {
	AppArmor       LogCheck `json:"apparmor"`
	AppArmorDetail string   `json:"apparmor_detail,omitempty"`
	LogFile        LogCheck `json:"log_file"`
	LogFileDetail  string   `json:"log_file_detail,omitempty"`
}

// Broken reports whether either half was measured wrong.
func (d HAProxyLogging) Broken() bool {
	return d.AppArmor == LogBroken || d.LogFile == LogBroken
}

// Undetermined reports whether either half could not be measured. Checked
// BEFORE Broken by every caller that renders one verdict, so an unreadable
// host never renders as a healthy one.
func (d HAProxyLogging) Undetermined() bool {
	return d.AppArmor == LogUnknown || d.LogFile == LogUnknown
}

// Details are the non-empty sentences, in reading order.
func (d HAProxyLogging) Details() []string {
	var out []string
	for _, s := range []string{d.AppArmorDetail, d.LogFileDetail} {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// DiagnoseHAProxyLogging reads the two facts off this host.
//
// Paths are parameters so the tests drive real files rather than the live
// system; DiagnoseHAProxyLoggingHere is the production spelling.
func DiagnoseHAProxyLogging(profilePath, logPath string) HAProxyLogging {
	var d HAProxyLogging
	d.AppArmor, d.AppArmorDetail = checkAppArmorProfile(profilePath)
	d.LogFile, d.LogFileDetail = checkLogFile(logPath)
	return d
}

// DiagnoseHAProxyLoggingHere diagnoses this host's real paths.
func DiagnoseHAProxyLoggingHere() HAProxyLogging {
	return DiagnoseHAProxyLogging(RsyslogAppArmorProfile, HAProxyLogPath)
}

func checkAppArmorProfile(path string) (LogCheck, string) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		// A profile that already carries the flag is the healthy case.
		if strings.Contains(string(data), attachDisconnected) {
			return LogOK, ""
		}
		return LogBroken, fmt.Sprintf(
			"rsyslogd's apparmor profile (%s) has no %s flag, so the kernel refuses the "+
				"socket haproxy logs over from inside its chroot and every line is dropped silently",
			path, attachDisconnected)
	case errors.Is(err, fs.ErrNotExist):
		// Nothing confines rsyslogd here, so there is nothing to patch. A
		// complete answer, not a missing one.
		return LogNotApplicable, fmt.Sprintf(
			"no apparmor profile for rsyslogd at %s, so nothing confines it on this host", path)
	default:
		// The state the old bool could not express. Permission denied is the
		// live case — hz reads this as root today and will not after item 12 —
		// and reporting it as healthy is how a real breakage stays invisible.
		return LogUnknown, fmt.Sprintf(
			"could not read rsyslogd's apparmor profile (%s): %v — hz cannot tell whether "+
				"haproxy's logs are reaching syslog", path, err)
	}
}

func checkLogFile(path string) (LogCheck, string) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return LogOK, ""
	case errors.Is(err, fs.ErrNotExist):
		return LogBroken, fmt.Sprintf(
			"%s does not exist, and rsyslog drops privileges before it could create one", path)
	default:
		return LogUnknown, fmt.Sprintf(
			"could not stat %s: %v — hz cannot tell whether haproxy has a log file", path, err)
	}
}
