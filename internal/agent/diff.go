package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// This file is PURE, and it is the one that must never leak. Everything a
// human, a log line or a failing test sees about a change comes through here.
//
// Two layers, because one is a promise and the other is a check:
//
//  1. File.Secret, declared by whatever produced the payload. A secret file is
//     described by size only — never by content.
//  2. redactLine, applied to every emitted line regardless of that flag. A
//     file wrongly marked public still does not hand over a private key.
//
// Layer 2 exists because layer 1 is a human decision made somewhere else, and
// "the producer will remember" is not a security property.

// maxDiffLines bounds how much of a changed file reaches the report. A diff
// report is a summary an operator reads, not a copy of the file; an haproxy.cfg
// rewrite would otherwise put two thousand lines in a log.
const maxDiffLines = 3

// secretAssignment matches `<key> = <value>` where the key names something
// that must not be printed. WireGuard's PrivateKey and PresharedKey are the
// concrete cases on the gateway; the rest are there because the next file the
// agent is handed will not be a WireGuard config.
//
// The leading group allows a `+`/`-` DIFF MARKER, and that is not cosmetic.
// describeTextChange redacts a line and then prefixes it, so its own output
// was always safe — but this function is also the SECOND pass over text that
// has already been formatted: Report re-redacts every line it prints, and
// StateReport.Sanitized re-redacts every Detail a machine sent hz. Without
// the marker, a line that arrives already reading "  + PrivateKey = …" walks
// straight through both of those. A second pass that cannot match the shape
// its own first pass emits is not a second layer.
var secretAssignment = regexp.MustCompile(`(?i)^(\s*[-+]?\s*)([a-z0-9_.\-]*(private[_ -]?key|preshared[_ -]?key|secret|password|passphrase|token|credential)[a-z0-9_.\-]*)(\s*[:=]\s*)(.*)$`)

// redactLine blanks the value of any assignment whose key names key material,
// and passes everything else through unchanged.
//
// Applied to every line the report emits, not only to files marked secret.
func redactLine(line string) string {
	m := secretAssignment.FindStringSubmatch(line)
	if m == nil {
		return line
	}
	return m[1] + m[2] + m[4] + "[redacted]"
}

// describeTextChange summarises a file that exists and differs.
//
// A secret file gets sizes and nothing else: the fact that a key rotated is
// operationally useful, the key is not.
func describeTextChange(current, desired string, secret bool) string {
	if secret {
		return fmt.Sprintf("contents differ (%d bytes on disk, %d desired) — not shown, this file carries key material",
			len(current), len(desired))
	}

	added, removed := lineDelta(current, desired)
	head := fmt.Sprintf("contents differ (+%d/-%d lines)", len(added), len(removed))

	var parts []string
	parts = append(parts, head)
	for _, l := range sample(removed, maxDiffLines) {
		parts = append(parts, "  - "+truncate(redactLine(l), 120))
	}
	for _, l := range sample(added, maxDiffLines) {
		parts = append(parts, "  + "+truncate(redactLine(l), 120))
	}
	if len(removed) > maxDiffLines || len(added) > maxDiffLines {
		parts = append(parts, "  … truncated")
	}
	return strings.Join(parts, "\n")
}

// lineDelta reports which lines are only in desired and which only in current.
//
// Multiset difference rather than a real LCS diff: the report says what moved,
// not where. An operator deciding whether to let the agent apply needs "these
// four backend lines appear and these four go", and an alignment algorithm
// would buy nothing for that at the cost of being wrong in interesting ways.
func lineDelta(current, desired string) (added, removed []string) {
	count := map[string]int{}
	for _, l := range strings.Split(current, "\n") {
		count[l]++
	}
	for _, l := range strings.Split(desired, "\n") {
		if count[l] > 0 {
			count[l]--
			continue
		}
		added = append(added, l)
	}

	want := map[string]int{}
	for _, l := range strings.Split(desired, "\n") {
		want[l]++
	}
	for _, l := range strings.Split(current, "\n") {
		if want[l] > 0 {
			want[l]--
			continue
		}
		removed = append(removed, l)
	}
	return added, removed
}

func sample(lines []string, n int) []string {
	out := make([]string, 0, n)
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, l)
		if len(out) == n {
			break
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Report renders a plan as the text `hz-agent diff` prints and the daemon
// logs. It is the data behind the redesign's drift screen
// (plan/ui-redesign.md), which is why the plan carries structure and this
// function only formats it.
func Report(p Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "machine:    %s\n", orNone(p.Machine))
	fmt.Fprintf(&b, "generation: %s\n", orNone(p.Generation))

	pending, unknown := p.Pending(), p.Unknown()
	switch {
	case len(pending) == 0 && len(unknown) == 0:
		b.WriteString("state:      in sync — nothing to apply\n")
	case len(pending) == 0:
		fmt.Fprintf(&b, "state:      nothing to apply, but %d target(s) could not be read\n", len(unknown))
	default:
		fmt.Fprintf(&b, "state:      %d change(s) would be applied", len(pending))
		if len(unknown) > 0 {
			fmt.Fprintf(&b, ", %d target(s) could not be read", len(unknown))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	for _, c := range p.Changes {
		fmt.Fprintf(&b, "  [%s] %s %s\n", c.Kind, c.Subsystem, c.Target)
		for _, line := range strings.Split(c.Detail, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			// c.Detail is already redacted for the file paths that went
			// through describeTextChange; redact again because this function
			// is the last thing between a Change and a terminal, and a Change
			// can be constructed anywhere.
			fmt.Fprintf(&b, "        %s\n", redactLine(line))
		}
	}
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
