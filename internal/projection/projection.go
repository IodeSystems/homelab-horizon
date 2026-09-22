// Package projection is `project(global, machineID) -> MachineConfig`: what hz
// says one machine should look like, computed from hz's records alone.
//
// It is phase 4 item 14 of plan/architecture.md, and the generalisation of a
// thing hz already did for exactly one box. `internal/haproxy`,
// `internal/dnsmasq`, `internal/iptables` and `internal/wireguard` are all
// `render(global) -> local files -> reload`, hardcoded to the machine hz runs
// on. This adds the machine parameter.
//
// # Pure, and why that is the whole point
//
// No file is read, no command is run, no clock is consulted and no environment
// variable is looked at. Everything this function needs arrives in Global.
// seam_test.go enforces it the same way the five subsystem packages enforce
// theirs. Purity buys three things that are not stylistic:
//
//   - The projection for a machine you are not on can be computed. That is the
//     entire premise of a fleet: hz has to be able to say what `app-1` should
//     look like without being `app-1`.
//   - It is diffable before anything is applied — "what would change on box X"
//     is a question with an offline answer.
//   - It is testable against a fixture estate with no machine anywhere. See
//     projection_test.go, which runs plan/example-projection.md §3.
//
// # The gateway is machine #1, not a special case
//
// architecture.md is explicit about this and the code honours it literally:
// there is no branch in here for the box hz runs on. hz's own machine goes
// through Project exactly as every other machine does.
//
// What the gateway ALSO has is sections hz produces by reading local files —
// wg0.conf read back, the served certificate bundles, the live firewall's
// classifier inputs. Those are not projections and cannot be: hz cannot read
// `app-1`'s files. They are composed onto the projection AT THE CALL SITE
// (internal/server/handlers_agent.go, desiredFor), which keeps the special
// thing about the gateway — that hz can open its filesystem — outside the
// model, where it belongs.
//
// # What hz cannot compute, and why it says so out loud
//
// A projection that silently omits a section it could not compute is
// indistinguishable from one that computed "nothing is wanted here". Those are
// different answers and the drift screen already tells *unmanaged* from
// *unreadable* from *empty* for exactly this reason. So every section this
// function leaves empty because it does not know, rather than because nothing
// is wanted, is named in Unresolved with the reason. A reader of a
// MachineConfig can therefore always tell hz's opinion from hz's silence.
//
// Today the biggest such gap is the Segment record, which does not exist:
// Machine.Segments holds NAMES that resolve against nothing (item 15). So hz
// can say which segments a machine is a member of and cannot say what
// interface, address or peer set that membership means.
package projection

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// MachineConfig is what hz says one machine should look like: the struct
// plan/architecture.md names under "The projection", populated the way
// plan/example-projection.md §5 shows.
//
// Every slice is non-nil after Project returns, so "hz wants nothing here" is
// an empty list rather than a null — and a section hz could not compute is an
// empty list PLUS an entry in Unresolved. The two are not the same state and
// the type refuses to conflate them.
type MachineConfig struct {
	// Machine is who this was computed for. It is the input, echoed, so a
	// MachineConfig that has been passed around still says whose it is.
	Machine string `json:"machine"`

	// Serial is architecture.md's monotonic floor — the rollback defence where
	// hz refuses to serve a machine a generation older than one it has already
	// applied ("apply → must re-reach hz within N seconds → else roll back to
	// the previous Serial"). NOTHING PRODUCES ONE: projectionGlobal passes 0
	// and this is always 0 today.
	//
	// It is an INPUT rather than something this function invents, because a
	// counter is state somebody has to keep correct across restarts and a pure
	// function has no state.
	//
	// IT IS NOT THE GENERATION, and it is not waiting on a spec.
	// example-projection.md §5 used to show a serial pair and no longer does:
	// item 11 chose a content hash (agent.Desired.Fingerprint) for "is this
	// current", deliberately, and that question is answered. A floor answers a
	// different one — "is this OLDER than what I already ran" — which nothing
	// has asked for yet. The field is carried rather than dropped because the
	// defence architecture.md wants it for is real and this is where it lands;
	// if it is still 0 when someone next reads this, the honest options are to
	// build the floor or to delete the field, not to re-explain it.
	Serial uint64 `json:"serial"`

	// Segments are the network segments this machine is a member of.
	//
	// NAMES ONLY TODAY. See Segment.Resolved.
	Segments []Segment `json:"segments"`

	// Forwards are the declared exceptions to "a machine may not forward
	// between its own segment interfaces".
	//
	// EMPTY IS AN OPINION HERE, not a gap: architecture.md's rule is that
	// forwarding between a machine's own segments is denied BY DEFAULT and a
	// crossing is a declared exception. Nothing can declare one yet, so the
	// answer is deny, which is the answer the rule already gives. A
	// multi-homed machine's Note explains the crossing to a human; it is prose
	// and confers nothing, which is why it does not become a Forward.
	Forwards []Forward `json:"forwards"`

	// Hosts is what /etc/hosts should carry — the names this machine resolves
	// without asking anything.
	//
	// Empty and unresolvable while segments are: example-projection.md §5's
	// single entry is the gateway's address ON THAT SEGMENT, and no record
	// holds a per-segment address.
	Hosts []HostEntry `json:"hosts"`

	// Packages is what this machine should have installed, at an exact
	// version, held. Derived from the instances registered on it: an instance
	// at environment E of project P means P's package at E's declared version.
	Packages []Package `json:"packages"`

	// Feeds are the apt sources those packages come from, resolved up the
	// project tree by config.ResolveFeed.
	//
	// Not in example-projection.md §5, which shows packages without saying
	// where they come from; a machine cannot install a held version without
	// the source that carries it, so the projection answers both halves.
	Feeds []Feed `json:"feeds"`

	// Units is which instance units should exist and be enabled on this box.
	Units []Unit `json:"units"`

	// Unresolved is what hz could NOT compute for this machine, and why.
	//
	// This field is the difference between a projection and a guess. An empty
	// section with no gap beside it is hz saying "nothing is wanted here"; an
	// empty section with a gap is hz saying "I have no opinion". Anything
	// rendering a MachineConfig has to be able to tell those apart, and
	// without this it cannot.
	Unresolved []Gap `json:"unresolved,omitempty"`
}

