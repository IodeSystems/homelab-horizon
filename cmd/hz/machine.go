package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz machine` — the boxes: identity and segment membership.
//
// It mirrors `hz project` and `hz env` exactly, including the halves that are
// easy to skip: rm is a DRY RUN without --confirm, it REFUSES while anything
// depends on the machine and names every dependant, and --cascade is opt-in and
// lists what it will take before it takes it.
//
// WHAT A MACHINE IS NOT, said here because the listing is where somebody would
// look for it: it has no project and no environment. Those are coordinates of
// an INSTANCE — the gateway hosts instances from two projects at once — so a
// project column on this table would be wrong for the most important row in the
// fleet (plan/architecture.md, "Instance, not machine, carries the
// environment"). It has no observed version either: that belongs to an
// instance, and several instances share a box, so a machine-level version would
// report a half-finished rollout as finished.
//
// WHAT IT DOES SHOW LOUDLY: a machine in more than one segment. Its blast
// radius is the UNION of its segments, which is only a useful sentence if the
// segments can be enumerated — so they are listed, the row is flagged, the
// declared reason is printed beside it, and `--multi-homed` narrows the whole
// listing to exactly those rows.
func runMachine(c *client, args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "", "ls", "list":
		return machineList(c, args)
	case "show":
		return machineShow(c, args)
	case "add", "create":
		return machineAdd(c, args)
	case "rm", "remove", "delete":
		return machineRm(c, args)
	default:
		return fmt.Errorf("unknown machine subcommand: %s (want ls, show, add or rm)", sub)
	}
}

func fetchMachines(c *client) ([]apitypes.MachineResp, error) {
	var out []apitypes.MachineResp
	if err := c.do(http.MethodGet, "/api/v1/machines", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

const machineAddUsage = `usage: hz machine add <name> [--segment S]... [--note "why"]

Declares a machine: its identity and which network segments it is in. A machine
has NO project and NO environment — those belong to an INSTANCE, and one box
hosts instances from several projects, so there is nothing here to put them on.

Writes immediately. A declared machine renders nothing — no DNS record, no
HAProxy backend, no apt source. What it confers is the right to ENROL: hz issues
an agent credential only for a machine it has been told about.

  --segment S  a network segment this machine is a member of. Repeatable.
               Usually one. More than one is legal and is flagged everywhere it
               appears: blast radius is the UNION of a machine's segments.
  --note TEXT  why. REQUIRED when more than one --segment is given: forwarding
               between a machine's own segment interfaces is denied by default,
               and a box that bridges segments is a declared exception with a
               reason, not a default.

A segment name must name a segment that EXISTS, once any is declared —
"hz segment ls" lists them. On a config that declares none, every membership is
still a label and the name is only checked for shape: nothing resolves it to an
interface, an address or a peer set.
`

// repeatedFlag collects a flag given more than once, which is how segment
// membership is expressed: `--segment a --segment b`. A comma-separated single
// value would make a segment called "a,b" the easiest typo to make.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ", ") }
func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

func machineAdd(c *client, args []string) error {
	fs := flag.NewFlagSet("machine add", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, machineAddUsage) }
	var segments repeatedFlag
	fs.Var(&segments, "segment", "a network segment this machine is in (repeatable)")
	note := fs.String("note", "", "why this machine is in more than one segment")

	name, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" || fs.NArg() > 1 {
		return fmt.Errorf("%s", machineAddUsage)
	}

	var out apitypes.MachineResp
	req := apitypes.MachineAddReq{Name: name, Segments: segments, Note: *note}
	if err := c.do(http.MethodPost, "/api/v1/machines/add", req, &out); err != nil {
		return err
	}
	fmt.Printf("Declared machine %s.\n", out.Name)
	printMachine(out, "  ")
	fmt.Println("\nIt has no project and no environment, and that is the model: those are")
	fmt.Println("coordinates of an instance, not of a box.")
	fmt.Printf("\nEnrol it from the box itself, as root:\n  hz-agent enroll --hz <hz url> --machine %s\n", out.Name)
	fmt.Println("hz issues the credential — the box does not mint its own.")
	return nil
}

const machineRmUsage = `usage: hz machine rm <name> [--cascade] [--confirm]

Removes a machine. Prints what that would take and writes NOTHING unless
--confirm is given.

Refuses while anything depends on it — today that is the machine's agent
credential — and names it. Removing the record and leaving the credential would
leave something that still authenticates for a machine hz no longer declares,
and nothing in the config would explain it.

  --cascade   REVOKE the machine's agent credential too. That box's agent stops
              being able to poll the moment this runs. What it will take is
              listed before it does.
  --confirm   actually remove it.
