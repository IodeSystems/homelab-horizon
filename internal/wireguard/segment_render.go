package wireguard

// The PURE renderer for a SEGMENT interface: one machine's config for one
// network segment, computed by hz for a box hz cannot open, and applied on that
// box by hz-agent. Same rules as render.go and guarded by the same test
// (seam_test.go, pureFiles).
//
// # NOT A wg-quick FILE, AND NO PrivateKey LINE
//
// The output is wg(8) format, for `wg syncconf <iface> <file>`. It carries no
// PrivateKey, no Address, no PostUp/PostDown, no Table and no DNS:
//
//   - PrivateKey: the segment key is minted ON THE BOX
//     (internal/agent/segmentkey.go) and never leaves it. hz renders this file
//     without ever holding it; the agent loads the key into the interface
//     itself (`wg set <iface> private-key <file>`). A file with a PrivateKey
//     line would have to be rendered by something that holds the key.
//   - Address: `wg` does not read one. The agent sets it with `ip address`.
//   - PostUp/PostDown/Table: wg-quick directives. PostUp is a shell string run
//     as root, and a remote box running shell text hz wrote is the thing
//     CLAUDE.md invariant 13 forbids. Nothing here writes a firewall rule, a
//     route to anywhere but the segment, or a forwarding flag — deny-by-default
//     forwarding (invariant 10) stays the box's state.

import (
	"fmt"
	"strings"
)

// SegmentInterface is what one machine's config for one segment is made of.
// Every field arrives as an argument; nothing is looked up.
type SegmentInterface struct {
	// Segment is the segment's name, for the header comment.
	Segment string

	// ListenPort is the UDP port this interface listens on. Zero omits the
	// line, and the kernel picks one — right for a spoke, which dials out and
	// is never dialled.
	ListenPort int

	// Peers are the `[Peer]` blocks, in order.
	Peers []SegmentPeer
}

// SegmentPeer is one `[Peer]` block. AllowedIPs is whatever the caller derived
// (internal/projection derives it from CIDR + hub); this file does not decide
// routing.
type SegmentPeer struct {
	Name       string
	PublicKey  string
	AllowedIPs []string

	// Endpoint is host:port to dial. Empty omits the line.
	Endpoint string

	// PersistentKeepalive in seconds. Zero omits the line. A spoke behind NAT
	// sets it towards its hub so the hub can reach it back.
	PersistentKeepalive int
}

// RenderSegmentConfig renders one segment interface's wg(8) config.
//
// Deterministic: the same input gives the same bytes, so the agent's
// write-if-changed and hz's payload fingerprint both see "no change" as no
// change. The `[Peer]` block itself is RenderPeerBlock — the same stanza the
// human VPN's wg0.conf uses — extended with the two lines a segment peer can
// carry.
//
// Every interpolated value is reduced to one line first. A value is one line
// on a valid record; this makes a record with a newline in a name or an
// endpoint unable to add a directive to the file.
func RenderSegmentConfig(s SegmentInterface) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by homelab-horizon: segment %s. Edits are overwritten.\n", oneLine(s.Segment))
	b.WriteString("# wg(8) format for `wg syncconf`, not wg-quick. There is no PrivateKey line:\n")
	b.WriteString("# the key was minted on this machine, stays on it, and hz-agent loads it.\n")
	b.WriteString("[Interface]\n")
	if s.ListenPort > 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", s.ListenPort)
	}
	for _, p := range s.Peers {
		ips := make([]string, 0, len(p.AllowedIPs))
		for _, ip := range p.AllowedIPs {
			ips = append(ips, oneLine(ip))
		}
		b.WriteString(RenderPeerBlock(oneLine(p.Name), oneLine(p.PublicKey), strings.Join(ips, ", ")))
		if e := oneLine(p.Endpoint); e != "" {
			fmt.Fprintf(&b, "Endpoint = %s\n", e)
		}
		if p.PersistentKeepalive > 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.PersistentKeepalive)
		}
	}
	return b.String()
}

// oneLine drops every CR and LF and trims the result.
func oneLine(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(s))
}