// Segment is one network-segment membership.
type Segment struct {
	Name string `json:"name"`

	// Interface, Address and Peers are what the membership MEANS on the box,
	// and they are empty until item 15.
	Interface string   `json:"interface,omitempty"`
	Address   string   `json:"address,omitempty"`
	Peers     []string `json:"peers,omitempty"`

	// Resolved says whether the three fields above were computed or merely
	// left out. False means this entry is a declared MEMBERSHIP OF A LABEL and
	// nothing more — there is no Segment record to resolve the name against
	// (phase 4 item 15), so an empty Interface here does not mean "no
	// interface", it means hz does not know.
	//
	// Rendered even when false (no omitempty) because a reader must not have
	// to infer it from an absence.
	Resolved bool `json:"resolved"`
}

// Forward is a declared exception to the default deny between a machine's own
// segment interfaces.
//
// Distinct from config.Forward, which is a layer-4 port forward on the gateway
// (UDP/QUIC traffic HAProxy cannot carry). Same word, different subject: that
// one crosses from the public internet to a LAN address, this one crosses
// between two segments a machine is a member of.
type Forward struct {
	From   string `json:"from"`   // segment name
	To     string `json:"to"`     // segment name
	Reason string `json:"reason"` // why this crossing is allowed
}

// HostEntry is one /etc/hosts line.
type HostEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// Package is one thing to install, at one version, pinned there.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`

	// Hold is `apt-mark hold`: this machine installs exactly this version and
	// unattended-upgrades does not move it (architecture.md phase 3 item 8).
	// True whenever a version is declared — a package hz named a version for
	// and did not hold is a package that upgrades itself out of the version hz
	// declared.
	Hold bool `json:"hold"`
}

// Feed is an apt source, and which project's declaration supplied it.
type Feed struct {
	// From is the project that DECLARED the feed, which is not necessarily a
	// project this machine hosts: ResolveFeed walks up the tree and the
	// nearest declaration wins whole. Carried because "where did this source
	// come from" is the question the second return value of ResolveFeed exists
	// to answer, and dropping it here would throw that answer away.
	From string `json:"from"`

	URL       string `json:"url"`
	Suite     string `json:"suite"`
	Component string `json:"component"`
	KeyID     string `json:"key_id,omitempty"`
}

