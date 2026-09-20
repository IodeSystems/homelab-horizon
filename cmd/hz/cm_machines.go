package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// The enrolled-machine surface: listing boxes, and removing one so its name can
// be enrolled again.
//
// This exists because hz already refused the other half and told the operator
// to do something no command did. Register a box under a name that is enrolled
// with a different public key and hz answers:
//
//	machine <name> is enrolled with a different public key; an operator must
//	remove it before it can re-enrol
//
// The refusal is right — silently accepting a new key would mean anything that
// can reach /register takes over an existing machine's identity, and the next
// approval gets wrapped to a key it holds. But there was no remove, so a box
// that lost its state directory (rebuilt VM, replaced disk, wiped /var/lib)
// could never come back under its own name. For a config manager, reprovisioning
// is the ordinary case.

// cmMachines lists every enrolled box.
func cmMachines(c *client, args []string) error {
	fs := flag.NewFlagSet("cm machines", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the raw response")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var rows []apitypes.CMMachineResp
	if err := c.do(http.MethodGet, apitypes.CMPathMachines, nil, &rows); err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(rows) == 0 {
		fmt.Println("No machines enrolled.")
		return nil
	}

	fmt.Printf("%-24s  %-12s  %-6s  %-8s  %-14s  %-10s  %s\n",
		"MACHINE", "ENROLLED", "ADDRS", "SECRETS", "OBSERVED", "REPORTED", "FINGERPRINT")
	for _, m := range rows {
		approved := 0
		for _, reg := range m.Registrations {
			if reg.State == configmgr.StateApproved {
				approved++
			}
		}
		observed, reported := observedForMachine(m)
		fmt.Printf("%-24s  %-12s  %-6s  %-8d  %-14s  %-10s  %s\n", m.Name, m.EnrolledEnvironment,
			fmt.Sprintf("%d/%d", approved, len(m.Registrations)),
			len(m.SecretKeys), observed, reported, orDash(m.Fingerprint))
	}
	fmt.Println("\nADDRS is approved/total registrations.")
	fmt.Println("OBSERVED is the version the box reported it is RUNNING, and REPORTED is how long")
	fmt.Println("ago it said so — a box reports on every config resolve, so a stale REPORTED means")
	fmt.Println("it has stopped asking. 'mixed' means this box's addresses reported DIFFERENT")
	fmt.Println("versions, which is the normal state mid-rollout; 'hz cm machines --json' shows")
	fmt.Println("each address on its own. hz only displays this — it never upgrades anything.")
	fmt.Println("Remove a box so its name can be enrolled again: 'hz cm remove <machine>'.")
	return nil
}

// observedForMachine reduces a box's per-instance observations to the one line
// a machine listing has room for.
//
// The reduction is the interesting part. A version belongs to an INSTANCE, not
// to a box: redline's `current` and `next` slots share a machine and run
// different versions for the whole length of a rolling deploy. So picking any
// single registration's version and printing it as the machine's would report a
// half-done rollout as done. When they disagree this says "mixed" instead and
// points at --json, which is the honest answer rather than a confident wrong
// one. The timestamp is the NEWEST report, so REPORTED answers "when did I last
// hear from this box at all".
func observedForMachine(m apitypes.CMMachineResp) (version, reported string) {
	seen := ""
	mixed := false
	newest := time.Time{}
	for _, reg := range m.Registrations {
		if reg.ObservedVersion == "" {
			continue
		}
		switch {
		case seen == "":
			seen = reg.ObservedVersion
		case seen != reg.ObservedVersion:
			mixed = true
		}
		if at, err := time.Parse(time.RFC3339, reg.ObservedAt); err == nil && at.After(newest) {
			newest = at
		}
	}
	if seen == "" {
		// Never reported: every registration predates the box's first report,
		// or the box has not resolved since it enrolled. A dash, never a blank
		// column an operator has to interpret.
		return "-", "-"
	}
	if mixed {
		seen = "mixed"
	}
	if newest.IsZero() {
		return seen, "-"
	}
	return seen, since(newest)
}

// since renders an age the way an operator reads one: coarse, and never a
// negative number if a box's clock runs ahead of hz's.
func since(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// cmRemove deletes one enrolled box.
//
// The shape is the approve ceremony's, for the same reason: read first, show
// the operator what they are acting on, then make them type something that only
// someone who read it can produce. Here that is the machine's own name, and it
// travels to hz as confirm=<name> — so the prompt is the thing the server
// checks, not a local courtesy the server would have accepted without.
//
// What is printed before the prompt is everything that is about to be destroyed
// by name: every address the box registered, which of them hold a grant, and
// every machine-scoped secret key. Printing counts alone would not let an
// operator tell the box they rebuilt from a box that took its name.
func cmRemove(c *client, args []string) error {
	fs := flag.NewFlagSet("cm remove", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: hz cm remove <machine-name-or-id> [--yes]\n\n"+
			"Removes an enrolled box so its name can be enrolled again — the fix for\n"+
			"\"machine X is enrolled with a different public key\" after a rebuild.\n\n"+
			"This destroys every registration the box holds (including approved ones)\n"+
			"and every machine-scoped secret sealed to its key. It is NOT a revocation:\n"+
			"a box that was ever approved already holds the environment key unwrapped on\n"+
			"its own disk. Rotating that key is the only revocation.\n")
	}
	yes := fs.Bool("yes", false, "skip the typed confirmation (the name in argv is the confirmation)")
	ref, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if ref == "" {
		ref = fs.Arg(0)
	}
	if ref == "" || fs.NArg() > 1 {
		fs.Usage()
		return fmt.Errorf("exactly one machine name or id is required")
	}

	path := apitypes.CMPathMachines + "/" + url.PathEscape(ref)
	var m apitypes.CMMachineResp
	if err := c.do(http.MethodGet, path, nil, &m); err != nil {
		var se *apiStatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			// The ordinary outcome of removing something twice, and of a typo.
			// Both deserve the listing rather than a stack of HTTP.
			return fmt.Errorf("no machine named or identified by %q is enrolled.\n"+
				"  'hz cm machines' lists what is", ref)
		}
		return err
	}

	fmt.Printf("Machine %s\n", m.Name)
	fmt.Printf("  id           %s\n", orDash(m.ID))
	fmt.Printf("  enrolled as  %s\n", orDash(m.EnrolledEnvironment))
	fmt.Printf("  fingerprint  %s\n", orDash(m.Fingerprint))
	fmt.Printf("  first seen   %s\n", orDash(m.CreatedAt))
	fmt.Printf("  last seen    %s\n", orDash(m.LastSeenAt))
	fmt.Println()

	grants := 0
	if len(m.Registrations) == 0 {
		fmt.Println("  no registrations")
	} else {
		fmt.Println("  registrations that will be destroyed:")
		for _, reg := range m.Registrations {
			addr := configmgr.EnvKeyAddr{Environment: reg.Environment, App: reg.App, Role: reg.Role}
			note := ""
			if reg.State == configmgr.StateApproved {
				grants++
				note = "  (holds a wrapped key)"
			}
			fmt.Printf("    %-30s  %s%s\n", addr, reg.State, note)
		}
	}
	if len(m.SecretKeys) > 0 {
		fmt.Println("  machine secrets that will be destroyed:")
		for _, k := range m.SecretKeys {
			fmt.Printf("    %s\n", k)
		}
		// Said plainly because it is the one loss that is not recoverable by
		// re-approving: these were sealed to the private key the box no longer
		// has, so they are already unopenable — but the ROWS are what hz would
		// have re-served, and they are going.
		fmt.Println("    (sealed to this box's key: a box with a new keypair cannot open them,")
		fmt.Println("     so they must be set again after it re-enrols)")
	}
	fmt.Println()

	// The honest scope of the act, printed every time, including under --yes.
	// The cost of an operator believing removal revokes something is that they
	// skip the rotation that actually would.
	fmt.Println("This does NOT revoke anything. A box that was ever approved holds the")
	fmt.Println("environment key unwrapped on its own disk; removing it here stops hz serving")
	fmt.Println("that box and frees the name. Rotating the key is the only revocation.")
	fmt.Println()

	if !*yes {
		fmt.Printf("Type the machine name to remove it: ")
		typed, err := readLine(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading the confirmation: %w", err)
		}
		// One shot, no retry loop — same rule as the fingerprint prompt. A
		// retry loop invites typing until something passes, which is the shape
		// of the mistake the prompt guards.
		if strings.TrimSpace(typed) != m.Name {
			return fmt.Errorf("refusing: you typed %q, the machine is %q. Nothing was removed",
				strings.TrimSpace(typed), m.Name)
		}
	}

	// confirm carries the name hz resolved, not the reference the operator
	// typed: `hz cm remove mch_abc123` is legitimate, and the server checks the
	// NAME. Sending the raw ref would make the id form fail its own guard.
	q := url.Values{apitypes.CMQueryConfirm: {m.Name}}
	var removed apitypes.CMMachineRemovedResp
	if err := c.do(http.MethodDelete, path+"?"+q.Encode(), nil, &removed); err != nil {
		return err
	}

	fmt.Printf("\nRemoved %s: %d registration(s), %d of them holding a grant, %d machine secret(s).\n",
		removed.Name, removed.RegistrationsRemoved, removed.GrantsRemoved, removed.SecretsRemoved)
	fmt.Printf("The name %q is free. The box's next boot registers as pending, with whatever\n", removed.Name)
	fmt.Println("keypair it holds now — approve it with 'hz cm approve <id>', comparing the")
	fmt.Println("fingerprint it prints. Nothing it held before carries over.")
	return nil
}
