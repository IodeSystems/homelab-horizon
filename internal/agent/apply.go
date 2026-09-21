package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/dnsmasq"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// This file is the PRIVILEGED half: the whole list of things hz-agent needs
// root for.
//
//   - write the files hz rendered, under /etc
//   - reload haproxy, dnsmasq and wireguard through their own apply halves
//   - reconcile the firewall through iptables.Reconcile
//
// Nothing here decides what anything should say. hz's render halves did that
// (they are pure, and they stay in hz); plan.go decided what differs. This
// only writes and reloads.
//
// It RECONCILES, it does not blindly write. writeIfChanged carries the rule in
// its doc comment, and Apply reloads a subsystem only when one of that
// subsystem's files actually moved.

// writeIfChanged writes data to path only when the file's contents differ, and
// reports whether it wrote.
//
// This is the agent's one statement of the reload discipline, and it is the
// same one internal/haproxy states for hz: callers reload a subsystem when a
// write reports changed, so identical contents must not report changed — that
// reloads HAProxy for nothing. Stating it here rather than at each call site
// means a new writer gets the property by using this.
//
// A file that cannot be read — missing, or unreadable — counts as changed, so
// the first write always lands. The parent directory is created if needed.
func writeIfChanged(path string, data []byte, perm fs.FileMode) (changed bool, err error) {
	if prev, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(prev, data) {
		return false, nil
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return false, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// Reloader is the set of subsystem apply halves the agent drives.
//
// An interface, not direct calls, for one reason: every implementation behind
// it shells out to systemctl, haproxy or iptables, so without a seam here no
// test could ever prove that a no-op plan reloads nothing. That is the single
// most important property in this package and it has to be assertable.
type Reloader interface {
	HAProxy(sec *HAProxySection) error
	DNSMasq(sec *DNSMasqSection) error
	WireGuard(sec *WireGuardSection) error
	IPTables(sec *IPTablesSection, live []iptables.Rule) (iptables.Report, error)

	// Units pokes the generic section's units. There is no fifth apply half
	// behind this one — that is the point of the generic section: its files
	// have no semantics of their own, so all the agent can do after writing
	// them is what the payload said to do.
	Units(sec *FilesSection) error
}

// SystemReloader is the real one. Each method is a thin call into the apply
// half that already exists in the subsystem's own package — the agent adds no
// second implementation of any of them.
type SystemReloader struct{}

// HAProxy validates the config and reloads, via internal/haproxy's apply half.
func (SystemReloader) HAProxy(sec *HAProxySection) error {
	return haproxy.New(sec.ConfigPath, sec.StatsSocket).Reload()
}

// DNSMasq restarts dnsmasq, via internal/dnsmasq's unit half — which is a
// different privilege from writing the files and is why the reload is named
// separately here rather than folded into the write loop.
func (SystemReloader) DNSMasq(sec *DNSMasqSection) error {
	return dnsmasq.New(sec.ConfigPath, sec.HostsPath, sec.Interfaces, sec.Upstream).Reload()
}

// WireGuard syncs the changed config into the running interface.
func (SystemReloader) WireGuard(sec *WireGuardSection) error {
	return wireguard.NewConfig(sec.ConfigPath, sec.Interface).Reload()
}

// IPTables hands the wire's rule sets straight to internal/iptables' own
// reconciler. The agent does not re-derive a rule and does not shell out to
// iptables itself: one definition of what the rules are, one thing that
// installs them.
func (SystemReloader) IPTables(sec *IPTablesSection, live []iptables.Rule) (iptables.Report, error) {
	return iptables.Reconcile(
		live, sec.Expected, sec.Stale, sec.Blessed,
		sec.DefaultInterface, sec.LastLocalIface,
	), nil
}

// Units restarts or reloads each unit the generic section names.
//
// An action the agent does not recognise is an error rather than a no-op: a
// payload asking for something this binary cannot do is a version skew, and
// silently doing nothing about it is how a file lands and never takes effect.
func (SystemReloader) Units(sec *FilesSection) error {
	if sec == nil {
		return nil
	}
	for _, u := range sec.Units {
		if u.Name == "" || u.Action == "" {
			continue
		}
		switch u.Action {
		case UnitRestart, UnitReload:
		default:
			return fmt.Errorf("unit %s: unknown action %q", u.Name, u.Action)
		}
		if out, err := exec.Command("systemctl", u.Action, u.Name).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s %s: %w: %s", u.Action, u.Name, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Result is what one apply pass did.
type Result struct {
	Generation string
	Wrote      []string

	// Removed is what the prune deleted. Separate from Wrote because they are
	// different in kind: a rewritten file can be rendered again, a removed one
	// is gone, and an operator reading a result should not have to work out
	// which of these paths was which.
	Removed  []string
	Reloaded []Subsystem
	IPTables *iptables.Report
	Errors   []string
}

// Apply writes what differs and reloads what a write touched.
//
// It takes the plan as well as the payload so an apply can only ever do what a
// diff already said it would: the plan's unknowns are not written blind, and a
// caller that printed a report is applying that report.
//
// Applying needs root. The caller checks; this returns an error rather than
// half-writing if it was called anyway.
func Apply(d *Desired, p Plan, obs Observed, r Reloader) (Result, error) {
	res := Result{Generation: p.Generation}
	if d == nil {
		return res, nil
	}
	if r == nil {
		r = SystemReloader{}
	}

	// A target the agent could not read is a target it must not overwrite.
	// Writing over a file whose current contents are unknown is how a
	// reconcile turns into data loss.
	blocked := map[string]string{}
	for _, c := range p.Unknown() {
		blocked[c.Target] = c.Detail
	}

	touched := map[Subsystem]bool{}
	for _, of := range d.allFiles() {
		if why, stop := blocked[of.File.Path]; stop {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: refusing to write, %s", of.File.Path, why))
			continue
		}
		mode := fs.FileMode(of.File.Mode)
		if mode == 0 {
			mode = 0o644
		}
		changed, err := writeIfChanged(of.File.Path, []byte(of.File.Contents), mode)
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		if changed {
			res.Wrote = append(res.Wrote, of.File.Path)
			touched[of.Subsystem] = true
		}
	}

	// REMOVALS COME AFTER THE WRITES, before any reload.
	//
	// Both orderings matter. After the writes, because a file the payload
	// lists is never a removal candidate and writing first means the
	// directory is in its final state before anything is asked to read it.
	// Before the reloads, because the point of a prune is that the subsystem
	// stops seeing the file — a removal that landed after the reload would
	// take effect at some unrelated later reload.
	res.prune(d, p, touched)

	// A certificate is loaded by HAProxy at start and at reload, so a rotated
	// bundle takes effect the same way a changed config does. There is no
	// separate cert apply half to call and inventing one would mean a second
	// implementation of "reload haproxy" — so the cert section folds into
	// HAProxy's reload instead of carrying its own.
	if touched[SubsystemCerts] && d.HAProxy != nil {
		touched[SubsystemHAProxy] = true
	}

	if d.HAProxy != nil && touched[SubsystemHAProxy] {
		res.reload(SubsystemHAProxy, r.HAProxy(d.HAProxy))
	}
	if d.DNSMasq != nil && touched[SubsystemDNSMasq] {
		res.reload(SubsystemDNSMasq, r.DNSMasq(d.DNSMasq))
	}
	if d.WireGuard != nil && touched[SubsystemWireGuard] {
		res.reload(SubsystemWireGuard, r.WireGuard(d.WireGuard))
	}
	if d.Files != nil && touched[SubsystemFiles] {
		res.reload(SubsystemFiles, r.Units(d.Files))
	}

	// iptables has no file to change, so it reconciles on its own terms: the
	// live set is compared to the expected set every pass, and Reconcile is
	// itself a no-op when they agree. It runs only when the live set was
	// actually readable — see Observed.IPTablesReadable.
	if d.IPTables != nil && obs.IPTablesReadable {
		rep, err := r.IPTables(d.IPTables, obs.LiveRules)
		if err != nil {
			res.Errors = append(res.Errors, "iptables: "+err.Error())
		} else {
			res.IPTables = &rep
			res.Errors = append(res.Errors, rep.Errors...)
			if len(rep.Added) > 0 || len(rep.Deleted) > 0 {
				res.Reloaded = append(res.Reloaded, SubsystemIPTables)
			}
		}
	}

	if len(res.Errors) > 0 {
		return res, fmt.Errorf("apply finished with %d error(s)", len(res.Errors))
	}
	return res, nil
}

// prune removes what the plan said a claimed directory no longer holds — and
// asks the bound itself, again, about every single path before it unlinks it.
//
// THE RE-CHECK IS THE WHOLE POINT, and it is not defence in depth for its own
// sake. Without it the answer to "what can this delete" would be "whatever a
// Change says", and a Change is a plain struct: Compute builds one, but so
// does anything that can hand this function a Plan, and a Plan already crosses
// a wire in the other direction (observed.go). With it, the answer is
// "whatever Desired.prunable allows", which is a function of the PAYLOAD —
// the same payload the diff was computed from and the same one whose
// fingerprint the agent authenticated. A plan that names /etc/passwd removes
// nothing; it produces a refusal that says so.
//
// The subsystem to reload comes from the bound too, not from the Change's own
// label, so a mislabelled removal cannot reload something else.
func (res *Result) prune(d *Desired, p Plan, touched map[Subsystem]bool) {
	for _, c := range p.Changes {
		if c.Kind != KindRemove {
			continue
		}
		sub, ok := d.prunable(c.Target)
		if !ok {
			res.Errors = append(res.Errors, fmt.Sprintf(
				"%s: refusing to remove it, it is not a file this payload claims", c.Target))
			continue
		}
		// Lstat, not Stat: a symlink must be seen as a symlink. Nothing but a
		// plain file is ever unlinked, so a link planted in a claimed
		// directory is refused rather than followed.
		fi, err := os.Lstat(c.Target)
		if errors.Is(err, fs.ErrNotExist) {
			continue // already gone; a prune is a statement about the end state
		}
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: cannot remove it: %v", c.Target, err))
			continue
		}
		if !fi.Mode().IsRegular() {
			res.Errors = append(res.Errors, fmt.Sprintf(
				"%s: refusing to remove it, it is not a regular file", c.Target))
			continue
		}
		if err := os.Remove(c.Target); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: cannot remove it: %v", c.Target, err))
			continue
		}
		res.Removed = append(res.Removed, c.Target)
		touched[sub] = true
	}
}

func (res *Result) reload(s Subsystem, err error) {
	if err != nil {
		res.Errors = append(res.Errors, string(s)+" reload: "+err.Error())
		return
	}
	res.Reloaded = append(res.Reloaded, s)
}