// Unit is one systemd unit that should exist and be enabled on this machine.
type Unit struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// Gap is one thing hz could not compute for this machine, named and explained.
//
// Section is one of the Section* constants — a stable key a screen can branch
// on. Why is prose for a human, and it names what would close the gap, because
// "hz does not know" without "and here is what would tell it" is a dead end.
type Gap struct {
	Section string `json:"section"`
	Why     string `json:"why"`
}

// Section keys. Stable strings: a screen branches on these, and the first four
// name sections of MachineConfig itself while the rest name sections of the
// agent payload composed around it (the caller appends those — see
// handlers_agent.go).
const (
	SectionMachine   = "machine"
	SectionSegments  = "segments"
	SectionHosts     = "hosts"
	SectionPackages  = "packages"
	SectionFeeds     = "feeds"
	SectionUnits     = "units"
	SectionInstances = "instances"
)

// Instance is one (machine, environment, app, role) a box has booted as — a
// registration, flattened to values.
//
// It arrives as an ARGUMENT rather than being read, because registrations live
// in hz's database and a pure function may not open one. The caller that owns
// the database passes what it knows, exactly as haproxy.CertStore is an input
// to the HTTPS renderer and Config.MachineRemoval takes `enrolled` rather than
// reading the credential store.
//
// Machine is the machine NAME (cm_machines.name), not the row id: the config's
// Machine record is keyed by name and this is the field the two are joined on.
type Instance struct {
	Machine     string
	Environment string
	App         string
	Role        string
}

// Address is the instance's address as hz prints it everywhere else,
// environment/app/role. No project: see the note on Project's environment
// resolution for why that is the interesting part.
func (i Instance) Address() string {
	return i.Environment + "/" + i.App + "/" + i.Role
}

// Global is everything the projection reads: hz's declared config plus the
// registrations, which live outside it.
//
// A struct rather than a list of parameters so that adding an input is a
// visible change to what the projection depends on, and so the purity guard
// has something to point at: if it is not in here, the projection cannot see
// it.
type Global struct {
	// Config is hz's declared state — projects, environments, machines, feeds.
	// Read only; nothing here writes through it.
	Config *config.Config

	// Instances is every registration hz holds, for every machine. Project
	// filters to the one it was asked about rather than making the caller do
	// it, so a caller cannot accidentally narrow it wrongly.
	Instances []Instance

	// AgentVersion is the hz-agent version hz wants the fleet on.
	// example-projection.md §5 carries it as a package beside the app's.
	//
	// EMPTY MEANS NO PACKAGE, not "latest": hz declaring a package with no
	// version would be hz telling a box to install whatever the feed happens
	// to hold, which is the opposite of an exact version under hold.
	AgentVersion string

	// Serial is the monotonic floor to stamp on the result, and nothing
	// produces one: every caller passes 0. See MachineConfig.Serial for what a
	// floor is for, why it is carried in rather than computed, and why it is
	// not the generation.
	Serial uint64
}

// Project is the projection: what hz says machineID should look like.
//
// PURE. Every input is in g; nothing is read, run or timed. The only error is
// a caller mistake — an empty machine name — because everything the projection
// cannot work out about a real machine is a Gap rather than a failure. A
// machine hz knows nothing about still gets an answer: an empty one, saying so.
func Project(g Global, machineID string) (MachineConfig, error) {
	machineID = strings.TrimSpace(machineID)
	if machineID == "" {
		return MachineConfig{}, errors.New("a projection needs a machine name: project(global, machineID) has no answer for the empty machine")
	}

	cfg := g.Config
	if cfg == nil {
		cfg = &config.Config{}
	}

	mc := MachineConfig{
		Machine:  machineID,
		Serial:   g.Serial,
		Segments: []Segment{},
		Forwards: []Forward{},
		Hosts:    []HostEntry{},
		Packages: []Package{},
		Feeds:    []Feed{},
		Units:    []Unit{},
	}

	m, declared := cfg.FindMachine(machineID)
	if !declared {
		// NOT AN ERROR. hz runs on a box that may predate the Machine record,
		// and a registration can name a machine nobody declared. Both are
		// real states and both deserve a projection that says what it does not
		// know rather than nothing at all.
		mc.gap(SectionMachine, "no machine record declares "+machineID+
			", so hz knows no segment membership for it — `hz machine add "+machineID+" --segment <segment>` declares one."+
			" Instances registered on it are still projected below.")
	}

	projectSegments(&mc, m)
	projectInstances(&mc, cfg, g.Instances, machineID)
	projectAgent(&mc, g.AgentVersion)

	return mc, nil
}

