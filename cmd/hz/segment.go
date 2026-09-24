package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz segment` — the networks a machine's segment NAME resolves to.
//
// It mirrors `hz machine` and `hz project` exactly, including the halves that
// are easy to skip: rm is a DRY RUN without --confirm, it REFUSES while a
// machine is in the segment and names every one, and --cascade is opt-in and
// lists what it will take before it takes it.
//
// WHAT A SEGMENT IS: a project's machines form one (plan/design/architecture.md,
// "Segments"), hub and spoke, and the hub is normally hz's own box. It has a
// range and an interface of its own so that a machine in two segments has two
// interfaces rather than one that collides.
//
// WHAT IT IS NOT: a rung. It has no posture, no version and no services — a
// segment is a network. It names a PROJECT because somebody owns the network,
// and that ownership is what makes another project's machine on it read as a
// crossing rather than as a peer.
//
// WHERE MEMBERSHIP IS DECLARED: on the MACHINE (`hz machine add --segment`).
// This command addresses those memberships; it does not grant them. A machine
// that names the segment and has no address here is listed as UNADDRESSED,
// because a segment that is half declared must not look like a smaller segment.
func runSegment(c *client, args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "", "ls", "list":
		return segmentList(c, args)
	case "show":
		return segmentShow(c, args)
	case "add", "create":
		return segmentAdd(c, args)
	case "set":
		return segmentSet(c, args)
	case "rm", "remove", "delete":
		return segmentRm(c, args)
	default:
		return fmt.Errorf("unknown segment subcommand: %s (want ls, show, add, set or rm)", sub)
	}
}

func fetchSegments(c *client) ([]apitypes.SegmentResp, error) {
	var out []apitypes.SegmentResp
	if err := c.do(http.MethodGet, "/api/v1/segments", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

const segmentAddUsage = `usage: hz segment add <name> --project P --cidr C --interface I [--note "what for"]
                        [--member machine=M,address=A[,hub=true][,endpoint=H:P][,key=K]]...

Declares a network segment: what a machine's segment NAME resolves to. Until one
exists, a membership is a label and nothing resolves it — no interface, no
address, no peers.

  --project P    the project whose machines form this segment. Required: a
                 network nobody owns is a network nobody is responsible for,
                 and the owner is what makes another project's machine on it
                 read as a crossing.
  --cidr C       the segment's range, as a NETWORK (10.42.0.0/24, not
                 10.42.0.1/24). Checked not to overlap another segment's — two
                 segments on one range gives a machine in both two routes for
                 one prefix and one of them silently wins.
  --interface I  the WireGuard interface this segment lands on. Unique across
                 segments: that is what lets a machine be in two.
  --note TEXT    what the segment is for. Optional. It is NOT the reason a
                 machine bridges two segments — that lives on the machine.
  --member ...   address a machine that ALREADY names this segment. Repeatable.
                 Comma-separated key=value:
                   machine=<name>     required; must name this segment
                   address=<ip>       required; a bare IP inside --cidr
                   hub=true           the one member every other peers with
                   endpoint=<host:port>  where to dial it; the hub needs one
                   key=<publickey>    its WireGuard PUBLIC key for this
                                      interface. hz never holds the secret half.
                 A segment with any members needs exactly one hub — it is hub
                 and spoke, and a spoke with no hub peers with nothing.

Writes immediately. A declared segment renders nothing on any box: hz has no
whole-file WireGuard renderer, so nothing moves because this record exists. What
it confers is RESOLUTION — from the first segment on, every machine's membership
has to name a segment that exists.

Membership itself is declared on the machine:
  hz machine add <machine> --segment <segment>
