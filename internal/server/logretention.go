package server

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// hz DECLARES the journald drop-in; it does not own the journal.
//
// This is privilege-audit.md §7 B's "move log-retention (§3.1 #13)", the first
// item to land after the §4.3 decision (one generic section, and the named
// ones stay — Desired.Files is in the tree). It is the same shape as
// ipforward.go and maintenance_pages: hz declares a File and a Directory
// claim, the agent owns them, hz keeps writing the same bytes from the same
// constants until item 12 step 5, and logretention_test.go runs both writers
// over identically seeded directories and compares names, bytes and modes.
//
// What hz writes today, and why a drop-in at all: PCI DSS 10.5.1 wants twelve
// months of audit history that survives a reboot, and the Ubuntu default
// satisfies neither (it is size-bounded, not time-bounded, and volatile on a
// box with no /var/log/journal). A drop-in rather than an edit to
// journald.conf because the main file is a package-managed default full of
// commented examples: rewriting it means owning a merge with every upgrade,
// while a drop-in is additive, visible in `systemd-analyze cat-config` and
// removable by deleting one file.
//
// # THE DIRECTORY CLAIM IS THE TRAP, AND IT IS THE SAME TRAP TWICE ALREADY
//
// /etc/systemd/journald.conf.d holds OTHER PEOPLE'S drop-ins — a distribution
// package's, an operator's, a config-management tool's. A Match of ["*.conf"]
// would have the agent delete every one of them the first time it ran. That is
// the errors-directory lesson (privilege-audit.md §7 B) and the /etc/sysctl.d
// one, and this is the third place it would have bitten. The claim is
// journalDropInMatch — hz's own name shape and nothing else.
//
// # THREE STATES, AND THE MIDDLE ONE DOES NOT EXIST HERE
//
//	no section at all      hz did not compute for this machine (a remote box)
//	nothing for journald   hz has no opinion about this machine's journal
//	claim + file + unit    hz keeps twelve months of it here
//
// ipforward.go's middle state is "the claim with no files", which is how hz
// says "I used to manage this and I do not any more". Journal retention has no
// such state and must not pretend to: hz has never had a way to ask for a
// SHORTER journal, so a claim with no file would exist only to delete the
// drop-in — and on a box where cfg.JournalRetention is simply unset (every box
// that predates the field, and every box whose operator never pressed the
// button) that delete is the wrong answer, silently, at PCI's expense. So when
// hz has no opinion it says so by ABSENCE, which is exactly what nil means
// everywhere else in a payload (CLAUDE.md §2: empty and unknown are different
// states — this is unknown, and it is spelled as unknown).
//
// Config.JournalRetention is the record that distinguishes the two, and
// adoptJournalRetention is how a machine that already has hz's drop-in gets
// one without an operator pressing the button a second time.
const (
	// journaldDropInDir is where journald reads drop-ins from. hz's unit
	// already creates it (config.go's ExecStartPre), which is one of the
	// directories §5.2's fourth rule wants re-justified at the flip — after
	// this hand-over the agent is the one that needs it.
	journaldDropInDir = "/etc/systemd/journald.conf.d"

	// journalDropInName is hz's file in it. 99- so it wins over the
	// distribution's own drop-ins, which is the opposite of the sysctl
	// drop-in's 70- and deliberately so: a retention floor an operator asked
	// for should not be silently shortened by a package default, whereas
	// forwarding must not override an admin who turned it off on purpose.
	journalDropInName = "99-homelab-horizon.conf"

	// journalDropInMatch is "hz's own journald drop-in, whatever number it was
	// written with" — the agent's directory claim and the sweep in
	// writeJournalDropInAt, from one constant.
	//
	// NARROW ON PURPOSE. See the header: a "*.conf" claim here deletes
	// somebody else's configuration.
	journalDropInMatch = "*-homelab-horizon.conf"

	// journaldUnit is what has to be poked for the drop-in to mean anything
	// before the next reboot.
	journaldUnit = "systemd-journald"

	// journalRetentionSpec and journalMaxUseSpec are the two bounds, and they
	// travel together: without a size cap, twelve months of logs can fill the
	// disk this gateway routes from, and filling that disk costs far more than
	// logging to it ever saved.
	journalRetentionSpec = "1year"
	journalMaxUseSpec    = "2G"
)