// projectSegments turns a machine's segment memberships into Segment entries,
// and says what a membership does not yet mean.
func projectSegments(mc *MachineConfig, m config.Machine) {
	for _, name := range m.Segments {
		mc.Segments = append(mc.Segments, Segment{Name: name, Resolved: false})
	}
	if len(m.Segments) == 0 {
		// Nothing declared, nothing unknown. A machine with no segments has an
		// empty segment list because it has no segments, and that is an
		// opinion rather than a gap — example-projection.md §3's `new-box`
		// before approval, or a box that only ever talks on the LAN.
		return
	}

	mc.gap(SectionSegments, "hz can name this machine's segments and not what they mean:"+
		" nothing resolves a segment NAME to a CIDR, an interface, a per-machine address or a peer set."+
		" That record is phase 4 item 15. Until it exists a membership is a declaration about a label,"+
		" so `interface`, `address` and `peers` are absent because hz does not know them, not because they are empty.")

	// /etc/hosts follows the segments. example-projection.md §5's single entry
	// is the gateway's address ON A SEGMENT, which is precisely the thing no
	// record holds, so this gap is a consequence of the one above rather than
	// an independent hole — said separately anyway, because a reader looking
	// at an empty `hosts` must not have to derive it.
	mc.gap(SectionHosts, "an /etc/hosts entry is a peer's address on a segment, and no record holds a per-segment address (phase 4 item 15),"+
		" so hz has no host entries to declare rather than declaring that this machine should have none.")
}