`

func segmentAdd(c *client, args []string) error {
	fs := flag.NewFlagSet("segment add", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, segmentAddUsage) }
	project := fs.String("project", "", "the project whose machines form this segment")
	cidr := fs.String("cidr", "", "the segment's range, as a network (10.42.0.0/24)")
	iface := fs.String("interface", "", "the WireGuard interface this segment lands on")
	note := fs.String("note", "", "what this segment is for")
	var members repeatedFlag
	fs.Var(&members, "member", "address a machine on this segment (repeatable): machine=M,address=A[,hub=true][,endpoint=H][,key=K]")

	name, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" || fs.NArg() > 1 {
		return fmt.Errorf("%s", segmentAddUsage)
	}

	req := apitypes.SegmentAddReq{
		Name: name, Project: *project, CIDR: *cidr, Interface: *iface, Note: *note,
	}
	for _, spec := range members {
		m, err := parseMemberSpec(spec)
		if err != nil {
			return err
		}
		req.Members = append(req.Members, m)
	}

	var out apitypes.SegmentResp
	if err := c.do(http.MethodPost, "/api/v1/segments/add", req, &out); err != nil {
		return err
	}
	fmt.Printf("Declared segment %s in project %s.\n", out.Name, out.Project)
	printSegment(out, "  ")
	fmt.Println("\nEvery machine's segment membership now has to name a segment that exists.")
	fmt.Printf("Join a machine to it with:\n  hz machine add <machine> --segment %s\n", out.Name)
	return nil
}

// parseMemberSpec reads one --member value. Comma-separated key=value rather
// than a positional triple, because a positional one is unreadable the moment
// it has four fields and unguessable when it has three — and a value split on
// its FIRST '=' leaves base64 padding in a public key alone.
func parseMemberSpec(spec string) (apitypes.SegmentMemberAddReq, error) {
	out := apitypes.SegmentMemberAddReq{}
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return out, fmt.Errorf("--member %q: %q is not key=value. Want machine=M,address=A[,hub=true][,endpoint=H:P][,key=K]", spec, pair)
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "machine":
			out.Machine = strings.TrimSpace(v)
		case "address", "addr":
			out.Address = strings.TrimSpace(v)
		case "key", "publickey", "public_key":
			out.PublicKey = strings.TrimSpace(v)
		case "endpoint":
			out.Endpoint = strings.TrimSpace(v)
		case "hub":
			out.Hub = v == "" || strings.EqualFold(v, "true") || v == "1" || strings.EqualFold(v, "yes")
		default:
			return out, fmt.Errorf("--member %q: unknown field %q. Want machine, address, hub, endpoint or key", spec, k)
		}
	}
	if out.Machine == "" {
		return out, fmt.Errorf("--member %q names no machine — a member is a MACHINE at an address on this segment", spec)
	}
	if out.Address == "" {
		return out, fmt.Errorf("--member %q gives %s no address — a member hz cannot address is a membership, and that is declared with `hz machine add --segment`", spec, out.Machine)
	}
	return out, nil
}

const segmentSetUsage = `usage: hz segment set <name> [--cidr C] [--interface I] [--project P] [--note "what for"]
                        [--hub <machine>]
                        [--member machine=M[,address=A][,endpoint=H:P][,key=K]]...
                        [--unaddress <machine>]...
                        [--cascade] [--confirm]

Changes a segment that already exists, and the memberships on it. Only the flags
you pass are touched. This is how a member gets ADDRESSED or RE-ADDRESSED without
removing the segment and declaring it again.

  --member ...    address or re-address a machine that already names this
                  segment. Repeatable, same key=value spelling as 'segment add'.
                  Only the keys you give change: re-addressing a member keeps its
                  public key and its endpoint.
                    address=<ip>          a bare IP inside the range
                    endpoint=<host:port>  where to dial it; empty clears it
                    key=<publickey>       its PUBLIC key; empty clears it
                  There is no hub= here — see --hub.
  --unaddress M   drop M's address, leaving it IN the segment and unaddressed.
                  It peers with nothing until it is addressed again. Repeatable.
  --hub M         make M the hub, demoting whoever holds it. This is a TOPOLOGY
                  change, not a field edit: peers are derived hub and spoke, so
                  every member's peer set is rewired. The rewiring is printed
                  before it is written. There is no way to leave a segment with
                  members and no hub — the only move is naming a different one.
  --interface I   the interface the segment lands on. Still unique across
                  segments; a clash names the other segment and refuses.
  --project P     the project that owns it. Cannot be cleared.
  --note TEXT     what it is for. --note "" clears it.
  --cidr C        the range, as a NETWORK (10.42.0.0/24). THIS CAN STRAND
                  MEMBERS: any address outside the new range refuses the write
                  and is named. --cascade opts in to UNADDRESSING each one
                  (never renumbering — hz does not invent an address), and with
                  --cascade this is a DRY RUN until --confirm.