// renderJournalDropIn is the drop-in's contents, and the ONLY definition of
// them. Both writers take the bytes from here.
//
// Because they do, agreement between the writers is FREE and proves nothing
// about whether the file means what it is supposed to mean — the blind spot
// the ip-forwarding hand-over found the hard way (rendering
// net.ipv4.ip_forward=0 left its whole tree green). The answer there and here
// is the same: a separate test reads this file the way its real consumer does
// — journalPersistence and parseSystemdDuration, the two functions
// readJournalState feeds — and asserts what it MEANS.
func renderJournalDropIn() string {
	return "# Written by homelab-horizon for PCI DSS 10.5.1.\n" +
		"# Twelve months of audit history, with a size cap so a year of logs cannot\n" +
		"# fill the disk this gateway runs on.\n" +
		"[Journal]\n" +
		"Storage=persistent\n" +
		"MaxRetentionSec=" + journalRetentionSpec + "\n" +
		"SystemMaxUse=" + journalMaxUseSpec + "\n"
}

// writeJournalDropInAt is hz's OWN writer, with its directory as an ARGUMENT.
//
// Parameterised for the reason wireguard.EnableIPForwardingAt is: a test that
// runs hz's real writer must be able to run it without rewriting
// /etc/systemd/journald.conf.d on whatever machine the suite is on. There is
// no second implementation — the fixer handler is this function with the
// constant filled in.
//
// THE SWEEP IS THE SAME CLAIM THE AGENT MAKES, on hz's side of the seam and
// from the same glob: hz's own journald drop-in under any other number is
// removed, so a renumbering cannot leave two files setting one key and the two
// writers cannot leave different directories. It takes nothing else —
// journalDropInMatch is hz's namespace and nobody else's files are in it — and
// it removes plain files only.
//
// It stays until hz stops writing files at all (item 12 step 5). The point of
// both writers existing is that they can be compared.
func writeJournalDropInAt(dropInDir string) error {
	if dropInDir == "" {
		return errors.New("no journald drop-in directory")
	}
	if err := os.MkdirAll(dropInDir, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", dropInDir, err)
	}
	if err := os.WriteFile(
		filepath.Join(dropInDir, journalDropInName),
		[]byte(renderJournalDropIn()), 0644,
	); err != nil {
		return fmt.Errorf("writing the journald drop-in: %w", err)
	}
	return errors.Join(sweepJournalDropIns(dropInDir)...)
}

// sweepJournalDropIns removes hz's journald drop-in written under any name but
// the current one. Plain files only, and only names journalDropInMatch covers.
func sweepJournalDropIns(dropInDir string) []error {
	entries, err := os.ReadDir(dropInDir)
	if err != nil {
		return []error{fmt.Errorf("checking %s for stale journald drop-ins: %w", dropInDir, err)}
	}
	var errs []error
	for _, e := range entries {
		if !e.Type().IsRegular() || e.Name() == journalDropInName {
			continue
		}
		if ok, err := filepath.Match(journalDropInMatch, e.Name()); err != nil || !ok {
			continue
		}
		if err := os.Remove(filepath.Join(dropInDir, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("removing the stale journald drop-in %s: %w", e.Name(), err))
		}
	}
	return errs
}

// journaldPaths is where this hz says the drop-in goes. Empty means the real
// one.
//
// A struct computed from the Server, like staticPaths and sysctlPaths and for
// the same reason: the tests that apply hz's payload with the REAL agent must
// not rewrite /etc/systemd/journald.conf.d or restart the journal of the
// machine running the suite.
type journaldPaths struct {
	DropInDir string
}