// projectInstances is the join architecture.md describes and
// example-projection.md §5 shows the output of:
//
//	registration (machine, environment, app, role)
//	  -> the environment it names            -> the project that declares it
//	  -> Environment.Version                 -> the package version, held
//	  -> config.ResolveFeed(project)         -> where that package comes from
//	  -> project + role                      -> the unit that runs it
//
// Every step of it can fail to resolve against today's records, and each
// failure is a Gap rather than a dropped row.
func projectInstances(mc *MachineConfig, cfg *config.Config, all []Instance, machineID string) {
	mine := make([]Instance, 0, len(all))
	for _, inst := range all {
		if inst.Machine == machineID {
			mine = append(mine, inst)
		}
	}
	if len(mine) == 0 {
		// NOTHING TO HOST IS AN ANSWER, and a correct one:
		// example-projection.md §3's ci-1 has a segment, an agent and a
		// healthy poll and hosts nothing, permanently and by design. No gap —
		// hz is not failing to work something out here.
		return
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Address() < mine[j].Address() })

	// Package name is the PROJECT, so two instances of one project on one
	// machine (storefront/staging/web/app and .../web/next — the two slots)
	// are one package. Which is also where a real conflict can appear: two
	// ENVIRONMENTS of one project on one machine declare two versions of one
	// package name, and apt cannot hold two versions of one path
	// (architecture.md, "Versions").
	type pkgSource struct {
		version string
		from    Instance
	}
	packages := map[string]pkgSource{}
	feeds := map[string]Feed{}
	units := map[string][]string{} // unit name -> the instance addresses that produced it
	var unitOrder []string
	seenFeedGap := map[string]bool{}

	for _, inst := range mine {
		env, err := resolveEnvironment(cfg, inst)
		if err != nil {
			mc.gap(SectionInstances, "instance "+inst.Address()+" cannot be resolved to a project: "+err.Error()+
				". It contributes no package, feed or unit — a guess here would pin this machine to another project's version.")
			continue
		}

		// The desired version is the ENVIRONMENT's, declared, opaque to hz.
		if strings.TrimSpace(env.Version) == "" {
			mc.gap(SectionPackages, "instance "+inst.Address()+" runs "+env.Project+"/"+env.Name+
				", which declares no version — hz will not tell a machine to install an unspecified one,"+
				" so no package is projected for it. `hz env set "+env.Project+"/"+env.Name+" --version <v>` declares one.")
		} else if prev, dup := packages[env.Project]; dup && prev.version != env.Version {
			mc.gap(SectionPackages, "package "+env.Project+" would be pinned to two versions on this machine: "+
				prev.version+" (from "+prev.from.Address()+") and "+env.Version+" (from "+inst.Address()+")."+
				" apt cannot hold two versions of one package name, so hz projects the first and names the conflict rather than picking.")
		} else if !dup {
			packages[env.Project] = pkgSource{version: env.Version, from: inst}
		}

		// The feed cascades: nearest declaration up the project tree wins
		// whole (internal/config/feed.go).
		feed, from, err := cfg.ResolveFeed(env.Project)
		switch {
		case err != nil:
			if !seenFeedGap[env.Project] {
				seenFeedGap[env.Project] = true
				mc.gap(SectionFeeds, "the package feed for project "+env.Project+" cannot be resolved: "+err.Error())
			}
		case feed == nil:
			if !seenFeedGap[env.Project] {
				seenFeedGap[env.Project] = true
				mc.gap(SectionFeeds, "project "+env.Project+" declares no package feed and neither does anything above it in the tree,"+
					" so hz cannot say where this machine installs "+env.Project+" from."+
					" `hz feed set "+env.Project+" --url … --suite … --component …` declares one,"+
					" and declaring it on an ancestor serves every project under it.")
			}
		default:
			key := feed.URL + "\x00" + feed.Suite + "\x00" + feed.Component + "\x00" + feed.KeyID
			if _, have := feeds[key]; !have {
				feeds[key] = Feed{From: from, URL: feed.URL, Suite: feed.Suite, Component: feed.Component, KeyID: feed.KeyID}
			}
		}

		// THE UNIT NAME, AND THE SHAPE example-projection.md §5 GIVES IT:
		// <project>@<environment>-<app>-<role>.service, where the instance
		// part is systemd-escape of the instance address environment/app/role.
		// So storefront/prod/web/app on app-1 is
		// `storefront@prod-web-app.service`. See unitName.
		name := unitName(env.Project, inst)
		if _, have := units[name]; !have {
			unitOrder = append(unitOrder, name)
		}
		units[name] = append(units[name], inst.Address())
	}

	for _, name := range sortedKeys(packages) {
		mc.Packages = append(mc.Packages, Package{Name: name, Version: packages[name].version, Hold: true})
	}
	for _, key := range sortedKeys(feeds) {
		mc.Feeds = append(mc.Feeds, feeds[key])
	}
	sort.Strings(unitOrder)
	for _, name := range unitOrder {
		mc.Units = append(mc.Units, Unit{Name: name, Enabled: true})
		// ONE UNIT IS ONE INSTANCE, and this says so when it is not.
		//
		// The NAME can no longer merge two instances — unitName carries all
		// four coordinates and is reversible (see its doc comment) — so what
		// is left for this to catch is the INPUT: Global.Instances is an
		// argument, and nothing in here can know that cm_registrations is
		// unique on (machine, environment, app, role). A caller that joins its
		// tables carelessly and hands the same address twice is told, rather
		// than quietly getting one unit for two rows it believes in. It also
		// stays the standing guard on the naming itself: a scheme that drops a
		// coordinate again lands here on the very estate that found the
		// original bug.
		if owners := units[name]; len(owners) > 1 {
			mc.gap(SectionUnits, "unit "+name+" is the projection of "+fmt.Sprint(len(owners))+" instances ("+
				strings.Join(owners, ", ")+"), and one unit is one instance. A unit name carries the project and all three"+
				" coordinates of the address, and a registration is unique on (machine, environment, app, role), so two instances"+
				" cannot legitimately share a name on one machine — this machine has been handed the same address more than once."+
				" hz projects one unit and names the duplicate rather than running one service where its input declares several.")
		}
	}
}