The NAME cannot be changed: it is the identity every machine's membership
resolves through, so renaming it here would rename it on every machine in the
segment. 'hz segment rm --cascade' and re-declare says that honestly.

Membership itself is still declared on the MACHINE (hz machine add <m>
--segment <s>). This command addresses a membership; it does not grant one.

Writes immediately unless it would strand a member — then it refuses, or
dry-runs under --cascade until --confirm.
`

func segmentSet(c *client, args []string) error {
	fs := flag.NewFlagSet("segment set", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, segmentSetUsage) }
	project := fs.String("project", "", "the project that owns this segment")
	cidr := fs.String("cidr", "", "the segment's range, as a network (10.42.0.0/24)")
	iface := fs.String("interface", "", "the WireGuard interface this segment lands on")
	note := fs.String("note", "", "what this segment is for; empty clears it")
	hub := fs.String("hub", "", "make this machine the hub, rewiring every member's peers")
	cascade := fs.Bool("cascade", false, "unaddress the members a new range would strand")
	confirm := fs.Bool("confirm", false, "actually write a change that strands members")
	var members, unaddress repeatedFlag
	fs.Var(&members, "member", "address or re-address a machine (repeatable): machine=M[,address=A][,endpoint=H][,key=K]")
	fs.Var(&unaddress, "unaddress", "drop a machine's address, leaving it in the segment (repeatable)")

	name, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" || fs.NArg() > 1 {
		return fmt.Errorf("%s", segmentSetUsage)
	}

	// fs.Visit reports only the flags actually on the command line, which is the
	// patch contract `hz env set` sets: absent means "leave it alone" and
	// `--note ""` means "clear it". A zero-value check could not tell those
	// apart, and clearing a note would be inexpressible.
	req := apitypes.SegmentSetReq{
		Name: name, Unaddress: unaddress, Cascade: *cascade, Confirm: *confirm,
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "project":
			req.Project = project
		case "cidr":
			req.CIDR = cidr
		case "interface":
			req.Interface = iface
		case "note":
			req.Note = note
		case "hub":
			req.Hub = hub
		}
	})
	for _, spec := range members {
		m, err := parseMemberSetSpec(spec)
		if err != nil {
			return err
		}
		req.Members = append(req.Members, m)
	}
	if req.Project == nil && req.CIDR == nil && req.Interface == nil && req.Note == nil &&
		req.Hub == nil && len(req.Members) == 0 && len(req.Unaddress) == 0 {
		return fmt.Errorf("nothing to set on segment %s — pass at least one of --project, --cidr, --interface, --note, --hub, --member or --unaddress\n%s",
			name, segmentSetUsage)
	}

	var out apitypes.SegmentSetResp
	if err := c.do(http.MethodPost, "/api/v1/segments/set", req, &out); err != nil {
		return err
	}
	return renderSegmentSet(name, *cascade, *confirm, out)
}

// renderSegmentSet prints the one answer a set gives: what is in the way, or
// what it would do, or what it did. The server computes all three — a client
// re-deriving them from a read before and a read after would be a second answer
// free to disagree with the one that wrote them.
func renderSegmentSet(name string, cascade, confirm bool, out apitypes.SegmentSetResp) error {
	if len(out.Blocked) > 0 {
		fmt.Printf("REFUSED: the new range would strand %d addressed member(s) of segment %s.\n\n",
			len(out.Blocked), name)
		printDependants(out.Blocked)
		fmt.Println("\nA member addressed outside its own segment's range is a config hz cannot save,")
		fmt.Println("and hz will not renumber a box for you — two machines on one address is the")
		fmt.Println("failure that would cause. Give each of the above an address inside the new")
		fmt.Println("range in the same command:")
		fmt.Printf("  hz segment set %s --cidr <range> --member machine=%s,address=<ip>\n", name, out.Blocked[0].Name)
		fmt.Println("or re-run with --cascade to UNADDRESS them (they stay in the segment).")
		return fmt.Errorf("segment %s: the new range would strand %d member(s)", name, len(out.Blocked))
	}

	if len(out.Changes) == 0 && out.HubMove == nil && len(out.Strands) == 0 {
		fmt.Printf("Segment %s already reads that way — nothing changed.\n", name)
		if out.Segment != nil {
			printSegment(*out.Segment, "  ")
		}
		return nil
	}

	if len(out.Changes) > 0 {
		fmt.Println("Changes:")
		for _, line := range out.Changes {
			fmt.Printf("  %s\n", line)
		}
	}
	printHubMove(out.HubMove)
	if len(out.Strands) > 0 {
		fmt.Printf("\n--cascade would UNADDRESS %d member(s) the new range puts outside itself:\n\n", len(out.Strands))
		printDependants(out.Strands)
		fmt.Println("\nThey stay IN the segment and peer with nothing until they are addressed again.")
	}

	if len(out.Strands) > 0 && !confirm {
		fmt.Println("\nDry run: nothing was written. Re-run with --confirm to write it.")
		return nil
	}
	if !out.OK {
		// Belt and braces: a run that wrote nothing and named no blocker would
		// otherwise report success.
		return fmt.Errorf("the server did not change segment %s and gave no reason — nothing was written", name)
	}
	fmt.Printf("\nSet segment %s.\n", name)
	if out.Segment != nil {
		printSegment(*out.Segment, "  ")
	}
	return nil
}

// printHubMove prints the rewiring moving the hub causes. It is printed in full
// rather than summarised because it is the part of this command that changes
// records nobody named: Peers is derived hub and spoke, so the new hub gains
// every spoke, the old one loses all but one, and every spoke swaps its single
// peer. None of that appears in a diff of the segment record.
func printHubMove(move *apitypes.SegmentHubMoveResp) {
	if move == nil {
		return
	}
	if move.From == "" {
		fmt.Printf("\nHUB SET: %s\n", move.To)
	} else {
		fmt.Printf("\nHUB MOVES: %s → %s\n", move.From, move.To)
	}
	fmt.Println("This is a topology change, not a field edit — peers are derived hub and spoke,")
	fmt.Println("so every member's peer set is rewired:")
	fmt.Printf("  %-24s %-28s %s\n", "MACHINE", "PEERS NOW", "WAS")
	for _, p := range move.Peers {
		fmt.Printf("  %-24s %-28s %s\n", p.Machine,
			dash(strings.Join(p.After, ", ")), dash(strings.Join(p.Before, ", ")))
	}
}

// parseMemberSetSpec reads one `segment set --member` value. It differs from
// parseMemberSpec in the one way a patch differs from a declaration: an ABSENT
// key means "leave it alone" and a present-but-empty one means "clear it", so
// every field is a pointer. Re-addressing a member must not silently drop the
// public key the operator did not retype.
func parseMemberSetSpec(spec string) (apitypes.SegmentMemberSetReq, error) {
	out := apitypes.SegmentMemberSetReq{}
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return out, fmt.Errorf("--member %q: %q is not key=value. Want machine=M[,address=A][,endpoint=H:P][,key=K]", spec, pair)
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "machine":
			out.Machine = v
		case "address", "addr":
			out.Address = &v
		case "key", "publickey", "public_key":
			out.PublicKey = &v
		case "endpoint":
			out.Endpoint = &v
		case "hub":
			// Refused rather than accepted, because hub is single-valued across
			// the whole segment: setting it on one member necessarily unsets it
			// on another, and a per-member flag invites two members each
			// claiming it. `--hub <machine>` cannot express the contradiction.
			return out, fmt.Errorf("--member %q: the hub is not a per-member field — it is one machine per segment, and moving it rewires every member's peers. Use `--hub %s`", spec, out.Machine)
		default:
			return out, fmt.Errorf("--member %q: unknown field %q. Want machine, address, endpoint or key (the hub is `--hub <machine>`)", spec, k)
		}
	}
	if out.Machine == "" {
		return out, fmt.Errorf("--member %q names no machine — a member is a MACHINE at an address on this segment", spec)
	}
	return out, nil
}

const segmentRmUsage = `usage: hz segment rm <name> [--cascade] [--confirm]

