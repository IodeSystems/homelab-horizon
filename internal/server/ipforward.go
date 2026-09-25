package server

import (
	"path/filepath"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// hz DECLARES IP forwarding as two files; it does not turn it on here.
//
// This is privilege-audit.md §7 B's "move ip-forwarding as two File entries"
// AND plan/icebox.md's "IP forwarding is never persisted", and they are one
// change because fixing the bug properly IS the hand-over: what was missing is
// a persistent FILE, and a file is the thing the agent owns.
//
// hz keeps writing both itself (wireguard.EnableIPForwarding, reached from the
// fixer button and from `homelab-horizon check --fix`) until item 12 step 5.
// What this adds is that the agent is now ABLE to own them, and that both
// writers work from one set of constants — ipforward_test.go runs each over an
// identically seeded directory and compares names, bytes and modes.
//
// # /proc/sys/net/ipv4/ip_forward IS NOT AN ORDINARY FILE
//
// Three things follow that a File entry has to get right rather than assume,
// and all three were worked out against what the agent's applier actually does
// with it (internal/agent/apply.go writeIfChanged):
//
//  1. CONVERGENCE IS A NEWLINE. procfs prints the flag back as "1\n", so a
//     desired "1" — which is what hz's own writer wrote until this change —
//     never equals what the agent reads. The agent would rewrite the kernel
//     flag on every pass and `hz-agent diff` would report drift forever on a
//     correct machine. wireguard.IPForwardOn carries the newline; both writers
//     take the value from there.
//
//  2. THE MODE IS TRUE AND UNENFORCEABLE. os.WriteFile passes a permission
//     only when it CREATES the inode, and this inode always exists; procfs
//     refuses a chmod outright. 0644 is declared because that is what the node
//     already reports, so the payload states something true — but a payload
//     that asked for a different mode would be silently ignored, and nothing
//     in the agent would say so. Do not treat Mode as a lever here.
//
//  3. THE WRITE IS THE SHELL'S WRITE. os.WriteFile opens
//     O_WRONLY|O_CREATE|O_TRUNC, which is exactly what `echo 1 >
//     /proc/sys/net/ipv4/ip_forward` has always done — procfs accepts the
//     truncate and ignores it — and writeIfChanged's MkdirAll is a no-op
//     wherever procfs is mounted, which is everywhere the agent runs.
//
// # WHY A File ANYWAY, RATHER THAN A TYPED SYSCTL SECTION
//
// Because the apply semantics are "put these bytes at this path, then poke
// nothing", which is the exact line Desired.Files draws. A named sixth section
// would buy a branch on both sides of a version boundary, an apply half with
// nothing in it, and one more type to keep in step — and it would end up
// writing the same bytes to the same path. The one thing a typed section could
// add is refusing a mode it cannot apply (point 2), and that is a property of
// procfs, not of the wire.
//
// # WHAT IT MUST NEVER BE IS PRUNABLE
//
// The claim below is on /etc/sysctl.d and nothing else, narrowed to hz's own
// name shape. A claim reaching /proc is additionally UNREPRESENTABLE — the
// agent refuses one (internal/agent/ownership.go, kernelStateRoots) — because
// the payload now names a path under /proc and the directory above it would
// otherwise be one claim away from a prune.

// sysctlPaths is where the two declared files go.
//
// A struct computed from the Server, like staticPaths and for the same reason:
// the tests that apply hz's payload with the REAL agent must not rewrite
// /etc/sysctl.d or poke the kernel of the machine running the suite.
type sysctlPaths struct {
	// DropInDir is the sysctl.d directory the persistent half goes in, and
	// the only directory this declaration claims.
	DropInDir string

	// Runtime is the live kernel flag.
	Runtime string
}

// sysctlPaths is where this hz says the two files go. Empty fields mean the
// real ones.
func (s *Server) sysctlPaths() sysctlPaths {
	p := sysctlPaths{DropInDir: s.sysctlDir, Runtime: s.ipForwardPath}
	if p.DropInDir == "" {
		p.DropInDir = wireguard.SysctlDropInDir
	}
	if p.Runtime == "" {
		p.Runtime = wireguard.IPForwardRuntimePath
	}
	return p
}

// withIPForwarding adds hz's forwarding declaration to the generic section.
//
// It takes the section rather than returning one of its own because Desired
// has exactly one Files field: the generic section is SHARED, and a second
// producer that replaced it would silently drop the static file server's unit.
// A nil section becomes one — a machine with no static sites still has a
// kernel.
//
// # THREE STATES, AND THE MIDDLE ONE IS THE POINT
//
//	no section at all      hz did not compute for this machine (a remote box)
//	the claim, no files    hz is not routing here: no opinion about the flag
//	the claim and the two  hz is routing here and forwarding must be on
//
// HZ NEVER DECLARES FORWARDING OFF. Not an omission — the flag is kernel-wide
// and shared with docker, libvirt and anything else that bridges its own
// networks, so a hz that wrote 0 would break them, and hz has never had a
// reason to want it off. "hz does not manage this" and "hz wants this off" are
// different states (CLAUDE.md §2) and only the first of them exists here.
//
// THE CLAIM IS MADE EVEN WHEN NOTHING IS LISTED, and that is what makes the
// middle state actionable rather than cosmetic: a claim with no matching file
// in the payload is how hz says "the drop-in I used to write is not wanted any
// more", exactly as the maintenance-page claim does when an admin clears a
// page. Without it, turning a gateway back into an ordinary box would leave
// hz's own drop-in behind, turning forwarding on at every boot, with nothing
// in the payload left to mention it. The live flag is NOT touched in that
// state — the drop-in stops setting it at the next boot, and hz does not get
// to decide that something else's forwarding should stop now.
func withIPForwarding(sec *agent.FilesSection, cfg *config.Config, p sysctlPaths) *agent.FilesSection {
	if cfg == nil {
		return sec
	}
	if sec == nil {
		sec = &agent.FilesSection{}
	}

	sec.Dirs = append(sec.Dirs, agent.Directory{
		Path:  p.DropInDir,
		Match: []string{wireguard.IPForwardDropInMatch},
	})

	// The same spelling of "this box routes" that iptables.ExpectedRules uses
	// (rules.go: an empty WGInterface returns no rules at all). One condition,
	// so the firewall that forwards and the flag that permits forwarding
	// cannot be declared for different machines.
	if cfg.WGInterface == "" {
		return sec
	}

	sec.Files = append(sec.Files,
		agent.File{
			Path:     filepath.Join(p.DropInDir, wireguard.IPForwardDropInName),
			Mode:     0o644,
			Contents: wireguard.RenderIPForwardDropIn(),
		},
		agent.File{
			Path: p.Runtime,
			// True, and not applicable: see point 2 at the top of this file.
			Mode:     0o644,
			Contents: wireguard.IPForwardOn,
		},
	)

	// NO Units ENTRY. There is nothing to poke: the live flag takes effect the
	// instant it is written, and the drop-in is read by systemd-sysctl at
	// boot. agent.Unit's empty Action exists for exactly this, and running
	// `systemctl restart systemd-sysctl` would re-apply every drop-in on the
	// box — including whatever else an admin has in that directory — to make a
	// change hz has already made directly.
	return sec
}