func (s *Server) journaldPaths() journaldPaths {
	p := journaldPaths{DropInDir: s.journaldDir}
	if p.DropInDir == "" {
		p.DropInDir = journaldDropInDir
	}
	return p
}

// withLogRetention adds hz's journal declaration to the generic section.
//
// It takes the section rather than returning one of its own for the reason
// withIPForwarding does: Desired has exactly one Files field, so a second
// producer that RETURNED a section would silently drop the static file
// server's unit and the forwarding declaration.
//
// # THE CLAIM, THE FILE AND THE UNIT ARE ONE DECISION
//
// All three are emitted together or not at all. The claim without the file
// would be hz asking the agent to DELETE the drop-in, which is the wrong
// answer on every machine that has simply never set the flag; the file without
// the claim could never remove hz's own drop-in from an older number; and the
// file without the unit would be a retention change that does not take effect
// until the next reboot while the PCI card — which reads the configuration,
// not the running journal — already reports the control met.
//
// # THE UNIT POKE IS SECTION-WIDE, AND THAT IS A KNOWN OVER-POKE
//
// FilesSection.Units names units to poke when ANY file in the generic section
// moves, not per-file. So on a machine where hz manages the journal, changing
// a static site map also restarts systemd-journald. It is a socket-activated
// log daemon and a restart of it is cheap and lossless, the poke fires only
// when a file actually changed (apply.go: touched[SubsystemFiles]), and the
// alternative — no poke — is a retention setting that silently does not apply.
// Associating a unit with the file that needs it is in plan/icebox.md.
func withLogRetention(sec *agent.FilesSection, cfg *config.Config, p journaldPaths) *agent.FilesSection {
	if cfg == nil || !cfg.JournalRetention {
		return sec
	}
	if sec == nil {
		sec = &agent.FilesSection{}
	}

	sec.Dirs = append(sec.Dirs, agent.Directory{
		Path:  p.DropInDir,
		Match: []string{journalDropInMatch},
	})
	sec.Files = append(sec.Files, agent.File{
		Path:     filepath.Join(p.DropInDir, journalDropInName),
		Mode:     0o644,
		Contents: renderJournalDropIn(),
	})
	sec.Units = append(sec.Units, agent.Unit{
		Name:   journaldUnit,
		Action: agent.UnitRestart,
	})
	return sec
}

// adoptJournalRetention turns a machine that ALREADY has hz's drop-in into one
// that declares it. FIRST SIGHTING ADOPTS, NEVER ACTS (CLAUDE.md §11).
//
// The problem it solves is the one every late-added record has: hz has been
// writing this file from a button since before Config.JournalRetention
// existed, so on the office gateway — where PCI 10.5.1 already reads met — the
// flag is off and the file is there. Without adoption hz would never declare
// it, `hz-agent diff` would never carry it, and the checklist's "in sync for
// every served section" (§7 D) would be met by a section that is missing.
//
// It ACTS ON NOTHING. No file is written, no unit is poked; the only effect is
// that hz now says out loud what the machine already is. The reverse mistake —
// a machine with no drop-in being adopted into wanting one — cannot happen,
// because the file's presence is the whole test.
//
// Idempotent and self-correcting: once the flag is set this returns
// immediately, and if a future off-switch removes both the flag and the file,
// the next boot finds nothing to adopt.
func (s *Server) adoptJournalRetention() {
	if s.cfg().JournalRetention {
		return
	}
	path := filepath.Join(s.journaldPaths().DropInDir, journalDropInName)
	if _, err := os.Stat(path); err != nil {
		return
	}
	if err := s.updateConfig(func(c *config.Config) { c.JournalRetention = true }); err != nil {
		slog.Warn("log-retention: found hz's journald drop-in but could not record it", "path", path, "err", err)
		return
	}
	slog.Info("log-retention: adopted the journald drop-in this machine already has", "path", path)
}