// unitName is the systemd unit one instance runs under, and it is
// example-projection.md §5's scheme:
//
//	<project>@<environment>-<app>-<role>.service
//
// where the instance part is systemd-escape of the instance address
// environment/app/role. `storefront` + `prod/web/app` is
// `storefront@prod-web-app.service`.
//
// THE PROJECT IS THE PREFIX, not a fourth coordinate of the instance part,
// because the project IS the package name. A systemd template unit
// `<project>@.service` ships in `<project>`'s package, so the `packages` entry
// of this very payload is what provides its `units` entry. Any other prefix
// leaves nothing in the payload saying which package the unit came from.
//
// ALL THREE OF environment, app AND role are in the instance part because
// dropping any one of them collides on the estate example-projection.md §3
// already describes:
//
//   - dropping APP was the original bug — `intern/prod/git/app` and
//     `intern/prod/idp/app` are two apps of one project at one role on gw-1,
//     and both become `intern@app.service`;
//   - dropping ROLE collides gw-1's three storefront slots (app, next, ops);
//   - dropping ENVIRONMENT collides a box hosting two rungs of one project,
//     which is the same case the package-version conflict gap already names.
//
// AND IT CANNOT COLLIDE. A registration is unique on
// (machine, environment, app, role) — migration 0009 — so the triple is
// already unique on one machine, systemdEscape is injective (it is reversible;
// see its doc comment), and prefixing with the project can only narrow the
// name space, never merge two names into one.
func unitName(project string, inst Instance) string {
	return project + "@" + systemdEscape(inst.Address()) + ".service"
}

// unitNameLiteral is what systemd leaves alone inside a unit name: its
// VALID_CHARS less `-` and `\`, which the escaper handles before it gets here
// because they are the separator and the escape.
const unitNameLiteral = "0123456789" +
	"abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	":_."

const hexDigits = "0123456789abcdef"

// systemdEscape is `systemd-escape`: the transformation systemd itself applies
// when a path becomes a template unit's instance parameter.
//
// IMPLEMENTED HERE RATHER THAN SHELLED OUT, and that is not a convenience.
// The projection is pure — seam_test.go bans os/exec outright — and a name for
// app-1 computed by running a command on gw-1 would be a statement about
// gw-1's systemd rather than about app-1. The projection has to be computable
// for a machine hz has never touched, which rules out asking the local box
// what it thinks.
//
// The rules are systemd's own `do_escape` (src/basic/unit-name.c), per BYTE:
//
//   - a LEADING '.' escapes, so an instance parameter can never open a dotfile
//   - '/' becomes '-' — systemd's own path separator escape
//   - '-' and '\' always escape, being the separator and the escape itself
//   - anything outside 0-9 A-Z a-z ':' '_' '.' escapes
//   - an escape is `\x` plus two LOWERCASE hex digits
//
// The third rule is the one that matters for the unit name: a literal '-'
// inside an environment, app or role name becomes `\x2d` rather than a second
// separator, so `a-b/c` and `a/b-c` render differently (`a\x2db-c` against
// `a-b\x2dc`). The escaping is therefore reversible, and a reversible function
// is injective — which is what lets unitName claim it cannot collide for any
// estate rather than merely for §3's.
func systemdEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case i == 0 && c == '.':
			escapeByte(&b, c)
		case c == '/':
			b.WriteByte('-')
		case strings.IndexByte(unitNameLiteral, c) >= 0:
			b.WriteByte(c)
		default:
			escapeByte(&b, c)
		}
	}
	return b.String()
}

func escapeByte(b *strings.Builder, c byte) {
	b.WriteString(`\x`)
	b.WriteByte(hexDigits[c>>4])
	b.WriteByte(hexDigits[c&0x0f])
}

// resolveEnvironment answers the one question the registration record cannot:
// WHICH PROJECT is this instance's?
//
// A registration's address is (environment, app, role) and carries no project,
// while an environment name is unique PER PROJECT rather than globally —
// example-projection.md §1 has six projects declaring an environment called
// "prod". So the environment name alone is not an identity and hz has to get
// the project from somewhere else.
//
// IT GETS IT FROM THE APP COORDINATE, through a record that already exists.
// Service carries Project and Environment for exactly this reason — its own
// doc comment says naming them "lets a service be joined to the config
// manager, whose addresses are environment/app/role". The app coordinate IS
// the service name, so `prod/web/app` resolves through the service called
// `web` to the project that declares it, and the environment is then looked up
// within that project.
//
// The service's OWN environment is deliberately not required to match. A
// service sits on one rung and a project's package ships to several: `web` is
// declared at storefront/staging and app-1 runs storefront/prod. What the
// service supplies here is the PROJECT; the rung comes from the registration.
//
// Failing that, a globally unique environment name still identifies one. That
// is the common small estate — one project, one "prod" — and refusing it would
// make the projection useless everywhere the ambiguity does not exist.
//
// Both roads closed is a real dead end and returns an error, which becomes a
// gap. It is not a fault in the config: it is the missing project coordinate
// on the registration address, and the message says so.
func resolveEnvironment(cfg *config.Config, inst Instance) (config.Environment, error) {
	projects := map[string]bool{}
	for _, svc := range cfg.Services {
		if svc.Name == inst.App && strings.TrimSpace(svc.Project) != "" {
			projects[svc.Project] = true
		}
	}

	switch len(projects) {
	case 1:
		project := sortedKeys(projects)[0]
		env, err := cfg.LookupEnvironment(project, inst.Environment)
		if err != nil {
			return config.Environment{}, fmt.Errorf("app %q is service %q of project %q, which declares no environment %q — `hz env add %s/%s --posture <dev|staging|prod>` declares the rung",
				inst.App, inst.App, project, inst.Environment, project, inst.Environment)
		}
		return env, nil
	case 0:
		// No service of that name. Fall back to the environment name, which is
		// an identity only while exactly one project uses it.
		env, err := cfg.LookupEnvironment("", inst.Environment)
		if err != nil {
			return config.Environment{}, fmt.Errorf("%v. A registration's address is environment/app/role with no project coordinate, and no service is named %q to supply one — `hz service assign %s <project>/%s` joins the app to a project",
				err, inst.App, inst.App, inst.Environment)
		}
		return env, nil
	default:
		return config.Environment{}, fmt.Errorf("app %q is a service of %s, so hz cannot tell which project's rung %q is",
			inst.App, strings.Join(sortedKeys(projects), " and "), inst.Environment)
	}
}