`

func machineRm(c *client, args []string) error {
	fs := flag.NewFlagSet("machine rm", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, machineRmUsage) }
	cascade := fs.Bool("cascade", false, "revoke the machine's agent credential too")
	confirm := fs.Bool("confirm", false, "actually remove it; without this it is a dry run")

	name, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" || fs.NArg() > 1 {
		return fmt.Errorf("%s", machineRmUsage)
	}

	var out apitypes.RemovalResp
	req := apitypes.MachineRmReq{Name: name, Cascade: *cascade, Confirm: *confirm}
	if err := c.do(http.MethodPost, "/api/v1/machines/rm", req, &out); err != nil {
		return err
	}
	return renderRemoval("machine", name, *cascade, *confirm, out)
}

func machineList(c *client, args []string) error {
	fs := flag.NewFlagSet("machine ls", flag.ContinueOnError)
	onlyMultiHomed := fs.Bool("multi-homed", false, "list only machines in more than one segment, and what each bridges")
	if err := fs.Parse(args); err != nil {
		return err
	}
	machines, err := fetchMachines(c)
	if err != nil {
		return err
	}

	if *onlyMultiHomed {
		return printMultiHomed(machines)
	}

	if len(machines) == 0 {
		fmt.Println("No machines declared.")
		fmt.Println("`hz machine add <name> --segment <segment>` declares the first one. A machine")
		fmt.Println("must be declared before hz will issue it an agent credential.")
		return nil
	}

	fmt.Printf("%-24s  %-10s  %-8s  %s\n", "MACHINE", "SEGMENTS", "ENROLLED", "MEMBER OF")
	bridges := 0
	for _, m := range machines {
		if m.MultiHomed {
			bridges++
		}
		fmt.Printf("%-24s  %-10s  %-8s  %s\n",
			m.Name, machineSegmentCount(m), yesNo(m.Enrolled), machineSegments(m))
	}
	fmt.Println("\nSEGMENTS counts them; a count above 1 is marked MULTI-HOMED, and that machine's")
	fmt.Println("blast radius is the UNION of its segments. Forwarding between a machine's own")
	fmt.Println("segment interfaces stays denied by default; crossing is a separate, declared")
	fmt.Println("exception.")
	if bridges > 0 {
		fmt.Printf("\n%d machine(s) here are in more than one segment. `hz machine ls --multi-homed`\n", bridges)
		fmt.Println("lists exactly those, with the declared reason for each.")
	}
	fmt.Println("\nENROLLED is whether hz holds an agent credential for the box. A machine has no")
	fmt.Println("project, no environment and no observed version: those belong to the instances")
	fmt.Println("running on it, and several instances share a box.")
	fmt.Println("\nhz machine show <name> shows one machine.")
	return nil
}

// printMultiHomed is the enumeration architecture.md asks for by name: every
// machine that bridges segments, what it bridges, and why. "Bad design but
// possible" becomes "possible, visible, and it has to be explained" only if
// there is a command that prints the list.
func printMultiHomed(machines []apitypes.MachineResp) error {
	var rows []apitypes.MachineResp
	for _, m := range machines {
		if m.MultiHomed {
			rows = append(rows, m)
		}
	}
	if len(rows) == 0 {
		fmt.Println("No machine is in more than one segment.")
		fmt.Printf("(%d machine(s) declared, each in one segment or none.)\n", len(machines))
		return nil
	}
	fmt.Printf("%d machine(s) bridge segments. Each one's blast radius is the UNION of its\n", len(rows))
	fmt.Println("segments, and each is a declared exception with a reason:")
	for _, m := range rows {
		fmt.Printf("\n%s  MULTI-HOMED (%d segments)\n", m.Name, len(m.Segments))
		fmt.Printf("  bridges   %s\n", strings.Join(m.Segments, "  ·  "))
		fmt.Printf("  declared  %s\n", dash(m.Note))
		fmt.Printf("  enrolled  %s\n", yesNo(m.Enrolled))
	}
	fmt.Println("\nForwarding between a machine's own segment interfaces is denied by default.")
	fmt.Println("Membership is not a crossing; a crossing is declared separately.")
	return nil
}

func machineShow(c *client, args []string) error {
	fs := flag.NewFlagSet("machine show", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: hz machine show <machine>")
	}
	want := fs.Arg(0)
	machines, err := fetchMachines(c)
	if err != nil {
		return err
	}
	for _, m := range machines {
		if m.Name != want {
			continue
		}
		fmt.Printf("%s\n", m.Name)
		printMachine(m, "  ")
		fmt.Println("\n  project          — (a machine has none; a project is an instance's coordinate)")
		fmt.Println("  environment      — (likewise: one box hosts instances from several)")
		fmt.Println("  observed version — (belongs to an instance; several share a box)")
		return nil
	}
	known := make([]string, 0, len(machines))
	for _, m := range machines {
		known = append(known, m.Name)
	}
	if len(known) == 0 {
		return fmt.Errorf("no machine %q — this config declares no machines at all; `hz machine add %s` declares one", want, want)
	}
	return fmt.Errorf("no machine %q — `hz machine ls` lists what exists. Declared: %s", want, strings.Join(known, ", "))
}

// printMachine renders one machine the way show does, so a write and a read of
// the same record look the same — the convention printEnvironment sets.
func printMachine(m apitypes.MachineResp, prefix string) {
	fmt.Printf("%ssegments         %s\n", prefix, machineSegments(m))
	fmt.Printf("%senrolled         %s\n", prefix, yesNo(m.Enrolled))
	if m.MultiHomed {
		fmt.Printf("%sMULTI-HOMED      in %d segments; blast radius is the union of them\n", prefix, len(m.Segments))
		fmt.Printf("%sdeclared reason  %s\n", prefix, dash(m.Note))
		fmt.Printf("%sForwarding between its own segment interfaces is denied by default.\n", prefix)
	} else if m.Note != "" {
		fmt.Printf("%snote             %s\n", prefix, m.Note)
	}
	if len(m.Segments) == 0 {
		fmt.Printf("%sIn no segment yet. It is declared, so it can be enrolled; it peers with nothing.\n", prefix)
	}
	if !m.Enrolled {
		fmt.Printf("%sNo agent credential issued. From the box, as root:\n", prefix)
		fmt.Printf("%s  hz-agent enroll --hz <hz url> --machine %s\n", prefix, m.Name)
	}
}

func machineSegments(m apitypes.MachineResp) string {
	if len(m.Segments) == 0 {
		return "—"
	}
	s := strings.Join(m.Segments, ", ")
	if m.MultiHomed {
		return s + "   MULTI-HOMED"
	}
	return s
}

func machineSegmentCount(m apitypes.MachineResp) string {
	return fmt.Sprintf("%d", len(m.Segments))
}