Removes a segment. Prints what that would take and writes NOTHING unless
--confirm is given.

Refuses while any machine is a member — a membership naming a segment that no
longer exists is a config hz will not save, and you would meet that later as a
validation error about a machine you never touched.

  --cascade   DROP the membership from every machine that names the segment.
              Each one is listed before it happens. A machine left in no segment
              at all is legal: it stays declared, it stays enrolled, and it
              peers with nothing.
  --confirm   actually remove it.
`

func segmentRm(c *client, args []string) error {
	fs := flag.NewFlagSet("segment rm", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, segmentRmUsage) }
	cascade := fs.Bool("cascade", false, "drop the membership from every machine in the segment")
	confirm := fs.Bool("confirm", false, "actually remove it; without this it is a dry run")

	name, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" || fs.NArg() > 1 {
		return fmt.Errorf("%s", segmentRmUsage)
	}

	var out apitypes.RemovalResp
	req := apitypes.SegmentRmReq{Name: name, Cascade: *cascade, Confirm: *confirm}
	if err := c.do(http.MethodPost, "/api/v1/segments/rm", req, &out); err != nil {
		return err
	}
	return renderRemoval("segment", name, *cascade, *confirm, out)
}

func segmentList(c *client, args []string) error {
	fs := flag.NewFlagSet("segment ls", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	segments, err := fetchSegments(c)
	if err != nil {
		return err
	}

	if len(segments) == 0 {
		fmt.Println("No segments declared.")
		fmt.Println("Every machine's segment membership is a LABEL until one exists: nothing")
		fmt.Println("resolves it to an interface, an address or a peer set.")
		fmt.Println("\n`hz segment add <name> --project <project> --cidr <range> --interface <iface>`")
		fmt.Println("declares the first one. From then on a membership has to name one that exists.")
		return nil
	}

	fmt.Printf("%-20s  %-16s  %-18s  %-12s  %-8s  %s\n",
		"SEGMENT", "PROJECT", "RANGE", "INTERFACE", "MEMBERS", "HUB")
	unaddressed := 0
	for _, s := range segments {
		unaddressed += len(s.Unaddressed)
		fmt.Printf("%-20s  %-16s  %-18s  %-12s  %-8s  %s\n",
			s.Name, s.Project, s.CIDR, s.Interface, segmentMemberCount(s), dash(segmentHubName(s)))
	}
	fmt.Println("\nMEMBERS counts the machines ADDRESSED on the segment; a count marked +N has N")
	fmt.Println("more that name the segment and have no address on it yet. HUB is the one member")
	fmt.Println("every other one peers with — hub and spoke, so a segment with members and no hub")
	fmt.Println("cannot form and is refused.")
	if unaddressed > 0 {
		fmt.Printf("\n%d membership(s) here are declared and not addressed. That is the state\n", unaddressed)
		fmt.Println("`hz machine add --segment` leaves; `hz segment show <name>` lists which.")
	}
	fmt.Println("\nMembership is declared on the MACHINE (`hz machine add <m> --segment <s>`) and")
	fmt.Println("addressed here. hz segment show <name> shows one segment.")
	return nil
}

func segmentShow(c *client, args []string) error {
	fs := flag.NewFlagSet("segment show", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: hz segment show <segment>")
	}
	want := fs.Arg(0)
	segments, err := fetchSegments(c)
	if err != nil {
		return err
	}
	for _, s := range segments {
		if s.Name != want {
			continue
		}
		fmt.Printf("%s\n", s.Name)
		printSegment(s, "  ")
		fmt.Println("\n  posture     — (a segment has none; a posture belongs to a rung)")
		fmt.Println("  version     — (likewise: a segment is a network, not an environment)")
		fmt.Println("  allowed IPs — (derived from the range and the hub, never stored twice)")
		return nil
	}
	known := make([]string, 0, len(segments))
	for _, s := range segments {
		known = append(known, s.Name)
	}
	if len(known) == 0 {
		return fmt.Errorf("no segment %q — this config declares no segments at all; every membership in it is still a label. `hz segment add %s --project <project> --cidr <range> --interface <iface>` declares one", want, want)
	}
	return fmt.Errorf("no segment %q — `hz segment ls` lists what exists. Declared: %s", want, strings.Join(known, ", "))
}

// printSegment renders one segment the way show does, so a write and a read of
// the same record look the same — the convention printEnvironment sets.
func printSegment(s apitypes.SegmentResp, prefix string) {
	fmt.Printf("%sproject     %s\n", prefix, dash(s.Project))
	fmt.Printf("%srange       %s\n", prefix, dash(s.CIDR))
	fmt.Printf("%sinterface   %s\n", prefix, dash(s.Interface))
	if s.Note != "" {
		fmt.Printf("%snote        %s\n", prefix, s.Note)
	}

	if len(s.Members) == 0 {
		fmt.Printf("%sNo machine is addressed on it yet. It is declared, so a membership can name\n", prefix)
		fmt.Printf("%sit; nothing peers with anything until a member is addressed, one of them the hub.\n", prefix)
	} else {
		fmt.Printf("%smembers\n", prefix)
		for _, m := range s.Members {
			role := "spoke"
			if m.Hub {
				role = "HUB"
			}
			fmt.Printf("%s  %-20s %-16s %-6s peers: %s\n",
				prefix, m.Machine, m.Address, role, dash(strings.Join(m.Peers, ", ")))
			if m.Endpoint != "" {
				fmt.Printf("%s    endpoint   %s\n", prefix, m.Endpoint)
			} else if m.Hub {
				fmt.Printf("%s    endpoint   — (the hub has none declared; a spoke has nothing to dial)\n", prefix)
			}
			if m.PublicKey == "" {
				fmt.Printf("%s    public key — (none yet; hz can route to it and cannot peer with it.\n", prefix)
				fmt.Printf("%s                 A box reports its key when it runs `hz-agent enroll`)\n", prefix)
			} else {
				fmt.Printf("%s    public key %s\n", prefix, m.PublicKey)
			}
		}
	}

	if len(s.Unaddressed) > 0 {
		fmt.Printf("%sUNADDRESSED %s\n", prefix, strings.Join(s.Unaddressed, ", "))
		fmt.Printf("%s  These machines name this segment and have no address on it. They ARE members;\n", prefix)
		fmt.Printf("%s  hz cannot say where they are, so nothing peers with them. Address one with:\n", prefix)
		fmt.Printf("%s    hz segment set %s --member machine=%s,address=<ip>\n", prefix, s.Name, s.Unaddressed[0])
	}
}

func segmentMemberCount(s apitypes.SegmentResp) string {
	if len(s.Unaddressed) == 0 {
		return fmt.Sprintf("%d", len(s.Members))
	}
	return fmt.Sprintf("%d+%d", len(s.Members), len(s.Unaddressed))
}

func segmentHubName(s apitypes.SegmentResp) string {
	for _, m := range s.Members {
		if m.Hub {
			return m.Machine
		}
	}
	return ""
}
