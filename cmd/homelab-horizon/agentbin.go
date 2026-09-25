package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/iodesystems/homelab-horizon/internal/server/hzbin"
)

// Every `homelab-horizon install` also places the hz-agent binary on the box.
//
// The rule, from the operator (2026-09-25): "hz-agent owns it all. Any homelab
// horizon install also installs the agent." Not opt-in, no --agent flag.
//
// It exists to dissolve one objection. hz is handing privileged verbs to
// hz-agent (`wg create-config` first — plan/design/privilege-audit.md §7 and
// the decision list at the end of that file), and the standing argument
// against every such hand-over has been "the agent is not there at bootstrap".
// A verb cannot move to a binary that may not exist. Making the binary arrive
// with hz removes the objection without adding a flag somebody has to
// remember, and it costs nothing new: a release hz ALREADY carries the agent
// for all three served arches (internal/server/hzbin, behind the hzembed tag,
// served at /admin/hz-agent/bin/<os>-<arch>). This writes out bytes hz is
// already shipping.
//
// ────────────────────────────────────────────────────────────────────────
// DO NOT add `systemctl enable`, `systemctl start`, a daemon-reload, or a
// unit file here, however helpful it looks. INSTALLING A BINARY IS NOT
// ARMING IT. Shipping the agent everywhere makes the flip POSSIBLE; it must
// not make it HAPPEN. hz still runs as root and still applies its own config
// — a second process reconciling the same haproxy is how the gateway breaks.
//
// The inertness rules live in cmd/hz-agent/install.go and are pinned by
// cmd/hz-agent/install_test.go: no [Install] section, no --apply in
// ExecStart, nothing enabled, nothing started. This file writes ONE file and
// runs NO command; cmd/homelab-horizon/agentbin_test.go fails if that stops
// being true.
// ────────────────────────────────────────────────────────────────────────
//
// It also writes no unit, deliberately. `hz-agent install` already writes
// /etc/systemd/system/hz-agent.service, and it enrols the machine first
// because a unit that cannot authenticate is the half-done shape that hid a
// real bug. Two commands writing the same unit is a drift source, and hz
// could not render the same text anyway: the unit's ExecStart comes from the
// agent's own os.Executable(). So the split is by capability — hz puts the
// bytes on the box, and the agent's own installer, run by a human with sudo,
// does everything that involves systemd.
const (
	// agentInstallDir is where the agent's own installer expects to find it
	// (cmd/hz-agent/install.go execPath fallback), beside the hz binary.
	agentInstallDir = "/usr/local/bin"
	agentBinaryName = "hz-agent"
	// agentBinaryMode matches the hz binary beside it: root-owned (install
	// refuses without root long before reaching here) and 0755. Nothing here
	// widens anything — no setuid, no group write.
	agentBinaryMode os.FileMode = 0o755
)

// platformKey is the hzbin key for a GOOS/GOARCH pair, and it must match the
// names the Makefile's hz-embed target cross-compiles — `hz-agent-linux-arm`
// for 32-bit ARM, not `-armv7`, even though the Makefile passes GOARM=7.
// Getting this wrong means a Raspberry Pi install silently reports "no agent
// for this machine" on a build that carries one.
func platformKey(goos, goarch string) string { return goos + "-" + goarch }

// agentPlatformKey is the ONE arch hz writes out: its own.
//
// The embed carries three because the server serves them to other machines
// over /admin/hz-agent/bin/. install is not doing that — the agent it places
// runs on this box, beside this hz, so hz's own GOOS/GOARCH is the only
// correct answer and writing the other two would be two binaries that can
// never execute here.
func agentPlatformKey() string { return platformKey(runtime.GOOS, runtime.GOARCH) }

// agentSource yields the agent bytes for this machine. A function so tests do
// not need a tagged build to exercise the writing, and so the empty-embed case
// is a value rather than a build configuration.
type agentSource func() ([]byte, bool)

// embeddedAgent is the real source: this hz binary's own embedded copy. False
// under a plain `go build` (no hzembed tag), where the embed is a stub.
func embeddedAgent() ([]byte, bool) { return hzbin.Get(hzbin.ToolAgent, agentPlatformKey()) }

// agentAvailable lists the arches this build carries, and is a var so a test
// can reach the "carries agents, but not yours" message without a tagged
// build. The two not-shipped cases read very differently to an operator — an
// untagged binary has nothing, a release binary on an unusual arch has three
// and none of them fits — and only one of them is `make build`'s fault.
var agentAvailable = func() []string { return hzbin.Available(hzbin.ToolAgent) }