// ResolveEnvironment is resolveEnvironment, exported for the one other caller
// that has to answer the same question about the same registration.
//
// The version-drift join (internal/server/handlers_version_drift.go) needs an
// instance's DECLARED version, and that version is `Environment.Version` on the
// rung the instance names — so it needs the project coordinate a registration
// address does not carry, by exactly the road resolveEnvironment documents.
// Re-deriving it there would give hz two answers to "which project is
// prod/web/app?", and the screen showing drift would then be free to compare
// against a different rung than the projection installs from.
//
// Exported rather than moved: this is the projection's join, the projection is
// its primary caller, and the error text names the `hz` command that closes
// each dead end — which the drift row renders verbatim.
func ResolveEnvironment(cfg *config.Config, inst Instance) (config.Environment, error) {
	return resolveEnvironment(cfg, inst)
}

// projectAgent adds hz-agent itself to the package list.
//
// example-projection.md §5 carries it beside the application's package, held at
// an exact version like everything else, and it is the one package every
// machine with an agent wants regardless of what it hosts.
func projectAgent(mc *MachineConfig, version string) {
	if strings.TrimSpace(version) == "" {
		return
	}
	mc.Packages = append(mc.Packages, Package{Name: AgentPackage, Version: version, Hold: true})
}

// AgentPackage is what hz-agent is called in a feed.
const AgentPackage = "hz-agent"

// AddGap records something hz could not compute for this machine.
//
// EXPORTED FOR THE CALL SITE, which is the other half of the composition. The
// projection covers the sections it produces; the caller that composes
// locally-read sections onto it — internal/server's desiredFor — covers the
// ones it could not read, and has to be able to say so in the same place. A
// reader of a MachineConfig then has one list of everything hz does not know
// about this machine, rather than one list and a convention.
//
// Recorded once: the same missing record reached from four instances is one
// gap, not four.
func (mc *MachineConfig) AddGap(section, why string) { mc.gap(section, why) }

// gap records something hz could not compute, once. Repeats are dropped: the
// same missing record reached from four instances is one gap, not four.
func (mc *MachineConfig) gap(section, why string) {
	for _, g := range mc.Unresolved {
		if g.Section == section && g.Why == why {
			return
		}
	}
	mc.Unresolved = append(mc.Unresolved, Gap{Section: section, Why: why})
}

// Gaps reports whether hz left anything unresolved, which is the question a
// caller composing more sections onto this asks before deciding what its own
// silence means.
func (mc MachineConfig) Gaps() bool { return len(mc.Unresolved) > 0 }

// Unresolvable reports whether a named section is one hz could not compute.
func (mc MachineConfig) Unresolvable(section string) bool {
	for _, g := range mc.Unresolved {
		if g.Section == section {
			return true
		}
	}
	return false
}

// sortedKeys is map iteration made deterministic. A projection is diffed
// against the last one and hashed into a generation, so a map's order would
// turn "nothing changed" into a change on every other poll.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