// shipAgent places the agent binary in dir, or explains why it cannot.
//
// Never an error. An hz built without the hzembed tag never had the bytes, and
// failing an install over that would fail CI's own binary and every
// `go build`-from-source box for something the operator can fix in one scp.
// The unit really was installed either way; saying so precisely is the
// deliverable, the same call installService already makes for missing
// dependency packages.
func shipAgent(dir string, src agentSource, dryRun bool, out io.Writer) error {
	target := filepath.Join(dir, agentBinaryName)

	want, ok := src()
	if !ok {
		_, _ = fmt.Fprintf(out, "hz-agent: NOT shipped — this homelab-horizon carries no agent for %s.\n",
			agentPlatformKey())
		if avail := agentAvailable(); len(avail) > 0 {
			_, _ = fmt.Fprintf(out, "  It embeds %v, none of which runs on this machine.\n", avail)
		} else {
			_, _ = fmt.Fprintln(out, "  It was built without the 'hzembed' tag — a plain `go build`, or CI —")
			_, _ = fmt.Fprintln(out, "  so there are no agent bytes in it to write out. `make build` produces")
			_, _ = fmt.Fprintln(out, "  one that has them; a release binary always does.")
		}
		_, _ = fmt.Fprintf(out, "  Nothing was written. Until a binary is at %s, hz-agent cannot be\n", target)
		_, _ = fmt.Fprintln(out, "  installed on this box — which is a prerequisite for the privileged")
		_, _ = fmt.Fprintln(out, "  verbs moving to it (plan/design/privilege-audit.md §7).")
		return nil
	}

	have, readErr := os.ReadFile(target)
	switch {
	case readErr == nil && bytes.Equal(have, want):
		// Idempotent by content, not by existence: a second install is a
		// no-op, but an install over an OLDER agent still replaces it.
		_, _ = fmt.Fprintf(out, "hz-agent: %s is already this build (%d bytes); left alone.\n", target, len(want))
		return nil
	case dryRun && readErr == nil:
		_, _ = fmt.Fprintf(out, "DRY RUN: would replace %s (%d bytes -> %d), mode %04o, and start nothing.\n",
			target, len(have), len(want), agentBinaryMode)
		return nil
	case dryRun:
		_, _ = fmt.Fprintf(out, "DRY RUN: would write %s (%d bytes), mode %04o, and start nothing.\n",
			target, len(want), agentBinaryMode)
		return nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	// Write-temp + rename, in the same directory so the rename is atomic.
	//
	// Not tidiness: writing over a RUNNING executable in place fails outright
	// with ETXTBSY ("text file busy"), and a partial write to a path something
	// is about to exec is worse than either. Renaming replaces the directory
	// entry and leaves a running process on its old inode.
	tmp, err := os.CreateTemp(dir, "."+agentBinaryName+"-*")
	if err != nil {
		return fmt.Errorf("creating a temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds
	if _, err := tmp.Write(want); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	// Explicit, because CreateTemp makes 0600 and the umask is not ours.
	if err := os.Chmod(tmpName, agentBinaryMode); err != nil {
		return fmt.Errorf("setting the mode on %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("installing %s: %w", target, err)
	}

	replaced := readErr == nil
	verb := "Installed"
	if replaced {
		verb = "Replaced"
	}
	_, _ = fmt.Fprintf(out, "hz-agent: %s %s (%d bytes, mode %04o).\n", verb, target, len(want), agentBinaryMode)
	if replaced {
		// Say it rather than act on it. On Linux a running process holds the
		// old inode, so the new bytes take effect at the next start — and
		// install does not do that start. Restarting the agent is an arming
		// decision; nothing here makes one.
		_, _ = fmt.Fprintln(out, "  An agent that is already running keeps the old binary until it is")
		_, _ = fmt.Fprintln(out, "  restarted. install does NOT restart it: the agent is inert and")
		_, _ = fmt.Fprintln(out, "  starting it is a decision a person makes, not an install side effect.")
	}
	_, _ = fmt.Fprintln(out, "  The binary is on the box and nothing else changed: no unit was written,")
	_, _ = fmt.Fprintln(out, "  nothing was enabled, nothing was started. To enrol this machine and")
	_, _ = fmt.Fprintf(out, "  install the (still inert) unit:  sudo %s install\n", target)
	return nil
}
